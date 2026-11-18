package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestEdgesRunOutToIn guards how the canvas draws a connection: it leaves
// its source on the right, so the source port must be an output, and enters
// its target on the left, so the target port must be an input. Every edge of
// every golden graph is checked against the catalogue.
func TestEdgesRunOutToIn(t *testing.T) {
	cat := Get()
	files, err := filepath.Glob(filepath.Join("testdata", "*.graph.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden graphs: %v", err)
	}
	edges := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var g Graph
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		kind := map[string]string{}
		for _, n := range g.Nodes {
			kind[n.ID] = n.Kind
		}
		for _, e := range g.Edges {
			edges++
			src, ok := cat.Port(kind[e.Source.Node], e.Source.Port)
			if !ok || src.Direction != Out {
				t.Errorf("%s: edge %s starts at %s.%s, which is not an output", f, e.ID, kind[e.Source.Node], e.Source.Port)
			}
			dst, ok := cat.Port(kind[e.Target.Node], e.Target.Port)
			if !ok || dst.Direction != In {
				t.Errorf("%s: edge %s ends at %s.%s, which is not an input", f, e.ID, kind[e.Target.Node], e.Target.Port)
			}
		}
	}
	if edges == 0 {
		t.Fatal("the golden graphs have no edges")
	}
}
