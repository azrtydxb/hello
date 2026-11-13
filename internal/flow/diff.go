package flow

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
)

// GraphDiff is what a change does to the drawn graph (spec flow-canvas
// S-19): the nodes and edges it adds, the ones it removes, and the nodes
// whose object changed. A batch dry run answers it so a proposal can be
// shown on the canvas as an overlay before anybody applies it.
type GraphDiff struct {
	// Added are the nodes the change creates, as they would be drawn.
	Added []Node `json:"added"`
	// Changed are the nodes whose object the change alters, as they would
	// be after it.
	Changed []Node `json:"changed"`
	// Removed are the ids of the nodes the change deletes.
	Removed []string `json:"removed"`
	// EdgesAdded are the connections the change makes (an edge whose
	// details change is removed and added again).
	EdgesAdded []Edge `json:"edgesAdded"`
	// EdgesRemoved are the ids of the connections the change drops.
	EdgesRemoved []string `json:"edgesRemoved"`
}

// Empty reports whether the change does nothing the canvas draws.
func (d GraphDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Changed) == 0 && len(d.Removed) == 0 &&
		len(d.EdgesAdded) == 0 && len(d.EdgesRemoved) == 0
}

// Diff compares two builds of the graph. A node changed when its version
// (the hash of the stored object) or its label differs; a node without a
// version (a terminal, a missing target) is compared by label and summary.
// The lists are sorted by id, so equal changes give equal diffs.
func Diff(before, after Graph) GraphDiff {
	d := GraphDiff{Added: []Node{}, Changed: []Node{}, Removed: []string{}, EdgesAdded: []Edge{}, EdgesRemoved: []string{}}
	old := make(map[string]Node, len(before.Nodes))
	for _, n := range before.Nodes {
		old[n.ID] = n
	}
	seen := make(map[string]bool, len(after.Nodes))
	for _, n := range after.Nodes {
		seen[n.ID] = true
		o, there := old[n.ID]
		switch {
		case !there:
			d.Added = append(d.Added, n)
		case nodeChanged(o, n):
			d.Changed = append(d.Changed, n)
		}
	}
	for _, n := range before.Nodes {
		if !seen[n.ID] {
			d.Removed = append(d.Removed, n.ID)
		}
	}
	oldEdges := make(map[string]Edge, len(before.Edges))
	for _, e := range before.Edges {
		oldEdges[e.ID] = e
	}
	kept := make(map[string]bool, len(after.Edges))
	for _, e := range after.Edges {
		o, there := oldEdges[e.ID]
		if there && reflect.DeepEqual(canonical(o), canonical(e)) {
			kept[e.ID] = true
			continue
		}
		d.EdgesAdded = append(d.EdgesAdded, e)
	}
	for _, e := range before.Edges {
		if !kept[e.ID] {
			d.EdgesRemoved = append(d.EdgesRemoved, e.ID)
		}
	}
	byID := func(a, b Node) int { return strings.Compare(a.ID, b.ID) }
	slices.SortFunc(d.Added, byID)
	slices.SortFunc(d.Changed, byID)
	slices.Sort(d.Removed)
	slices.SortFunc(d.EdgesAdded, func(a, b Edge) int { return strings.Compare(a.ID, b.ID) })
	slices.Sort(d.EdgesRemoved)
	return d
}

func nodeChanged(a, b Node) bool {
	if a.Label != b.Label || a.Kind != b.Kind {
		return true
	}
	if a.Version != "" || b.Version != "" {
		return a.Version != b.Version
	}
	return !reflect.DeepEqual(canonical(a.Summary), canonical(b.Summary))
}

// canonical is v through JSON, so numbers and nested maps compare by value.
func canonical(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}
