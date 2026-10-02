package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/jackc/pgx/v5"
)

type noLive struct{}

func (noLive) AllBindings(context.Context) ([]livestate.Binding, error) { return nil, nil }
func (noLive) Calls(context.Context) ([]livestate.Call, error)          { return nil, nil }

type auditRow struct{ actor, action, resource, resourceID string }

// TestConfigChangeAuditedAndRevisioned drives every extension and device
// mutation through the API and checks each writes exactly one audit row,
// raises config_revision by exactly one, and notifies hello_config with the
// new revision; a rejected change does none of these.
func TestConfigChangeAuditedAndRevisioned(t *testing.T) {
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dsn = scratchDatabase(t, ctx, dsn)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	hash, err := auth.HashPassword("pw-for-alice")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser(ctx, "test", "alice", hash)
	if err != nil {
		t.Fatal(err)
	}
	plain, tokHash := auth.NewToken()
	tok, err := st.CreateToken(ctx, "test", uid, "it", tokHash)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.Handler(api.Config{Store: st, Live: noLive{}, SIPDomain: "hello.test", SessionTTL: time.Hour}))
	defer srv.Close()

	listener, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close(context.Background()) }()
	if _, err := listener.Exec(ctx, "LISTEN "+store.NotifyChannel); err != nil {
		t.Fatal(err)
	}

	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+plain)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var m map[string]any
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, &m)
		return resp.StatusCode, m
	}
	revision := func() int64 {
		t.Helper()
		rev, err := st.ConfigRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return rev
	}
	audits := func() []auditRow {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT actor, action, resource, resource_id FROM audit_events ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []auditRow
		for rows.Next() {
			var a auditRow
			if err := rows.Scan(&a.actor, &a.action, &a.resource, &a.resourceID); err != nil {
				t.Fatal(err)
			}
			out = append(out, a)
		}
		return out
	}
	actor := "token:" + strconv.FormatInt(tok.ID, 10)

	// change performs one mutation and checks its audit row, revision bump
	// and notification.
	change := func(method, path string, body any, wantCode int, action, resource string, id func(map[string]any) string) map[string]any {
		t.Helper()
		rev, before := revision(), len(audits())
		code, m := call(method, path, body)
		if code != wantCode {
			t.Fatalf("%s %s = %d %v, want %d", method, path, code, m, wantCode)
		}
		if got := revision(); got != rev+1 {
			t.Fatalf("%s %s: config_revision %d -> %d, want exactly +1", method, path, rev, got)
		}
		all := audits()
		want := auditRow{actor, action, resource, id(m)}
		if len(all) != before+1 || all[len(all)-1] != want {
			t.Fatalf("%s %s: audit rows %d -> %d, last %+v; want one new %+v", method, path, before, len(all), all[len(all)-1], want)
		}
		wctx, wcancel := context.WithTimeout(ctx, 2*time.Second)
		defer wcancel()
		n, err := listener.WaitForNotification(wctx)
		if err != nil {
			t.Fatalf("%s %s: no %s notification: %v", method, path, store.NotifyChannel, err)
		}
		if n.Channel != store.NotifyChannel || n.Payload != strconv.FormatInt(rev+1, 10) {
			t.Fatalf("%s %s: notification %s %q, want %s %d", method, path, n.Channel, n.Payload, store.NotifyChannel, rev+1)
		}
		return m
	}
	// rejected checks a failing mutation leaves revision and audit alone and
	// sends no notification.
	rejected := func(method, path string, body any, wantCode int) {
		t.Helper()
		rev, before := revision(), len(audits())
		if code, m := call(method, path, body); code != wantCode {
			t.Fatalf("%s %s = %d %v, want %d", method, path, code, m, wantCode)
		}
		if got, n := revision(), len(audits()); got != rev || n != before {
			t.Fatalf("rejected %s %s changed revision %d -> %d, audit rows %d -> %d", method, path, rev, got, before, n)
		}
		wctx, wcancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer wcancel()
		if n, err := listener.WaitForNotification(wctx); err == nil {
			t.Fatalf("rejected %s %s notified %q", method, path, n.Payload)
		}
	}
	idOf := func(m map[string]any) string { return fmt.Sprint(m["id"]) }
	fixed := func(id string) func(map[string]any) string { return func(map[string]any) string { return id } }

	ext := change("POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Reception"}, 201, "create", "extension", idOf)
	extID := idOf(ext)
	rejected("POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Again"}, 409)
	rejected("POST", "/api/v1/extensions", map[string]string{"number": "1", "name": "Short"}, 400)
	change("PATCH", "/api/v1/extensions/"+extID, map[string]string{"name": "Front desk"}, 200, "update", "extension", idOf)
	ext2 := change("POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Sales"}, 201, "create", "extension", idOf)
	rejected("PATCH", "/api/v1/extensions/"+idOf(ext2), map[string]string{"number": "101"}, 409)

	dev := change("POST", "/api/v1/devices", map[string]any{"extensionId": ext["id"], "sipUsername": "101-desk"}, 201, "create", "device", idOf)
	devID := idOf(dev)
	rejected("POST", "/api/v1/devices", map[string]any{"extensionId": ext["id"], "sipUsername": "101-desk"}, 409)
	change("PATCH", "/api/v1/devices/"+devID, map[string]any{"enabled": false, "extensionId": ext2["id"]}, 200, "update", "device", idOf)
	change("POST", "/api/v1/devices/"+devID+"/rotate-secret", nil, 200, "rotate-secret", "device", idOf)
	change("DELETE", "/api/v1/devices/"+devID, nil, 204, "delete", "device", fixed(devID))
	rejected("DELETE", "/api/v1/devices/"+devID, nil, 404)

	change("DELETE", "/api/v1/extensions/"+extID, nil, 204, "delete", "extension", fixed(extID))
	rejected("DELETE", "/api/v1/extensions/"+extID, nil, 404)

	// /api/v1/version reports the real revision.
	resp, err := http.Get(srv.URL + "/api/v1/version")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var v struct {
		ConfigRevision int64 `json:"configRevision"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || v.ConfigRevision != revision() || v.ConfigRevision != 8 {
		t.Fatalf("version configRevision = %d (%v), want %d = 8 changes", v.ConfigRevision, err, revision())
	}
}
