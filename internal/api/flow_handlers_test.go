package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/routing"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/flow"
	"github.com/azrtydxb/hello/internal/phonebook"
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
	// The lists of the merged features: the handler must read each one.
	ivrs     []store.IVRMenu
	queues   []store.Queue
	channels []phonebook.Channel
	lots     []store.ParkLot
	pickups  []store.PickupGroup
	tcs      []store.TimeCondition
	scheds   []store.Schedule
	cals     []store.Calendar
	mohs     []store.MOHClass
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
func (f *flowFake) ListIVRMenus(context.Context) ([]store.IVRMenu, error) { return f.ivrs, nil }
func (f *flowFake) ListQueues(context.Context) ([]store.Queue, error)     { return f.queues, nil }
func (f *flowFake) ListMulticastChannels(context.Context) ([]phonebook.Channel, error) {
	return f.channels, nil
}
func (f *flowFake) ListParkLots(context.Context) ([]store.ParkLot, error) { return f.lots, nil }
func (f *flowFake) ListPickupGroups(context.Context) ([]store.PickupGroup, error) {
	return f.pickups, nil
}
func (f *flowFake) ListTimeConditions(context.Context) ([]store.TimeCondition, error) {
	return f.tcs, nil
}
func (f *flowFake) ListSchedules(context.Context) ([]store.Schedule, error)       { return f.scheds, nil }
func (f *flowFake) ListCalendars(context.Context) ([]store.Calendar, error)       { return f.cals, nil }
func (f *flowFake) ListMOHClasses(context.Context) ([]store.MOHClass, error)      { return f.mohs, nil }
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

