package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/flow"
)

// edgeFixtures is a configuration with one object of every kind a "call goes
// to" field may name, plus the objects whose fields the test sets.
type edgeFixtures struct {
	e  *env
	c  *client
	id map[string]string // "subj-rg" etc. -> id
	// schedule is the schedule the time conditions use.
	schedule string
	greeting int64
}

// edgeTargets is the stored target string of the fixture for each stored
// destination kind.
var edgeTargets = map[string]string{
	"extension": "101", "voicemail": "101", "ring_group": "sales", "ivr": "dest-ivr", "queue": "dest-queue",
	"announcement": "greet", "paging": "*88", "conference": "800", "time_condition": "dest-tc",
	"external": "+97142000001", "sip_uri": "sip:agent@ai.example",
	// The dial-by-name directory names no stored object: its target is empty.
	"directory": "",
}

func newEdgeFixtures(t *testing.T) *edgeFixtures {
	t.Helper()
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	f := &edgeFixtures{e: e, c: c, id: map[string]string{}}
	ann, err := e.st.CreateAnnouncement(context.Background(), "test", "greet", "ann/greet.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.greeting = ann.ID
	create := func(key, path string, body map[string]any) {
		t.Helper()
		f.id[key] = fmt.Sprint(c.must(http.StatusCreated, "POST", path, body).json(t)["id"])
	}
	member := []any{btMember(e, e.ext101, 1)}
	create("sales", "/api/v1/ring-groups", map[string]any{"name": "sales", "members": member})
	create("subj-rg", "/api/v1/ring-groups", map[string]any{"name": "subj-rg", "members": member})
	for _, n := range []string{"dest-ivr", "subj-ivr"} {
		create(n, "/api/v1/ivr-menus", map[string]any{"name": n, "greetingId": ann.ID})
	}
	for _, n := range []string{"dest-queue", "subj-queue"} {
		create(n, "/api/v1/queues", map[string]any{"name": n})
	}
	create("schedule", "/api/v1/schedules", map[string]any{"name": "office"})
	f.schedule = f.id["schedule"]
	for _, n := range []string{"dest-tc", "subj-tc"} {
		create(n, "/api/v1/time-conditions", map[string]any{"name": n, "scheduleId": json_(f.schedule)})
	}
	create("paging", "/api/v1/paging-groups", map[string]any{"name": "all", "code": "*88", "members": []any{e.ext101["id"]}})
	create("conference", "/api/v1/conferences", map[string]any{"name": "room", "number": "800"})
	create("in1", "/api/v1/routes/inbound", map[string]any{"name": "in1", "destinationKind": "extension", "destination": "101"})
	return f
}

// json_ passes an id string as a JSON number.
func json_(id string) json.Number { return json.Number(id) }

func (f *edgeFixtures) apply(t *testing.T, ops ...map[string]any) {
	t.Helper()
	f.c.must(http.StatusOK, "POST", "/api/v1/config/batch", btReq(btRevision(t, f.c), false, ops...))
}

type edgeView struct {
	Source, Target struct{ Node, Port string }
	Owner          struct{ Node, Field string }
}

// edgesFrom returns the edges that leave port (or port:<key>) of a node.
func (f *edgeFixtures) edgesFrom(t *testing.T, node, port string) []edgeView {
	t.Helper()
	var g struct {
		Edges []edgeView `json:"edges"`
	}
	if err := json.Unmarshal(f.c.must(http.StatusOK, "GET", "/api/v1/flow/graph", nil).body, &g); err != nil {
		t.Fatal(err)
	}
	var out []edgeView
	for _, e := range g.Edges {
		if e.Source.Node == node && (e.Source.Port == port || strings.HasPrefix(e.Source.Port, port+":")) {
			out = append(out, e)
		}
	}
	return out
}

// edgeOwner describes one stored "call goes to" field.
type edgeOwner struct {
	node  string // catalogue kind of the object that owns the field
	port  string // catalogue output port
	field string // owner field the graph reports
	// subject is the fixture key of the object whose field is set.
	subject string
	// set makes the batch operation that points the field at (kind, target).
	set func(f *edgeFixtures, kind, target string) map[string]any
	// clear makes the operation that removes the edge, or nil when the field
	// always names something (an inbound route).
	clear func(f *edgeFixtures) map[string]any
	// keyed ports (an IVR key) are drawn from "<port>:<digit>".
	keyed bool
}

func dest(kind, target string) map[string]any { return map[string]any{"kind": kind, "target": target} }

func edgeOwners() []edgeOwner {
	var owners []edgeOwner
	owners = append(owners,
		edgeOwner{node: flow.KindInboundRoute, port: "destination", field: "destinationKind+destination", subject: "in1",
			set: func(f *edgeFixtures, kind, target string) map[string]any {
				return btOp("", "updateInboundRoute", map[string]string{"id": f.id["in1"]}, map[string]any{"destinationKind": kind, "destination": target})
			}},
		edgeOwner{node: flow.KindRingGroup, port: "noAnswer", field: "failureKind+failureTarget", subject: "subj-rg",
			set: func(f *edgeFixtures, kind, target string) map[string]any {
				return btOp("", "updateRingGroup", map[string]string{"id": f.id["subj-rg"]}, map[string]any{"failureKind": kind, "failureTarget": target})
			},
			clear: func(f *edgeFixtures) map[string]any {
				return btOp("", "updateRingGroup", map[string]string{"id": f.id["subj-rg"]}, map[string]any{"failureKind": "none", "failureTarget": ""})
			}},
		edgeOwner{node: flow.KindIVR, port: "key", field: "options", subject: "subj-ivr", keyed: true,
			set: func(f *edgeFixtures, kind, target string) map[string]any {
				return ivrPut(f, map[string]any{"options": []any{map[string]any{"digit": "1", "kind": kind, "target": target}}})
			},
			clear: func(f *edgeFixtures) map[string]any { return ivrPut(f, map[string]any{"options": []any{}}) }},
	)
	for _, p := range []string{"timeout", "invalid"} {
		owners = append(owners, edgeOwner{node: flow.KindIVR, port: p, field: p, subject: "subj-ivr",
			set: func(f *edgeFixtures, kind, target string) map[string]any {
				return ivrPut(f, map[string]any{p: dest(kind, target)})
			},
			clear: func(f *edgeFixtures) map[string]any { return ivrPut(f, map[string]any{p: dest("hangup", "")}) }})
	}
	for _, p := range []string{"timeout", "full", "noAgents"} {
		owners = append(owners, edgeOwner{node: flow.KindQueue, port: p, field: p, subject: "subj-queue",
			set: func(f *edgeFixtures, kind, target string) map[string]any {
				return queuePut(f, map[string]any{p: dest(kind, target)})
			},
			clear: func(f *edgeFixtures) map[string]any { return queuePut(f, map[string]any{p: dest("hangup", "")}) }})
	}
	for _, p := range []string{"open", "closed", "holiday"} {
		owners = append(owners, edgeOwner{node: flow.KindTimeCond, port: p, field: p, subject: "subj-tc",
			set: func(f *edgeFixtures, kind, target string) map[string]any {
				return tcPut(f, map[string]any{p: dest(kind, target)})
			},
			clear: func(f *edgeFixtures) map[string]any {
				if p == "holiday" {
					return tcPut(f, map[string]any{p: nil})
				}
				return tcPut(f, map[string]any{p: dest("hangup", "")})
			}})
	}
	return owners
}

func ivrPut(f *edgeFixtures, fields map[string]any) map[string]any {
	body := map[string]any{"name": "subj-ivr", "greetingId": f.greeting}
	for k, v := range fields {
		body[k] = v
	}
	return btOp("", "putIVRMenu", map[string]string{"id": f.id["subj-ivr"]}, body)
}

func queuePut(f *edgeFixtures, fields map[string]any) map[string]any {
	body := map[string]any{"name": "subj-queue"}
	for k, v := range fields {
		body[k] = v
	}
	return btOp("", "putQueue", map[string]string{"id": f.id["subj-queue"]}, body)
}

func tcPut(f *edgeFixtures, fields map[string]any) map[string]any {
	body := map[string]any{"name": "subj-tc", "scheduleId": json_(f.schedule)}
	for k, v := range fields {
		body[k] = v
	}
	return btOp("", "putTimeCondition", map[string]string{"id": f.id["subj-tc"]}, body)
}

// TestEdgeRoundTrip fails if, for any connection the catalogue offers from a
// stored "call goes to" field, applying the owner's update through a batch
// does not draw that edge (from the right port, to the input the catalogue
// names, with the field as its owner) and nothing else, or clearing the field
// does not remove it. A catalogue connection no fixture covers fails too, so
// a new one is tested when it is added.
func TestEdgeRoundTrip(t *testing.T) {
	f := newEdgeFixtures(t)
	cat := flow.Get()
	// A stored kind by the input it reaches.
	storedAt := map[flow.Reach]string{}
	for kind, reach := range flow.StoredKinds {
		storedAt[reach] = kind
	}
	// Connections that have no fixture, with the reason.
	skipped := map[string]string{
		"internal": "the route's own DID names the target; the golden graph covers it",
	}
	covered := 0
	for _, o := range edgeOwners() {
		nt, ok := cat.Node(o.node)
		if !ok {
			t.Fatalf("no catalogue node %s", o.node)
		}
		var port flow.Port
		for _, p := range nt.Ports {
			if p.ID == o.port {
				port = p
			}
		}
		if port.ID == "" {
			t.Fatalf("%s has no port %s", o.node, o.port)
		}
		if port.Field != o.field {
			t.Errorf("%s.%s: the catalogue says the owner field is %q, the test %q", o.node, o.port, port.Field, o.field)
		}
		subject := o.node + ":" + f.id[o.subject]
		for _, tgt := range port.Targets {
			kind, ok := storedAt[flow.Reach{Node: tgt.Kind, Port: tgt.Port}]
			if !ok {
				t.Errorf("%s.%s offers %s.%s, which no stored kind reaches", o.node, o.port, tgt.Kind, tgt.Port)
				continue
			}
			if _, why := skipped[kind]; why {
				continue
			}
			target, ok := edgeTargets[kind]
			if !ok {
				t.Errorf("%s.%s offers kind %s and the test has no fixture for it", o.node, o.port, kind)
				continue
			}
			name := fmt.Sprintf("%s.%s->%s", o.node, o.port, kind)
			f.apply(t, o.set(f, kind, target))
			got := f.edgesFrom(t, subject, o.port)
			if len(got) != 1 {
				t.Errorf("%s: %d edges from %s after the update, want 1: %+v", name, len(got), subject, got)
				continue
			}
			e := got[0]
			if !strings.HasPrefix(e.Target.Node, tgt.Kind+":") || e.Target.Port != tgt.Port || e.Owner.Node != subject || e.Owner.Field != o.field {
				t.Errorf("%s: edge = %+v, want to %s.%s owned by %s.%s", name, e, tgt.Kind, tgt.Port, subject, o.field)
			}
			if o.keyed && e.Source.Port != o.port+":1" {
				t.Errorf("%s: source port %s, want %s:1", name, e.Source.Port, o.port)
			}
			covered++
		}
		if o.clear != nil {
			f.apply(t, o.clear(f))
			if got := f.edgesFrom(t, subject, o.port); len(got) != 0 {
				t.Errorf("%s.%s: clearing the field left %+v", o.node, o.port, got)
			}
		}
	}
	if covered < 50 {
		t.Errorf("only %d connections were exercised", covered)
	}
}

// edgesInto returns the nodes with an edge into a port of node.
func (f *edgeFixtures) edgesInto(t *testing.T, node, port string) []string {
	t.Helper()
	var g struct {
		Edges []edgeView `json:"edges"`
	}
	if err := json.Unmarshal(f.c.must(http.StatusOK, "GET", "/api/v1/flow/graph", nil).body, &g); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range g.Edges {
		if e.Target.Node == node && e.Target.Port == port {
			if e.Source.Port != "member" || e.Owner.Node != node || e.Owner.Field != port {
				t.Errorf("edge %+v into %s.%s: want a member edge owned by the group's %s", e, node, port, port)
			}
			out = append(out, e.Source.Node)
		}
	}
	return out
}

// TestEdgeRoundTripMembers is TestEdgeRoundTrip for the member ports: the
// extensions listed in a group's members (and a paging group's callers) are
// edges from those extensions, and replacing the list through a batch moves
// them.
func TestEdgeRoundTripMembers(t *testing.T) {
	f := newEdgeFixtures(t)
	e101, e102 := fmt.Sprint(f.e.ext101["id"]), fmt.Sprint(f.e.ext102["id"])
	f.id["subj-pickup"] = fmt.Sprint(f.c.must(http.StatusCreated, "POST", "/api/v1/pickup-groups",
		map[string]any{"name": "subj-pickup", "members": []any{json_(e101)}}).json(t)["id"])
	cases := []struct {
		node, port, subject string
		set                 func(ext string) map[string]any
	}{
		{flow.KindRingGroup, "members", "subj-rg", func(ext string) map[string]any {
			return btOp("", "updateRingGroup", map[string]string{"id": f.id["subj-rg"]},
				map[string]any{"members": []any{map[string]any{"extensionId": json_(ext), "position": 1}}})
		}},
		{flow.KindQueue, "members", "subj-queue", func(ext string) map[string]any {
			return queuePut(f, map[string]any{"members": []any{map[string]any{"extensionId": json_(ext)}}})
		}},
		{flow.KindPickupGroup, "members", "subj-pickup", func(ext string) map[string]any {
			return btOp("", "putPickupGroup", map[string]string{"id": f.id["subj-pickup"]},
				map[string]any{"name": "subj-pickup", "members": []any{json_(ext)}})
		}},
		{flow.KindPagingGroup, "members", "paging", func(ext string) map[string]any {
			return btOp("", "putPagingGroup", map[string]string{"id": f.id["paging"]},
				map[string]any{"name": "all", "code": "*88", "members": []any{json_(ext)}})
		}},
		{flow.KindPagingGroup, "callers", "paging", func(ext string) map[string]any {
			return btOp("", "putPagingGroup", map[string]string{"id": f.id["paging"]},
				map[string]any{"name": "all", "code": "*88", "members": []any{json_(e101)}, "callers": []any{json_(ext)}})
		}},
	}
	for _, c := range cases {
		node := c.node + ":" + f.id[c.subject]
		for _, ext := range []string{e102, e101} {
			f.apply(t, c.set(ext))
			got := f.edgesInto(t, node, c.port)
			want := "extension:" + ext
			if len(got) != 1 || got[0] != want {
				t.Errorf("%s.%s after setting extension %s: edges from %v, want [%s]", c.node, c.port, ext, got, want)
			}
		}
	}
}
