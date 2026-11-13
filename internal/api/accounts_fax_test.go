package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/fax"
	"github.com/azrtydxb/hello/internal/store"
)

// acctFaxStore is the fax table behind the self-service routes, without a
// database: two faxes, one owned by Alice's extension (101) and one by Bob's.
type acctFaxStore struct {
	FaxStore // nil: any other method panics
	f        *acctFake
	rows     []fax.Fax
}

func (s *acctFaxStore) FaxOwnerExtensions(_ context.Context, userID int64) ([]string, error) {
	var out []string
	for _, id := range s.f.links[userID] {
		if e, ok := s.f.exts[id]; ok {
			out = append(out, e.Number)
		}
	}
	return out, nil
}

func (s *acctFaxStore) ListFaxes(_ context.Context, fl fax.ListFilter) ([]fax.Fax, error) {
	var out []fax.Fax
	for _, r := range s.rows {
		if fl.Owners != nil && !slices.Contains(fl.Owners, r.OwnerExtension) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *acctFaxStore) GetFax(_ context.Context, id int64) (fax.Fax, error) {
	for _, r := range s.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return fax.Fax{}, store.ErrNotFound
}

// acctFax is the fax configuration of the account tests' handler.
func acctFax(f *acctFake) FaxConfig {
	objs := &fax.MemObjects{}
	_ = objs.Put(context.Background(), "fax/2026/1.pdf", []byte("%PDF-1.4 alice"))
	_ = objs.Put(context.Background(), "fax/2026/2.pdf", []byte("%PDF-1.4 bob"))
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	return FaxConfig{Objects: objs, Store: &acctFaxStore{f: f, rows: []fax.Fax{
		{ID: 1, Direction: "inbound", Status: "received", MailStatus: "none", OwnerExtension: "101", ObjectKey: "fax/2026/1.pdf", HasPDF: true, CreatedAt: at},
		{ID: 2, Direction: "inbound", Status: "received", MailStatus: "none", OwnerExtension: "102", ObjectKey: "fax/2026/2.pdf", HasPDF: true, CreatedAt: at},
		{ID: 3, Direction: "inbound", Status: "failed", MailStatus: "none", OwnerExtension: "101", CreatedAt: at},
	}}}
}

// TestSelfServiceFaxes fails if a user sees a fax owned by another extension,
// downloads one, or learns that one exists; or if the routes answer a
// credential with no user, a bad page size or a fax with no document wrongly.
func TestSelfServiceFaxes(t *testing.T) {
	_, h, _, _ := newAcctFake(t)

	m := wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/me/faxes", nil), http.StatusOK, "alice's faxes")
	got := nextOf(m)
	if len(got) != 2 || got[0]["ownerExtension"] != "101" || got[1]["ownerExtension"] != "101" {
		t.Fatalf("alice sees %v, want the two faxes of extension 101", got)
	}
	if got := nextOf(wantJSON(t, acctCall(h, "bob", "GET", "/api/v1/me/faxes", nil), http.StatusOK, "bob's faxes")); len(got) != 1 || got[0]["id"] != float64(2) {
		t.Fatalf("bob sees %v, want fax 2 only", got)
	}
	if got := nextOf(wantJSON(t, acctCall(h, "noext", "GET", "/api/v1/me/faxes", nil), http.StatusOK, "a user with no extension")); len(got) != 0 {
		t.Fatalf("a user with no extension sees %v", got)
	}
	for _, q := range []string{"?limit=0", "?cursor=zz", "?direction=up", "?status=nope"} {
		if rec := acctCall(h, "alice", "GET", "/api/v1/me/faxes"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, rec.Code)
		}
	}
	// A credential with no user (a service account) has no extensions.
	if rec := acctCall(h, "svc", "GET", "/api/v1/me/faxes", nil); rec.Code != http.StatusForbidden {
		t.Errorf("a credential with no user = %d, want 403", rec.Code)
	}
	if rec := acctCall(h, "svc", "GET", "/api/v1/me/faxes/1/pdf", nil); rec.Code != http.StatusForbidden {
		t.Errorf("pdf with a credential with no user = %d, want 403", rec.Code)
	}

	// The PDF: her own, never another extension's, never one with no document.
	rec := acctCall(h, "alice", "GET", "/api/v1/me/faxes/1/pdf", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "%PDF-1.4 alice" || rec.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("own pdf = %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	for _, c := range []struct{ who, id string }{{"alice", "2"}, {"bob", "1"}, {"alice", "3"}, {"alice", "99"}, {"alice", "0"}, {"alice", "x"}, {"noext", "1"}} {
		if rec := acctCall(h, c.who, "GET", fmt.Sprintf("/api/v1/me/faxes/%s/pdf", c.id), nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s fax %s = %d %s, want 404", c.who, c.id, rec.Code, rec.Body)
		}
	}
	// A fax that exists and one that does not are indistinguishable.
	a, b := acctCall(h, "alice", "GET", "/api/v1/me/faxes/2/pdf", nil), acctCall(h, "alice", "GET", "/api/v1/me/faxes/99/pdf", nil)
	if a.Body.String() != b.Body.String() {
		t.Errorf("a foreign fax answers %q, a missing one %q", a.Body, b.Body)
	}
}