// TestFlowGraphMergedFeatures fails if the graph handler leaves out the
// objects of a merged feature: every list the builder takes must be read.
func TestFlowGraphMergedFeatures(t *testing.T) {
	f := &flowFake{rev: 2,
		exts:     []store.Extension{{ID: 1, Number: "100", Name: "Ann"}},
		ivrs:     []store.IVRMenu{{ID: 1, Name: "menu", Timeout: store.IVRDestination{Kind: "hangup"}, Invalid: store.IVRDestination{Kind: "hangup"}}},
		queues:   []store.Queue{{ID: 1, Name: "q"}},
		channels: []phonebook.Channel{{ID: 1, Label: "lobby"}},
		lots:     []store.ParkLot{{ID: 1, Name: "lot"}},
		pickups:  []store.PickupGroup{{ID: 1, Name: "front"}},
		tcs:      []store.TimeCondition{{ID: 1, Name: "hours"}},
		scheds:   []store.Schedule{{ID: 1, Name: "office"}},
		cals:     []store.Calendar{{ID: 1, Name: "holidays"}},
		mohs:     []store.MOHClass{{ID: 1, Name: "jazz"}},
	}
	rec := flowGet(t, f, func(s *server) http.HandlerFunc { return s.getFlowGraph }, "/api/v1/flow/graph")
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var g flow.Graph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, n := range g.Nodes {
		have[n.ID] = true
	}
	for _, id := range []string{"ivr:1", "queue:1", "multicastChannel:1", "parkLot:1", "pickupGroup:1", "timeCondition:1", "schedule:1", "holidayCalendar:1", "mohClass:1"} {
		if !have[id] {
			t.Errorf("node %s is not in the graph", id)
		}
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

// traceFake adds the routing configuration to the flow fake.
type traceFake struct {
	flowFake
	inbound []store.InboundRoute
	cfg     routing.Config
}

func (f *traceFake) ListTrunks(context.Context) ([]store.Trunk, error) {
	return []store.Trunk{{ID: 1, Name: "carrier", Mode: "ip", Enabled: true}}, nil
}
func (f *traceFake) ListInboundRoutes(context.Context) ([]store.InboundRoute, error) {
	return f.inbound, nil
}
func (f *traceFake) ListFeatureCodes(context.Context) ([]store.FeatureCode, error) {
	return []store.FeatureCode{{Code: "*97", Action: "voicemail"}, {Code: "*8", Action: "park_retrieve"}}, nil
}
func (f *traceFake) RoutingConfig(context.Context) (store.RoutingSnapshot, error) {
	return store.RoutingSnapshot{Config: f.cfg}, nil
}

func traceGet(t *testing.T, f *traceFake, url string, opts ...func(*Config)) (int, flowTraceBody) {
	t.Helper()
	cfg := Config{Store: f, Router: engineRouter{}, Log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(&cfg)
	}
	s := &server{cfg}
	rec := httptest.NewRecorder()
	s.getFlowTrace(rec, httptest.NewRequest("GET", url, nil))
	var out flowTraceBody
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, out
}

type flowTraceBody struct {
	Outcome string           `json:"outcome"`
	Route   string           `json:"route"`
	Reason  string           `json:"reason"`
	Steps   []flow.TraceStep `json:"steps"`
}

func (b flowTraceBody) path() string {
	var p []string
	for _, s := range b.Steps {
		p = append(p, s.Node+":"+s.Port)
	}
	return strings.Join(p, " > ")
}

// TestTrace covers the trace endpoint: the routing decision becomes the
// start of the walk, an inbound schedule closes the route, and a bad
// request is refused.
func TestTrace(t *testing.T) {
	f := &traceFake{
		flowFake: flowFake{rev: 3, exts: []store.Extension{{ID: 1, Number: "100", Name: "Ann", VoicemailEnabled: true}}},
		inbound:  []store.InboundRoute{{ID: 7, Name: "office", DIDKind: "exact", DID: "5551", DestinationKind: "extension", Destination: "100", Enabled: true}},
		cfg: routing.Config{
			Trunks:     []routing.Trunk{{ID: 1, Name: "carrier", Mode: "ip", Enabled: true, Destinations: []routing.Destination{{Host: "192.0.2.1"}}}},
			Extensions: map[string]string{"100": ""},
			Inbound: []routing.InboundRoute{{ID: 7, Name: "office", DIDKind: "exact", DID: "5551", DestinationKind: "extension", Destination: "100", Enabled: true,
				Schedule: &routing.Schedule{TimeZone: "UTC", Windows: []routing.Window{{
					Days:  []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
					Start: "09:00", End: "17:00"}}}}},
		},
	}

	code, tr := traceGet(t, f, "/api/v1/flow/trace?from=trunk:1&number=5551&at=2026-10-26T10:00:00Z")
	if code != 200 || tr.Outcome != "inbound" || tr.Route != "office" {
		t.Fatalf("open hours: %d %+v", code, tr)
	}
	if got := tr.path(); got != "trunk:1:inbound > inboundRoute:7:destination > extension:1:ring > extension:1:voicemail" {
		t.Errorf("open hours path %s", got)
	}

	// Outside the route's schedule no inbound route matches: the trace ends
	// on the trunk with the routing reason.
	code, tr = traceGet(t, f, "/api/v1/flow/trace?from=trunk:1&number=5551&at=2026-10-25T20:00:00Z")
	if code != 200 || tr.Outcome != "reject" || !strings.Contains(tr.Reason, "No inbound route matched") {
		t.Fatalf("closed hours: %d %+v", code, tr)
	}
	if last := tr.Steps[len(tr.Steps)-1]; last.Node != "trunk:1" || !strings.Contains(last.Text, "rejected") {
		t.Errorf("closed hours last step %+v", last)
	}

	// An extension dialling another walks the extension.
	code, tr = traceGet(t, f, "/api/v1/flow/trace?from=100&number=100")
	if code != 200 || tr.Outcome != "internal" || tr.path() != "extension:1: > extension:1:ring > extension:1:voicemail" {
		t.Errorf("internal: %d %s %+v", code, tr.path(), tr)
	}

	// A feature code dialled from an extension is traced before routing.
	code, tr = traceGet(t, f, "/api/v1/flow/trace?from=100&number=*97100")
	if code != 200 || tr.Outcome != "internal" || tr.path() != "extension:1: > extension:1: > extension:1:voicemail" {
		t.Errorf("feature code: %d %s", code, tr.path())
	}
	// The ring step lists the busy and not-registered branches.
	code, tr = traceGet(t, f, "/api/v1/flow/trace?from=100&number=100")
	if code != 200 || len(tr.Steps) < 2 {
		t.Fatalf("ring: %d %+v", code, tr)
	}
	if ring := tr.Steps[1]; len(ring.Branches) != 2 || ring.Branches[0].Label != "If busy" {
		t.Errorf("ring branches: %+v", tr.Steps)
	}

	for _, bad := range []string{"", "?from=abc&number=1", "?from=trunk:1", "?from=trunk:1&number=1&at=noon", "?from=trunk:1&number=1&digits=ab"} {
		if code, _ := traceGet(t, f, "/api/v1/flow/trace"+bad); code == 200 {
			t.Errorf("%q was accepted", bad)
		}
	}
}

type tracePark struct {
	calls []livestate.ParkedCall
	err   error
}

func (f tracePark) ParkList(context.Context) ([]livestate.ParkedCall, error) { return f.calls, f.err }

type traceAgents struct {
	QueueLive
	agents map[string]queue.AgentState
	err    error
}

func (f traceAgents) Agents(context.Context) (map[string]queue.AgentState, error) {
	return f.agents, f.err
}

// TestTraceLiveState reads the live state the trace branches on: the park
// slot and the queue agents, and says so when it cannot.
func TestTraceLiveState(t *testing.T) {
	f := &traceFake{
		flowFake: flowFake{rev: 3,
			exts: []store.Extension{{ID: 1, Number: "100", Name: "Ann"}, {ID: 2, Number: "101", Name: "Bob"}},
			lots: []store.ParkLot{{ID: 1, Name: "lot", SlotStart: 701, SlotEnd: 709, Enabled: true}},
			queues: []store.Queue{{ID: 1, Name: "support", Number: "820", Enabled: true, Strategy: "ring_all", MaxWait: 30,
				Timeout: store.IVRDestination{Kind: "hangup"}, NoAgents: store.IVRDestination{Kind: "hangup"},
				Members: []store.QueueMember{{ExtensionID: 2, Number: "101", Dynamic: true}}}}},
		cfg: routing.Config{Extensions: map[string]string{"100": "", "101": ""}},
	}
	first := func(tr flowTraceBody) string { return tr.Steps[1].Text }

	// Nothing wired: both parts are unknown.
	if _, tr := traceGet(t, f, "/api/v1/flow/trace?from=100&number=*8705"); !strings.Contains(first(tr), "unknown (live state unavailable)") {
		t.Errorf("no live state: %s", first(tr))
	}
	if _, tr := traceGet(t, f, "/api/v1/flow/trace?from=100&number=820"); len(tr.Steps[1].Branches) != 2 {
		t.Errorf("no live agents: %+v", tr.Steps)
	}
	// Unreadable is unknown too.
	broken := func(c *Config) {
		c.Parked = tracePark{err: errors.New("valkey down")}
		c.Queues = traceAgents{err: errors.New("valkey down")}
	}
	if _, tr := traceGet(t, f, "/api/v1/flow/trace?from=100&number=*8705", broken); !strings.Contains(first(tr), "unknown (live state unavailable)") {
		t.Errorf("unreadable: %s", first(tr))
	}
	// Readable: an occupied slot, an empty one, a logged-in agent.
	live := func(c *Config) {
		c.Parked = tracePark{calls: []livestate.ParkedCall{{Slot: "705", ParkedBy: "101"}}}
		c.Queues = traceAgents{agents: map[string]queue.AgentState{"101": {Ext: "101", LoggedIn: true}}}
	}
	if _, tr := traceGet(t, f, "/api/v1/flow/trace?from=100&number=*8705", live); !strings.Contains(first(tr), "parked by extension 101") {
		t.Errorf("occupied: %s", first(tr))
	}
	if _, tr := traceGet(t, f, "/api/v1/flow/trace?from=100&number=*8706", live); !strings.Contains(first(tr), "slot is empty") {
		t.Errorf("empty: %s", first(tr))
	}
	if _, tr := traceGet(t, f, "/api/v1/flow/trace?from=100&number=820", live); len(tr.Steps[1].Branches) != 0 || !strings.Contains(tr.Steps[1].Text, "101 logged in") {
		t.Errorf("agents: %+v", tr.Steps[1])
	}
	// A logged-out agent leaves nobody: the no-agents exit.
	out := func(c *Config) {
		c.Parked = tracePark{}
		c.Queues = traceAgents{agents: map[string]queue.AgentState{}}
	}
	if _, tr := traceGet(t, f, "/api/v1/flow/trace?from=100&number=820", out); !strings.Contains(tr.Steps[1].Text, "101 logged out") {
		t.Errorf("logged out: %+v", tr.Steps[1])
	}
}
