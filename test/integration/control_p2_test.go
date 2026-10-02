package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/jackc/pgx/v5"
)

// acceptRouter compiles any configuration; the routing engine itself is
// tested in internal/routing.
type acceptRouter struct{}

func (acceptRouter) Compile(routing.Config) (api.RouteTable, []routing.FieldError) {
	return acceptTable{}, nil
}

type acceptTable struct{}

func (acceptTable) Decide(routing.Call, routing.TrunkUsability) routing.Decision {
	return routing.Decision{Kind: routing.KindReject, RejectCode: 404}
}

func p2Store(t *testing.T, ctx context.Context) (*sql.DB, *store.Store, string) {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	dsn = scratchDatabase(t, ctx, dsn)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	box, err := secret.New(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	return db, store.New(db).WithSecretBox(box), dsn
}

// TestRoutingChangesAuditedAndRevisioned drives every trunk, route, order
// and external-number change through the API and checks each writes one
// audit row, raises config_revision by exactly one and notifies
// hello_config with it; a rejected change does none of these.
func TestRoutingChangesAuditedAndRevisioned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, st, dsn := p2Store(t, ctx)
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
	srv := httptest.NewServer(api.Handler(api.Config{Store: st, Live: noLive{}, Router: acceptRouter{}, SIPDomain: "hello.test", SessionTTL: time.Hour}))
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
	state := func() (int64, []auditRow) {
		t.Helper()
		rev, err := st.ConfigRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
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
		return rev, out
	}
	actor := "token:" + strconv.FormatInt(tok.ID, 10)
	idOf := func(m map[string]any) string { return fmt.Sprint(m["id"]) }
	fixed := func(id string) func(map[string]any) string { return func(map[string]any) string { return id } }

	change := func(method, path string, body any, wantCode int, action, resource string, id func(map[string]any) string) map[string]any {
		t.Helper()
		rev, before := state()
		code, m := call(method, path, body)
		if code != wantCode {
			t.Fatalf("%s %s = %d %v, want %d", method, path, code, m, wantCode)
		}
		got, all := state()
		if got != rev+1 {
			t.Fatalf("%s %s: config_revision %d -> %d, want exactly +1", method, path, rev, got)
		}
		want := auditRow{actor, action, resource, id(m)}
		if len(all) != len(before)+1 || all[len(all)-1] != want {
			t.Fatalf("%s %s: audit %d -> %d rows, last %+v; want one new %+v", method, path, len(before), len(all), all[len(all)-1], want)
		}
		wctx, wcancel := context.WithTimeout(ctx, 2*time.Second)
		defer wcancel()
		n, err := listener.WaitForNotification(wctx)
		if err != nil || n.Payload != strconv.FormatInt(rev+1, 10) {
			t.Fatalf("%s %s: notification %v (%v), want revision %d", method, path, n, err, rev+1)
		}
		return m
	}
	rejected := func(method, path string, body any, wantCode int) {
		t.Helper()
		rev, before := state()
		if code, m := call(method, path, body); code != wantCode {
			t.Fatalf("%s %s = %d %v, want %d", method, path, code, m, wantCode)
		}
		if got, all := state(); got != rev || len(all) != len(before) {
			t.Fatalf("rejected %s %s changed revision %d -> %d or audit %d -> %d", method, path, rev, got, len(before), len(all))
		}
		wctx, wcancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer wcancel()
		if n, err := listener.WaitForNotification(wctx); err == nil {
			t.Fatalf("rejected %s %s notified %q", method, path, n.Payload)
		}
	}

	ext := change("POST", "/api/v1/extensions", map[string]any{"number": "101", "name": "Desk"}, 201, "create", "extension", idOf)
	change("PATCH", "/api/v1/extensions/"+idOf(ext), map[string]any{"externalNumber": "+97142000101"}, 200, "update", "extension", idOf)
	rejected("PATCH", "/api/v1/extensions/"+idOf(ext), map[string]any{"externalNumber": "x"}, 400)

	trunk := change("POST", "/api/v1/trunks", map[string]any{"name": "peer", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}}}, 201, "create", "trunk", idOf)
	tid := idOf(trunk)
	rejected("POST", "/api/v1/trunks", map[string]any{"name": "peer", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.6"}}}, 409)
	change("PATCH", "/api/v1/trunks/"+tid, map[string]any{"password": "p-" + strconv.Itoa(int(time.Now().UnixNano()%1000))}, 200, "update", "trunk", idOf)
	rejected("PATCH", "/api/v1/trunks/"+tid, map[string]any{"maxCalls": -1}, 400)

	var routes []string
	for _, name := range []string{"one", "two", "three"} {
		r := change("POST", "/api/v1/routes/outbound", map[string]any{"name": name, "matchKind": "prefix", "match": "0", "trunks": []any{trunk["id"]}}, 201, "create", "outbound_route", idOf)
		routes = append(routes, idOf(r))
	}
	rejected("POST", "/api/v1/routes/outbound", map[string]any{"name": "bad", "matchKind": "regex", "match": "(", "trunks": []any{trunk["id"]}}, 400)
	change("PATCH", "/api/v1/routes/outbound/"+routes[0], map[string]any{"enabled": false}, 200, "update", "outbound_route", idOf)
	ids := func(s ...string) []int64 {
		var out []int64
		for _, v := range s {
			n, _ := strconv.ParseInt(v, 10, 64)
			out = append(out, n)
		}
		return out
	}
	change("PUT", "/api/v1/routes/outbound/order", map[string]any{"ids": ids(routes[2], routes[0], routes[1])}, 204, "reorder", "outbound_route", fixed("order"))
	rejected("PUT", "/api/v1/routes/outbound/order", map[string]any{"ids": ids(routes[2], routes[0])}, 400)
	rejected("DELETE", "/api/v1/trunks/"+tid, nil, 409)

	in := change("POST", "/api/v1/routes/inbound", map[string]any{"name": "DID", "trunkId": trunk["id"], "destinationKind": "extension", "destination": "101"}, 201, "create", "inbound_route", idOf)
	change("PATCH", "/api/v1/routes/inbound/"+idOf(in), map[string]any{"trunkId": nil}, 200, "update", "inbound_route", idOf)
	change("PUT", "/api/v1/routes/inbound/order", map[string]any{"ids": ids(idOf(in))}, 204, "reorder", "inbound_route", fixed("order"))
	change("DELETE", "/api/v1/routes/inbound/"+idOf(in), nil, 204, "delete", "inbound_route", fixed(idOf(in)))
	for _, r := range routes {
		change("DELETE", "/api/v1/routes/outbound/"+r, nil, 204, "delete", "outbound_route", fixed(r))
	}
	change("DELETE", "/api/v1/trunks/"+tid, nil, 204, "delete", "trunk", fixed(tid))

	// The route tester reads only: no revision, no audit, no notification.
	rejected("POST", "/api/v1/routing/test", map[string]any{"from": "101", "number": "0501234567"}, 200)
}

// TestReorderAtomic fails if a reorder can leave a mix of old and new
// positions: a swap must succeed (deferred unique constraint), a rejected
// permutation or a failing whole-configuration check must leave the old
// order, and concurrent reorders must each land whole.
func TestReorderAtomic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, st, _ := p2Store(t, ctx)
	tr, err := st.CreateTrunk(ctx, "test", store.TrunkInput{Name: "peer", Mode: "ip", RegisterExpires: 3600, OptionsInterval: 30,
		Enabled: true, Destinations: []routing.Destination{{Host: "10.0.0.5", Weight: 1}}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := range 5 {
		r, err := st.CreateOutboundRoute(ctx, "test", store.OutboundRoute{Name: fmt.Sprint("r", i), MatchKind: "prefix", Match: "0",
			Trunks: []int64{tr.ID}, FailoverCodes: store.DefaultFailoverCodes, Enabled: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	order := func() []int64 {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT id, position FROM outbound_routes ORDER BY position`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []int64
		for i := 1; rows.Next(); i++ {
			var id int64
			var pos int
			if err := rows.Scan(&id, &pos); err != nil {
				t.Fatal(err)
			}
			if pos != i {
				t.Fatalf("positions are not 1..n: route %d at %d", id, pos)
			}
			out = append(out, id)
		}
		return out
	}
	rev := func() int64 {
		r, err := st.ConfigRevision(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	// A full reversal swaps every pair of positions.
	rev0 := slices.Clone(ids)
	slices.Reverse(rev0)
	if err := st.ReorderRoutes(ctx, "test", store.Outbound, rev0, nil); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if got := order(); !slices.Equal(got, rev0) {
		t.Fatalf("order = %v, want %v", got, rev0)
	}

	// Rejected permutations and a failing check leave everything as it was.
	r0 := rev()
	// failAfter passes the baseline check and fails the one after the
	// change, so the error counts as introduced by the reorder.
	calls := 0
	failAfter := func(routing.Config) []routing.FieldError {
		if calls++; calls%2 == 0 {
			return []routing.FieldError{{Path: "outbound", Message: "rejected by test"}}
		}
		return nil
	}
	for name, tc := range map[string]struct {
		ids   []int64
		check store.Check
	}{
		"missing":   {ids[:4], nil},
		"duplicate": {[]int64{ids[0], ids[0], ids[1], ids[2], ids[3]}, nil},
		"unknown":   {[]int64{ids[0], ids[1], ids[2], ids[3], 999999}, nil},
		"empty":     {[]int64{}, nil},
		"check":     {ids, failAfter},
	} {
		err := st.ReorderRoutes(ctx, "test", store.Outbound, tc.ids, tc.check)
		var v *store.ValidationError
		if !errors.As(err, &v) {
			t.Fatalf("%s: err = %v, want a ValidationError", name, err)
		}
		if got := order(); !slices.Equal(got, rev0) {
			t.Fatalf("%s: order changed to %v", name, got)
		}
	}
	if rev() != r0 {
		t.Fatal("rejected reorders bumped the revision")
	}

	// Concurrent reorders serialise; the result is exactly one of them.
	perms := [][]int64{}
	for i := range 8 {
		p := slices.Clone(ids)
		for j := range p {
			k := (j*3 + i) % len(p)
			p[j], p[k] = p[k], p[j]
		}
		perms = append(perms, p)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(perms))
	for i, p := range perms {
		wg.Go(func() { errs[i] = st.ReorderRoutes(ctx, "test", store.Outbound, p, nil) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent reorder %d: %v", i, err)
		}
	}
	final := order()
	if !slices.ContainsFunc(perms, func(p []int64) bool { return slices.Equal(p, final) }) {
		t.Fatalf("final order %v is none of the submitted permutations", final)
	}
	if got := rev(); got != r0+int64(len(perms)) {
		t.Fatalf("revision %d after %d reorders from %d", got, len(perms), r0)
	}
}

// TestConfigLockSerialisesChanges fails if two configuration changes that
// are each valid alone, but not together, can both commit. Creating an
// inbound route to extension 101 is held inside its transaction after its
// whole-configuration check passed; meanwhile extension 101 is deleted.
// With the advisory lock the delete waits, then sees the route and is
// refused; without it, the delete commits and the route rings nothing.
func TestConfigLockSerialisesChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, st, _ := p2Store(t, ctx)
	compile := func(cfg routing.Config) []routing.FieldError {
		_, errs := routing.Compile(cfg)
		return errs
	}
	ext, err := st.CreateExtension(ctx, "test", "101", "Desk", "", compile)
	if err != nil {
		t.Fatal(err)
	}
	rev0, err := st.ConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	checked, release := make(chan struct{}), make(chan struct{})
	var calls int
	holding := func(cfg routing.Config) []routing.FieldError {
		errs := compile(cfg)
		if calls++; calls == 2 { // the check after the change; the first is the baseline
			close(checked)
			<-release
		}
		return errs
	}
	createErr := make(chan error, 1)
	go func() {
		_, err := st.CreateInboundRoute(ctx, "test", store.InboundRoute{Name: "Main", DIDKind: "any",
			DestinationKind: "extension", Destination: "101", Enabled: true}, holding)
		createErr <- err
	}()
	<-checked
	deleteErr := make(chan error, 1)
	go func() { deleteErr <- st.DeleteExtension(ctx, "test", ext.ID, compile) }()
	select {
	case err := <-deleteErr:
		close(release)
		t.Fatalf("the delete finished (%v) while another configuration change held the lock", err)
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	if err := <-createErr; err != nil {
		t.Fatalf("create inbound route: %v", err)
	}
	if err := <-deleteErr; !errors.Is(err, store.ErrInUse) {
		t.Fatalf("delete after the route committed = %v, want ErrInUse", err)
	}
	var routes, exts int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM inbound_routes), (SELECT count(*) FROM extensions)`).Scan(&routes, &exts); err != nil {
		t.Fatal(err)
	}
	if routes != 1 || exts != 1 {
		t.Fatalf("after both changes: %d routes, %d extensions; want the route and its extension", routes, exts)
	}
	if rev, err := st.ConfigRevision(ctx); err != nil || rev != rev0+1 {
		t.Fatalf("revision %d -> %d (%v), want exactly one bump", rev0, rev, err)
	}
}
