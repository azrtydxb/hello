package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// blocklistFake is an in-memory BlocklistStore; Store is nil except for the
// trunk list the CSV scope column reads.
type blocklistFake struct {
	Store
	entries []store.BlocklistEntry
	next    int64
	trunks  []store.Trunk
}

func (f *blocklistFake) ListTrunks(context.Context) ([]store.Trunk, error) { return f.trunks, nil }

func (f *blocklistFake) ListBlocklist(_ context.Context, fl store.BlocklistFilter) ([]store.BlocklistEntry, string, int, error) {
	out := []store.BlocklistEntry{}
	for _, e := range f.entries {
		if e.ID > fl.After && (fl.Q == "" || strings.Contains(e.Pattern, fl.Q)) && (fl.Action == "" || e.Action == fl.Action) {
			out = append(out, e)
		}
	}
	return out, "", len(out), nil
}

func (f *blocklistFake) GetBlocklistEntry(_ context.Context, id int64) (store.BlocklistEntry, error) {
	for _, e := range f.entries {
		if e.ID == id {
			return e, nil
		}
	}
	return store.BlocklistEntry{}, store.ErrNotFound
}

func (f *blocklistFake) find(pattern, match string, trunk *int64) int {
	for i, e := range f.entries {
		if e.Pattern == pattern && e.Match == match && (e.TrunkID == nil) == (trunk == nil) && (trunk == nil || *e.TrunkID == *trunk) {
			return i
		}
	}
	return -1
}

