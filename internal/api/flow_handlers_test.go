package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/azrtydxb/hello/internal/flow"
	"github.com/azrtydxb/hello/internal/store"
)

// flowFake serves the flow handlers from memory; any other Store method
// panics on the nil embedded interface, which proves the handlers read
// nothing else.
type flowFake struct {
	Store
	rev   int64
	bump  bool // advance the revision on every read (a busy configuration)
	exts  []store.Extension
	rings []store.RingGroup
}

func (f *flowFake) ConfigRevision(context.Context) (int64, error) {
	if f.bump {
		f.rev++
	}
	return f.rev, nil
}
func (f *flowFake) ListTrunks(context.Context) ([]store.Trunk, error) { return nil, nil }
func (f *flowFake) ListInboundRoutes(context.Context) ([]store.InboundRoute, error) {
	return []store.InboundRoute{{ID: 1, Name: "main", DestinationKind: "extension", Destination: "100"}}, nil
}
func (f *flowFake) ListOutboundRoutes(context.Context) ([]store.OutboundRoute, error) {
	return nil, nil
}
func (f *flowFake) ListExtensions(context.Context) ([]store.Extension, error) { return f.exts, nil }
func (f *flowFake) ListRingGroups(context.Context) ([]store.RingGroup, error) { return f.rings, nil }
func (f *flowFake) ListPagingGroups(context.Context) ([]store.PagingGroup, error) {
	return nil, nil
}
func (f *flowFake) ListAnnouncements(context.Context) ([]store.Announcement, error) {
	return nil, nil
}
func (f *flowFake) ListFeatureCodes(context.Context) ([]store.FeatureCode, error) { return nil, nil }

func flowGet(t *testing.T, f *flowFake, h func(*server) http.HandlerFunc, url string) *httptest.ResponseRecorder {
	t.Helper()
	s := &server{Config{Store: f}}
	rec := httptest.NewRecorder()
	h(s)(rec, httptest.NewRequest("GET", url, nil))
	return rec
}

func TestFlowGraphHandler(t *testing.T) {
	f := &flowFake{rev: 4, exts: []store.Extension{{ID: 1, Number: "100", Name: "Ann"}}}
	get := func(s *server) http.HandlerFunc { return s.getFlowGraph }

	rec := flowGet(t, f, get, "/api/v1/flow/graph")
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var g flow.Graph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if g.ConfigRevision != 4 || len(g.Edges) == 0 {
		t.Fatalf("graph %+v", g)
	}

	if c := flowGet(t, f, get, "/api/v1/flow/graph?scope=closure:inboundRoute:1").Code; c != 200 {
		t.Errorf("closure status %d", c)
	}
	if c := flowGet(t, f, get, "/api/v1/flow/graph?scope=closure:nope:1").Code; c != 404 {
		t.Errorf("unknown closure status %d", c)
	}
	for _, bad := range []string{"?scope=weird", "?scope=closure:", "?include=all"} {
		if c := flowGet(t, f, get, "/api/v1/flow/graph"+bad).Code; c != 400 {
			t.Errorf("%s status %d", bad, c)
		}
	}
	// A configuration that never stops changing is refused, not served torn.
	f.bump = true
	if c := flowGet(t, f, get, "/api/v1/flow/graph").Code; c != 503 {
		t.Errorf("busy configuration status %d", c)
	}
}

func TestFlowCatalogueAndRevisionHandlers(t *testing.T) {
	f := &flowFake{rev: 9}
	rec := flowGet(t, f, func(s *server) http.HandlerFunc { return s.getFlowCatalogue }, "/api/v1/flow/catalogue")
	var c flow.Catalogue
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil || len(c.Nodes) == 0 || len(c.PortTypes) == 0 {
		t.Fatalf("catalogue %v %s", err, rec.Body)
	}
	rec = flowGet(t, f, func(s *server) http.HandlerFunc { return s.getFlowRevision }, "/api/v1/flow/revision")
	if rec.Body.String() != "{\"configRevision\":9}\n" {
		t.Fatalf("revision %q", rec.Body)
	}
}

// TestFlowGraphStatuses sends the graph's refusals through the real API, so
// the OpenAPI conformance check sees them: a bad scope or include (400), an
// unknown closure node (404) and a whole-configuration graph over the node
// cap (413). It needs the test database.
func TestFlowGraphStatuses(t *testing.T) {
	e := newEnv(t, noLive{})
	c := e.login()
	c.must(http.StatusOK, "GET", "/api/v1/flow/graph", nil)
	c.must(http.StatusBadRequest, "GET", "/api/v1/flow/graph?scope=weird", nil)
	c.must(http.StatusBadRequest, "GET", "/api/v1/flow/graph?include=all", nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/flow/graph?scope=closure:nope:1", nil)
	if _, err := e.db.ExecContext(context.Background(),
		`INSERT INTO extensions (number, name) SELECT g::text, 'bulk' FROM generate_series(100000, $1::int) g`, 100000+maxFlowNodes); err != nil {
		t.Fatal(err)
	}
	c.must(http.StatusRequestEntityTooLarge, "GET", "/api/v1/flow/graph", nil)
}
