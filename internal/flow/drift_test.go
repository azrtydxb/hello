package flow

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

// The guards in this file fail CI when a feature is merged without canvas
// support. They compare the catalogue and the tables in kinds.go with what
// the rest of the code base says: the OpenAPI document, the Go lists the
// API validates against and (in drift_db_test.go) the database CHECK
// constraints. Each checker is a function returning the problems it found,
// so the tests at the bottom can prove it finds them.

// openAPI reads the document hello-control embeds.
func openAPI(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// enums collects every string enum in the document with its schema path.
func enums(v any, path string, out map[string][]string) {
	switch x := v.(type) {
	case map[string]any:
		if list, ok := x["enum"].([]any); ok {
			var vals []string
			for _, e := range list {
				if s, ok := e.(string); ok {
					vals = append(vals, s)
				}
			}
			if len(vals) == len(list) {
				out[path] = vals
			}
		}
		for k, c := range x {
			enums(c, path+"/"+k, out)
		}
	case []any:
		for i, c := range x {
			enums(c, fmt.Sprintf("%s/%d", path, i), out)
		}
	}
}

// destinationValues reports whether a list looks like a list of destination
// kinds: at least three of its values are stored kinds.
func destinationValues(vals []string) bool {
	n := 0
	for _, v := range vals {
		if _, ok := StoredKinds[v]; ok {
			n++
		}
	}
	return n >= 3
}

// featureActionValues reports whether a list is the feature-code actions.
func featureActionValues(vals []string) bool {
	return slices.Contains(vals, "forward_always") && slices.Contains(vals, "dnd_on")
}

// reaches maps stored kind values to the set of "node.port" inputs they lead
// to; kinds that end the call are skipped, unknown ones are reported.
func reaches(vals []string) (set map[string]bool, unknown []string) {
	set = map[string]bool{}
	for _, v := range vals {
		if NoDestination[v] {
			continue
		}
		r, ok := StoredKinds[v]
		if !ok {
			unknown = append(unknown, v)
			continue
		}
		set[r.Node+"."+r.Port] = true
	}
	return set, unknown
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkKindList checks one list of stored destination kinds (a CHECK
// constraint or an OpenAPI enum) against the catalogue: every value is known,
// and some catalogue output connects to exactly the inputs the values reach,
// so a connection the catalogue offers is one the list stores and the other
// way round.
func checkKindList(c Catalogue, where string, vals []string) []string {
	var problems []string
	set, unknown := reaches(vals)
	for _, u := range unknown {
		problems = append(problems, fmt.Sprintf("%s: kind %q has no catalogue node: add it to StoredKinds in kinds.go (and the catalogue and graph builder), or to NoDestination", where, u))
	}
	if len(unknown) > 0 {
		return problems
	}
	want := sortedKeys(set)
	for _, n := range c.Nodes {
		for _, p := range n.Ports {
			if p.Direction != Out || len(p.Targets) == 0 {
				continue
			}
			got := map[string]bool{}
			for _, t := range p.Targets {
				got[t.Kind+"."+t.Port] = true
			}
			if slices.Equal(sortedKeys(got), want) {
				return nil
			}
		}
	}
	return append(problems, fmt.Sprintf("%s: no catalogue output connects to exactly %v: the list and the catalogue disagree", where, want))
}

// checkFeatureActions checks a list of feature-code actions: each is either
// a target action or has a reason for having no edge.
func checkFeatureActions(where string, vals []string) []string {
	var problems []string
	for _, v := range vals {
		_, target := FeatureCodeTargets[v]
		_, none := FeatureCodeNoTarget[v]
		if !target && !none {
			problems = append(problems, fmt.Sprintf("%s: feature code action %q is not classified: add it to FeatureCodeTargets (and draw its edge) or FeatureCodeNoTarget with a reason", where, v))
		}
	}
	return problems
}

// testStoredKinds (a subtest of TestCatalogueDrift) fails if the tables in kinds.go name a node or
// port the catalogue does not have, or a column's output connects to a kind
// nothing stores.
func testStoredKinds(t *testing.T) {
	c := Get()
	for kind, r := range StoredKinds {
		p, ok := c.Port(r.Node, r.Port)
		if !ok || p.Direction != In {
			t.Errorf("stored kind %q leads to %s.%s, which is not a catalogue input", kind, r.Node, r.Port)
		}
	}
	for _, f := range KindFields {
		p, ok := c.Port(f.Node, f.Port)
		if !ok || p.Direction != Out {
			t.Errorf("%s.%s: %s.%s is not a catalogue output", f.Table, f.Constraint, f.Node, f.Port)
			continue
		}
		for _, tg := range p.Targets {
			found := false
			for _, r := range StoredKinds {
				found = found || (r.Node == tg.Kind && r.Port == tg.Port)
			}
			if !found {
				t.Errorf("%s.%s connects to %s.%s, which no stored kind leads to", f.Node, f.Port, tg.Kind, tg.Port)
			}
		}
	}
	for action, kind := range FeatureCodeTargets {
		r, ok := StoredKinds[kind]
		if !ok {
			t.Errorf("feature code action %q names kind %q, which is not a stored kind", action, kind)
			continue
		}
		if okc, _, _ := c.CanConnect(KindFeatureCode, "target", r.Node, r.Port); !okc {
			t.Errorf("feature code action %q leads to %s.%s, which the feature code target cannot connect to", action, r.Node, r.Port)
		}
	}
	for action := range FeatureCodeTargets {
		if _, dup := FeatureCodeNoTarget[action]; dup {
			t.Errorf("feature code action %q is both a target and a no-target action", action)
		}
	}
}

// testGoKindLists (a subtest of TestCatalogueDrift) fails if the Go list the IVR API validates destination
// kinds against drifts from the catalogue.
func testGoKindLists(t *testing.T) {
	if p := checkKindList(Get(), "store.IVRDestinationKinds", store.IVRDestinationKinds); len(p) > 0 {
		t.Error(strings.Join(p, "\n"))
	}
}

// testOpenAPIKindEnums (a subtest of TestCatalogueDrift) fails if an OpenAPI enum of destination kinds lists a
// kind with no catalogue node, or no catalogue output offers exactly its
// connections, or the feature-code action enum has an unclassified action.
func testOpenAPIKindEnums(t *testing.T) {
	all := map[string][]string{}
	enums(openAPI(t), "", all)
	c := Get()
	checked, features := 0, 0
	for path, vals := range all {
		switch {
		case featureActionValues(vals):
			features++
			if p := checkFeatureActions("openapi "+path, vals); len(p) > 0 {
				t.Error(strings.Join(p, "\n"))
			}
		case destinationValues(vals):
			if _, ok := NotDestinationEnums[path]; ok {
				continue
			}
			checked++
			if p := checkKindList(c, "openapi "+path, vals); len(p) > 0 {
				t.Error(strings.Join(p, "\n"))
			}
		}
	}
	for path := range NotDestinationEnums {
		if _, ok := all[path]; !ok {
			t.Errorf("NotDestinationEnums lists %s, which is not an enum in openapi.json", path)
		}
	}
	// The scan must find the lists it is there to guard.
	if checked < 6 || features < 1 {
		t.Fatalf("found %d destination enums and %d feature action enums in openapi.json; the scan is broken", checked, features)
	}
}

// resourceCollections lists the CRUD resources of the document: the
// collection paths (below /api/v1) that accept POST and have an item path
// one parameter deeper.
func resourceCollections(doc map[string]any) []string {
	paths, _ := doc["paths"].(map[string]any)
	var out []string
	for p, ops := range paths {
		m, _ := ops.(map[string]any)
		if _, ok := m["post"]; !ok || strings.HasSuffix(p, "}") {
			continue
		}
		for q := range paths {
			if strings.HasPrefix(q, p+"/{") && strings.Count(q, "/") == strings.Count(p, "/")+1 && strings.HasSuffix(q, "}") {
				out = append(out, strings.TrimPrefix(p, "/api/v1/"))
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// checkResources reports a resource that is neither a node nor on the
// non-flow list, a catalogue node no resource draws, and entries that name
// something that does not exist.
func checkResources(c Catalogue, resources []string) []string {
	var problems []string
	have := map[string]bool{}
	for _, r := range resources {
		have[r] = true
		_, node := ResourceNodes[r]
		_, other := NonFlowResources[r]
		if !node && !other {
			problems = append(problems, fmt.Sprintf("configuration object %q has CRUD routes but is neither a canvas node nor on the non-flow list: add a node (catalogue.go, build.go, kinds.go ResourceNodes) or list it in NonFlowResources with the reason", r))
		}
		if node && other {
			problems = append(problems, fmt.Sprintf("%q is both a node and on the non-flow list", r))
		}
	}
	drawn := map[string]bool{}
	for r, kind := range ResourceNodes {
		drawn[kind] = true
		if _, ok := c.Node(kind); !ok {
			problems = append(problems, fmt.Sprintf("ResourceNodes[%q] names node kind %q, which is not in the catalogue", r, kind))
		}
		if !have[r] {
			problems = append(problems, fmt.Sprintf("ResourceNodes has %q, which is not a CRUD resource in openapi.json", r))
		}
	}
	for r := range NonFlowResources {
		if !have[r] {
			problems = append(problems, fmt.Sprintf("NonFlowResources has %q, which is not a CRUD resource in openapi.json", r))
		}
	}
	for kind := range SingletonNodes {
		drawn[kind] = true
	}
	for _, n := range c.Nodes {
		if !n.Pseudo && !n.Terminal && !drawn[n.Kind] {
			problems = append(problems, fmt.Sprintf("catalogue node %q is drawn from no configuration object: add its resource to ResourceNodes or SingletonNodes", n.Kind))
		}
	}
	return problems
}

// TestResourceCoverage fails if a configuration object with CRUD routes in the
// OpenAPI document is neither a catalogue node nor on the reasoned list of
// objects that are not part of a call's path.
func TestResourceCoverage(t *testing.T) {
	res := resourceCollections(openAPI(t))
	if len(res) < 25 {
		t.Fatalf("found only %v CRUD resources; the scan is broken", res)
	}
	if p := checkResources(Get(), res); len(p) > 0 {
		t.Error(strings.Join(p, "\n"))
	}
	for r, why := range NonFlowResources {
		if strings.TrimSpace(why) == "" {
			t.Errorf("NonFlowResources[%q] has no reason", r)
		}
	}
}

// checkRequires reports every requires marker that names a feature missing
// from pendingFeatures or whose probe path is already in the API.
func checkRequires(c Catalogue, pending map[string]string, paths map[string]any) []string {
	var problems []string
	check := func(where, marker string) {
		if marker == "" {
			return
		}
		probe, ok := pending[marker]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: requires %q, which is not a pending feature in pendingFeatures", where, marker))
			return
		}
		if _, shipped := paths[probe]; shipped {
			problems = append(problems, fmt.Sprintf("%s: still requires %q, but %s exists: the feature is on main, wire it and drop the marker", where, marker, probe))
		}
	}
	for _, n := range c.Nodes {
		check("node "+n.Kind, n.Requires)
		for _, p := range n.Ports {
			for _, t := range p.Targets {
				check(fmt.Sprintf("%s.%s -> %s.%s", n.Kind, p.ID, t.Kind, t.Port), t.Requires)
			}
		}
	}
	return problems
}

// TestNoStaleRequires fails if a catalogue node or connection keeps a
// requires marker for a feature that already exists on main.
func TestNoStaleRequires(t *testing.T) {
	paths, _ := openAPI(t)["paths"].(map[string]any)
	if p := checkRequires(Get(), pendingFeatures, paths); len(p) > 0 {
		t.Error(strings.Join(p, "\n"))
	}
}

// TestGuardsDetect proves each guard fails on the gap it exists for: a new
// destination kind, a new object type, a stale marker.
func TestGuardsDetect(t *testing.T) {
	c := Get()
	if p := checkKindList(c, "x", []string{"hangup", "extension", "ring_group", "park"}); len(p) != 1 || !strings.Contains(p[0], `"park"`) {
		t.Errorf("an unknown destination kind was not reported: %v", p)
	}
	// A list that adds a kind an output does not connect to, and one that omits one.
	if p := checkKindList(c, "x", []string{"hangup", "extension", "ring_group", "voicemail", "announcement", "paging", "ivr", "external", "time_condition"}); len(p) == 0 {
		t.Error("a list missing the queue kind matched an output")
	}
	if p := checkKindList(c, "x", []string{"hangup", "extension", "ring_group", "voicemail", "announcement", "paging", "ivr", "external", "time_condition", "queue", "sip_uri"}); len(p) == 0 {
		t.Error("a list with an extra kind matched an output")
	}
	if p := checkFeatureActions("x", []string{"dnd_on", "teleport"}); len(p) != 1 {
		t.Errorf("an unclassified feature action was not reported: %v", p)
	}
	if p := checkResources(c, append(resourceCollections(openAPI(t)), "fax-machines")); len(p) == 0 {
		t.Error("a new CRUD resource was accepted")
	}
	pending := map[string]string{"widgets": "/api/v1/widgets"}
	stale := Catalogue{Nodes: []NodeType{{Kind: "widget", Requires: "widgets"}}}
	if p := checkRequires(stale, pending, map[string]any{"/api/v1/widgets": nil}); len(p) != 1 || !strings.Contains(p[0], "on main") {
		t.Errorf("a stale marker was not reported: %v", p)
	}
	if p := checkRequires(stale, pending, map[string]any{}); len(p) != 0 {
		t.Errorf("a marker for an unshipped feature was reported: %v", p)
	}
	if p := checkRequires(Catalogue{Nodes: []NodeType{{Kind: "widget", Requires: "gadgets"}}}, pending, nil); len(p) != 1 {
		t.Errorf("a marker with no pending feature was accepted: %v", p)
	}
}
