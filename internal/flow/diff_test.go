package flow

import (
	"reflect"
	"testing"
)

// TestGraphDiff (spec flow-canvas S-19) fails if the overlay of a proposal
// misses a node or edge the change adds or removes, reports an unchanged
// node or edge, or treats a changed edge detail as unchanged.
func TestGraphDiff(t *testing.T) {
	route := Node{ID: "inboundRoute:1", Kind: KindInboundRoute, Label: "Main", Version: "a", Summary: map[string]any{}}
	group := Node{ID: "ringGroup:2", Kind: KindRingGroup, Label: "Sales", Version: "b", Summary: map[string]any{}}
	ext := Node{ID: "extension:3", Kind: KindExtension, Label: "100", Version: "c", Summary: map[string]any{}}
	vm := Node{ID: "voicemail:100", Kind: "voicemail", Label: "Voicemail 100", Summary: map[string]any{"box": "100"}}
	toGroup := Edge{ID: "e1", Source: Ref{Node: route.ID, Port: "destination"}, Target: Ref{Node: group.ID, Port: "in"}, Owner: Owner{Node: route.ID, Field: "destination"}}
	member := Edge{ID: "e2", Source: Ref{Node: group.ID, Port: "members"}, Target: Ref{Node: ext.ID, Port: "in"}, Owner: Owner{Node: group.ID, Field: "members"}, Data: map[string]any{"position": 1}}
	before := Graph{Nodes: []Node{route, group, ext, vm}, Edges: []Edge{toGroup, member}}

	t.Run("no change", func(t *testing.T) {
		if d := Diff(before, before); !d.Empty() {
			t.Fatalf("diff of a graph with itself = %+v", d)
		}
	})

	t.Run("a change", func(t *testing.T) {
		ivr := Node{ID: "ivr:9", Kind: KindIVR, Label: "Menu", Version: "d", Summary: map[string]any{}}
		route2 := route
		route2.Version = "a2"
		vm2 := vm
		vm2.Summary = map[string]any{"box": "100", "greeting": "busy"}
		member2 := member
		member2.Data = map[string]any{"position": 2}
		toIVR := Edge{ID: "e3", Source: Ref{Node: route.ID, Port: "destination"}, Target: Ref{Node: ivr.ID, Port: "in"}, Owner: Owner{Node: route.ID, Field: "destination"}}
		after := Graph{Nodes: []Node{ivr, route2, ext, vm2}, Edges: []Edge{member2, toIVR}}
		d := Diff(before, after)
		ids := func(ns []Node) []string {
			out := []string{}
			for _, n := range ns {
				out = append(out, n.ID)
			}
			return out
		}
		edgeIDs := func(es []Edge) []string {
			out := []string{}
			for _, e := range es {
				out = append(out, e.ID)
			}
			return out
		}
		for name, c := range map[string]struct{ got, want []string }{
			"added":        {ids(d.Added), []string{"ivr:9"}},
			"changed":      {ids(d.Changed), []string{"inboundRoute:1", "voicemail:100"}},
			"removed":      {d.Removed, []string{"ringGroup:2"}},
			"edgesAdded":   {edgeIDs(d.EdgesAdded), []string{"e2", "e3"}},
			"edgesRemoved": {d.EdgesRemoved, []string{"e1", "e2"}},
		} {
			if !reflect.DeepEqual(c.got, c.want) {
				t.Errorf("%s = %v, want %v", name, c.got, c.want)
			}
		}
	})
}