func (f *blocklistFake) CreateBlocklistEntry(_ context.Context, _ string, in store.BlocklistInput) (store.BlocklistEntry, error) {
	if f.find(in.Pattern, in.Match, in.TrunkID) >= 0 {
		return store.BlocklistEntry{}, &store.ValidationError{Fields: []routing.FieldError{{Path: "pattern", Message: "is already on the blocklist for this scope"}}}
	}
	f.next++
	e := store.BlocklistEntry{ID: f.next, Pattern: in.Pattern, Match: in.Match, Action: in.Action, TrunkID: in.TrunkID,
		Reason: in.Reason, AnnouncementID: in.AnnouncementID, Enabled: in.Enabled, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.entries = append(f.entries, e)
	return e, nil
}

func (f *blocklistFake) UpdateBlocklistEntry(_ context.Context, _ string, id int64, apply func(store.BlocklistInput) (store.BlocklistInput, error)) (store.BlocklistEntry, error) {
	for i, e := range f.entries {
		if e.ID != id {
			continue
		}
		in, err := apply(e.Input())
		if err != nil {
			return e, err
		}
		f.entries[i] = store.BlocklistEntry{ID: id, Pattern: in.Pattern, Match: in.Match, Action: in.Action, TrunkID: in.TrunkID,
			Reason: in.Reason, AnnouncementID: in.AnnouncementID, Enabled: in.Enabled}
		return f.entries[i], nil
	}
	return store.BlocklistEntry{}, store.ErrNotFound
}

func (f *blocklistFake) DeleteBlocklistEntry(_ context.Context, _ string, id int64) error {
	for i, e := range f.entries {
		if e.ID == id {
			f.entries = append(f.entries[:i], f.entries[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *blocklistFake) ImportBlocklist(_ context.Context, actor string, rows []store.BlocklistInput) (store.BlocklistImportResult, error) {
	var res store.BlocklistImportResult
	for _, in := range rows {
		if i := f.find(in.Pattern, in.Match, in.TrunkID); i >= 0 {
			f.entries[i].Action, f.entries[i].Reason = in.Action, in.Reason
			res.Updated++
			continue
		}
		_, _ = f.CreateBlocklistEntry(context.Background(), actor, in)
		res.Created++
	}
	return res, nil
}

func blocklistCall(t *testing.T, s *server, role auth.Role, h http.HandlerFunc, method, target, body string, path ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req = req.WithContext(auth.WithActor(req.Context(), auth.Actor{UserID: 1, Username: "u", Role: role}))
	if len(path) > 0 {
		req.SetPathValue("id", path[0])
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func blocklistUpload(t *testing.T, s *server, target, content string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="b.csv"`)
	part, _ := mw.CreatePart(h)
	_, _ = part.Write([]byte(content))
	_ = mw.Close()
	req := httptest.NewRequest("POST", target, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(auth.WithActor(req.Context(), auth.Actor{UserID: 1, Username: "u", Role: auth.RoleOperator}))
	rec := httptest.NewRecorder()
	s.importBlocklist(rec, req)
	return rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return m
}

// TestBlocklistImport fails if a dry run writes anything or misses a bad
// row, an import with a bad row writes any row, a good import does not
// normalise numbers or update a repeated one, or the scope column does not
// name a trunk.
func TestBlocklistImport(t *testing.T) {
	f := &blocklistFake{trunks: []store.Trunk{{ID: 7, Name: "carrier-a"}}}
	s := &server{Config{Blocklist: f, Store: f, Dialing: phonebook.Dialing{IntlPrefix: "00", CountryCode: "41"}}}
	good := "number,action,match,scope,reason\n" +
		"022 555 00 00,block,,global,spam\n" +
		"+44*,block,,carrier-a,uk\n" +
		"0041 79 555 11 11,allow,exact,,vip\n"
	rec := blocklistUpload(t, s, "/api/v1/blocklist/import?dry_run=true", good)
	if rec.Code != 200 {
		t.Fatalf("dry run = %d %s", rec.Code, rec.Body)
	}
	if m := decodeMap(t, rec); m["ok"] != true || m["dryRun"] != true || m["rows"] != float64(3) || m["created"] != float64(0) {
		t.Fatalf("dry run result = %v", m)
	}
	if len(f.entries) != 0 {
		t.Fatalf("a dry run wrote %d entries", len(f.entries))
	}

	bad := "number,action,match,scope\n+41225550000,maybe,,\n12,block,,\n+4122555,block,,nosuchtrunk\n+41225550001,block,,\n"
	rec = blocklistUpload(t, s, "/api/v1/blocklist/import?dry_run=true", bad)
	m := decodeMap(t, rec)
	errs := m["errors"].([]any)
	if rec.Code != 200 || m["ok"] == true || len(errs) != 3 {
		t.Fatalf("bad dry run = %d %v", rec.Code, m)
	}
	rows := map[float64]string{}
	for _, e := range errs {
		em := e.(map[string]any)
		rows[em["row"].(float64)] = em["column"].(string)
	}
	if rows[2] != "action" || rows[3] != "number" || rows[4] != "scope" {
		t.Fatalf("error rows = %v", rows)
	}
	if rec = blocklistUpload(t, s, "/api/v1/blocklist/import", bad); rec.Code != 400 || len(f.entries) != 0 {
		t.Fatalf("a failing import = %d, entries %d; want 400 and nothing written", rec.Code, len(f.entries))
	}
	if rec = blocklistUpload(t, s, "/api/v1/blocklist/import", "action\nblock\n"); rec.Code != 400 || decodeMap(t, rec)["ok"] == true {
		t.Fatalf("a file without a number column was accepted: %d %s", rec.Code, rec.Body)
	}

	if rec = blocklistUpload(t, s, "/api/v1/blocklist/import", good); rec.Code != 200 {
		t.Fatalf("import = %d %s", rec.Code, rec.Body)
	}
	if m := decodeMap(t, rec); m["created"] != float64(3) || m["updated"] != float64(0) {
		t.Fatalf("import result = %v", m)
	}
	got := map[string]store.BlocklistEntry{}
	for _, e := range f.entries {
		got[e.Pattern] = e
	}
	if e := got["+41225550000"]; e.Match != "exact" || e.Action != "block" || e.TrunkID != nil {
		t.Errorf("national number entry = %+v", e)
	}
	if e := got["+44*"]; e.Match != "prefix" || e.TrunkID == nil || *e.TrunkID != 7 {
		t.Errorf("prefix entry = %+v", e)
	}
	if e := got["+41795551111"]; e.Action != "allow" {
		t.Errorf("allow entry = %+v", e)
	}
	// The same numbers again update the rows instead of adding them.
	rec = blocklistUpload(t, s, "/api/v1/blocklist/import", "number,reason\n+41 22 555 00 00,again\n")
	if m := decodeMap(t, rec); m["created"] != float64(0) || m["updated"] != float64(1) || len(f.entries) != 3 {
		t.Fatalf("repeat import = %v, entries %d", m, len(f.entries))
	}
}

// TestBlocklistEntries fails if an entry is stored in anything but its
// normalised form, a bad field is accepted, or the reason note reaches a
// role below admin.
func TestBlocklistEntries(t *testing.T) {
	f := &blocklistFake{}
	s := &server{Config{Blocklist: f, Dialing: phonebook.Dialing{IntlPrefix: "00", CountryCode: "41"}}}
	rec := blocklistCall(t, s, auth.RoleOperator, s.createBlocklistEntry, "POST", "/api/v1/blocklist",
		`{"pattern":"0041 22 555 00 00","reason":"telemarketer"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	m := decodeMap(t, rec)
	if m["pattern"] != "+41225550000" || m["match"] != "exact" || m["action"] != "block" || m["enabled"] != true {
		t.Fatalf("created = %v", m)
	}
	if m["reason"] != "" {
		t.Errorf("an operator saw the reason %q", m["reason"])
	}
	if f.entries[0].Reason != "telemarketer" {
		t.Errorf("the reason was not stored: %q", f.entries[0].Reason)
	}
	rec = blocklistCall(t, s, auth.RoleAdmin, s.listBlocklist, "GET", "/api/v1/blocklist", "")
	if items := decodeMap(t, rec)["items"].([]any); len(items) != 1 || items[0].(map[string]any)["reason"] != "telemarketer" {
		t.Fatalf("admin list = %s", rec.Body)
	}
	rec = blocklistCall(t, s, auth.RoleViewer, s.listBlocklist, "GET", "/api/v1/blocklist", "")
	if items := decodeMap(t, rec)["items"].([]any); items[0].(map[string]any)["reason"] != "" {
		t.Fatalf("viewer list shows the reason: %s", rec.Body)
	}
	for name, body := range map[string]string{
		"duplicate":      `{"pattern":"+41225550000"}`,
		"short number":   `{"pattern":"123"}`,
		"bad match":      `{"pattern":"+41225550000","match":"regex"}`,
		"bad action":     `{"pattern":"+41225550000","action":"drop"}`,
		"missing":        `{}`,
		"long reason":    `{"pattern":"+41225550001","reason":"` + strings.Repeat("x", 121) + `"}`,
		"allow announce": `{"pattern":"+41225550001","action":"allow","announcementId":3}`,
		"unknown field":  `{"pattern":"+41225550001","colour":"red"}`,
	} {
		if rec := blocklistCall(t, s, auth.RoleOperator, s.createBlocklistEntry, "POST", "/api/v1/blocklist", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, rec.Code, rec.Body)
		}
	}
	// A replace re-normalises, and an operator's replace keeps the note.
	id := strconv.FormatInt(f.entries[0].ID, 10)
	rec = blocklistCall(t, s, auth.RoleOperator, s.putBlocklistEntry, "PUT", "/api/v1/blocklist/"+id, `{"pattern":"+44","match":"prefix","action":"allow"}`, id)
	if rec.Code != 200 || f.entries[0].Pattern != "+44*" || f.entries[0].Action != "allow" || f.entries[0].Reason != "telemarketer" {
		t.Fatalf("replace = %d %s, stored %+v", rec.Code, rec.Body, f.entries[0])
	}
	if rec = blocklistCall(t, s, auth.RoleOperator, s.deleteBlocklistEntry, "DELETE", "/api/v1/blocklist/"+id, "", id); rec.Code != http.StatusNoContent || len(f.entries) != 0 {
		t.Fatalf("delete = %d, entries %d", rec.Code, len(f.entries))
	}
	if rec = blocklistCall(t, s, auth.RoleOperator, s.deleteBlocklistEntry, "DELETE", "/api/v1/blocklist/"+id, "", id); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}
	if rec = blocklistCall(t, s, auth.RoleOperator, (&server{}).listBlocklist, "GET", "/api/v1/blocklist", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured = %d, want 503", rec.Code)
	}
}

// TestBlocklistAPI runs the blocklist routes against a real database: every
// documented answer, the scope of an entry, the reason hidden from an
// operator, the import, and the 10,000-entry limit.
func TestBlocklistAPI(t *testing.T) {
	e := newEnv(t, noLive{})
	c := e.login()
	ctx := context.Background()
	tr, err := e.st.CreateTrunk(ctx, "t", store.TrunkInput{Name: "carrier-a", Mode: "ip", RegisterExpires: 3600, OptionsInterval: 30,
		Enabled: true, SourceCIDRs: []string{"203.0.113.0/24"}, InternalDialing: store.Dialing{Mode: "off"},
		Destinations: []routing.Destination{{Host: "203.0.113.10", Port: 5060, Priority: 1, Weight: 1}}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	created := c.must(http.StatusCreated, "POST", "/api/v1/blocklist",
		map[string]any{"pattern": "+41 22 555 00 00", "reason": "spam", "trunkId": tr.ID}).json(t)
	if created["pattern"] != "+41225550000" || created["trunkName"] != "carrier-a" || created["reason"] != "spam" {
		t.Fatalf("created = %v", created)
	}
	id := rowID(created)
	c.must(http.StatusBadRequest, "POST", "/api/v1/blocklist", map[string]any{"pattern": "12"})
	c.must(http.StatusBadRequest, "POST", "/api/v1/blocklist", map[string]any{"pattern": "+4122", "trunkId": 99999})
	c.must(http.StatusBadRequest, "POST", "/api/v1/blocklist", map[string]any{"pattern": "+41225550000", "trunkId": tr.ID})
	c.must(http.StatusCreated, "POST", "/api/v1/blocklist", map[string]any{"pattern": "+44", "match": "prefix"})

	if items := listItems(t, c.must(http.StatusOK, "GET", "/api/v1/blocklist", nil)); len(items) != 2 {
		t.Fatalf("list = %v", items)
	}
	if items := listItems(t, c.must(http.StatusOK, "GET", "/api/v1/blocklist?scope=global", nil)); len(items) != 1 {
		t.Fatalf("global list = %v", items)
	}
	c.must(http.StatusBadRequest, "GET", "/api/v1/blocklist?action=bogus", nil)

	op, _ := e.asRole("ops", auth.RoleOperator)
	for _, it := range listItems(t, op.must(http.StatusOK, "GET", "/api/v1/blocklist", nil)) {
		if it.(map[string]any)["reason"] != "" {
			t.Fatalf("an operator saw a reason: %v", it)
		}
	}
	// An operator's replace keeps the note it cannot see.
	op.must(http.StatusOK, "PUT", "/api/v1/blocklist/"+id, map[string]any{"pattern": "+41225550000", "action": "allow", "trunkId": tr.ID})
	var reason string
	if err := e.db.QueryRowContext(ctx, `SELECT reason FROM call_blocklist WHERE id = $1`, id).Scan(&reason); err != nil || reason != "spam" {
		t.Fatalf("reason after an operator's replace = %q, %v", reason, err)
	}
	c.must(http.StatusBadRequest, "PUT", "/api/v1/blocklist/"+id, map[string]any{"pattern": "abc"})
	c.must(http.StatusNotFound, "PUT", "/api/v1/blocklist/99999", map[string]any{"pattern": "+4122555"})

	// The import: a dry run writes nothing, a bad row writes nothing, a good
	// file adds and updates.
	dry := c.upload("/api/v1/blocklist/import?dry_run=true", "number,action,scope\n+33 1 23 45 67 89,block,carrier-a\n").json(t)
	if dry["ok"] != true {
		t.Fatalf("dry run = %v", dry)
	}
	var n int
	if err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM call_blocklist`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows after a dry run = %d, %v", n, err)
	}
	if r := c.upload("/api/v1/blocklist/import", "number,action\nabc,block\n"); r.code != http.StatusBadRequest {
		t.Fatalf("import of a bad row = %d %s", r.code, r.body)
	}
	got := c.upload("/api/v1/blocklist/import", "number,action,scope,reason\n+33 1 23 45 67 89,block,carrier-a,x\n+4122 555 00 00,allow,carrier-a,again\n").json(t)
	if got["created"] != float64(1) || got["updated"] != float64(1) {
		t.Fatalf("import = %v", got)
	}

	// The limit: fill the table, then one more is refused everywhere.
	if _, err := e.db.ExecContext(ctx, `INSERT INTO call_blocklist (pattern, match, action)
		SELECT '+9' || lpad(g::text, 12, '0'), 'exact', 'block' FROM generate_series(1, $1 - (SELECT count(*) FROM call_blocklist)) g`,
		routing.MaxBlocklistEntries); err != nil {
		t.Fatal(err)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/blocklist", map[string]any{"pattern": "+4177 777 77 77"})
	if r := c.upload("/api/v1/blocklist/import", "number\n+41 77 777 77 78\n"); r.code != http.StatusConflict {
		t.Fatalf("import past the limit = %d %s", r.code, r.body)
	}
	if _, err := e.db.ExecContext(ctx, `DELETE FROM call_blocklist WHERE pattern LIKE '+9%'`); err != nil {
		t.Fatal(err)
	}

	c.must(http.StatusNoContent, "DELETE", "/api/v1/blocklist/"+id, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/blocklist/"+id, nil)
}
