package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// canvasFake is an in-memory canvas store with the semantics of the real
// one: a layout version that moves on every save, and pruning to the nodes
// that still exist. Everything else comes from flowFake.
type canvasFake struct {
	flowFake
	mu     sync.Mutex
	nextID int64
	canvas map[int64]*store.FlowCanvas
	nodes  map[int64]map[string]store.FlowCanvasNode
}

func newCanvasFake() *canvasFake {
	return &canvasFake{flowFake: flowFake{rev: 3, exts: []store.Extension{{ID: 1, Number: "100", Name: "Ann"}}},
		canvas: map[int64]*store.FlowCanvas{}, nodes: map[int64]map[string]store.FlowCanvasNode{}}
}

func (f *canvasFake) ListDevices(context.Context) ([]store.Device, error) { return nil, nil }

func (f *canvasFake) ListFlowCanvases(context.Context) ([]store.FlowCanvas, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.FlowCanvas{}
	for _, c := range f.canvas {
		out = append(out, *c)
	}
	return out, nil
}

func (f *canvasFake) GetFlowCanvas(_ context.Context, id int64) (store.FlowCanvas, []store.FlowCanvasNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.canvas[id]
	if !ok {
		return store.FlowCanvas{}, nil, store.ErrNotFound
	}
	return *c, f.list(id), nil
}

func (f *canvasFake) list(id int64) []store.FlowCanvasNode {
	out := []store.FlowCanvasNode{}
	for _, n := range f.nodes[id] {
		out = append(out, n)
	}
	return out
}

func (f *canvasFake) CreateFlowCanvas(_ context.Context, actor, name, desc string) (store.FlowCanvas, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.canvas {
		if c.Name == name {
			return store.FlowCanvas{}, fmt.Errorf("%w: name", store.ErrConflict)
		}
	}
	f.nextID++
	c := &store.FlowCanvas{ID: f.nextID, Name: name, Description: desc, CreatedBy: actor}
	f.canvas[c.ID] = c
	f.nodes[c.ID] = map[string]store.FlowCanvasNode{}
	return *c, nil
}

func (f *canvasFake) UpdateFlowCanvas(_ context.Context, _ string, id int64, ch store.FlowCanvasChange) (store.FlowCanvas, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.canvas[id]
	if !ok {
		return store.FlowCanvas{}, store.ErrNotFound
	}
	if ch.Name != nil {
		c.Name = *ch.Name
	}
	if ch.Description != nil {
		c.Description = *ch.Description
	}
	if ch.Viewport != nil {
		c.Viewport = *ch.Viewport
	}
	return *c, nil
}

func (f *canvasFake) DeleteFlowCanvas(_ context.Context, _ string, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.canvas[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.canvas, id)
	delete(f.nodes, id)
	return nil
}

func (f *canvasFake) PutFlowLayout(_ context.Context, _ string, id int64, w store.FlowLayoutWrite) (store.FlowCanvas, []store.FlowCanvasNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.canvas[id]
	if !ok {
		return store.FlowCanvas{}, nil, store.ErrNotFound
	}
	if c.LayoutVersion != w.LayoutVersion {
		return store.FlowCanvas{}, nil, store.ErrLayoutStale
	}
	for _, n := range w.Nodes {
		f.nodes[id][n.Node] = n
	}
	for _, n := range w.Remove {
		delete(f.nodes[id], n)
	}
	if w.Keep != nil {
		for k := range f.nodes[id] {
			if !w.Keep[k] {
				delete(f.nodes[id], k)
			}
		}
	}
	c.LayoutVersion++
	return *c, f.list(id), nil
}

func canvasReq(t *testing.T, h http.HandlerFunc, method, id string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&rd).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "/api/v1/flow/canvases", &rd)
	if id != "" {
		req.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestFlowCanvasHandlers drives the canvas handlers against the in-memory
// store. It fails if a bad name, description, viewport, layout version, node
// id or position is accepted, a duplicate name is not a conflict, a stale
// layout version saves anything, or the layout of an object that no longer
// exists is returned or kept.
func TestFlowCanvasHandlers(t *testing.T) {
	f := newCanvasFake()
	s := &server{Config{Store: f}}

	if c := canvasReq(t, s.createFlowCanvas, "POST", "", map[string]any{"name": "Main office", "description": "HQ"}).Code; c != 201 {
		t.Fatalf("create = %d", c)
	}
	for name, body := range map[string]map[string]any{
		"duplicate":     {"name": "Main office"},
		"no name":       {},
		"blank name":    {"name": " "},
		"padded name":   {"name": " x"},
		"long name":     {"name": string(make([]byte, 65))},
		"viewport":      {"name": "v", "viewport": map[string]any{"x": 1, "y": 1, "zoom": 1}},
		"unknown field": {"name": "u", "extra": 1},
	} {
		want := 400
		if name == "duplicate" {
			want = 409
		}
		if c := canvasReq(t, s.createFlowCanvas, "POST", "", body).Code; c != want {
			t.Errorf("create %s = %d, want %d", name, c, want)
		}
	}

	if c := canvasReq(t, s.getFlowCanvas, "GET", "9", nil).Code; c != 404 {
		t.Errorf("get unknown = %d", c)
	}
	if c := canvasReq(t, s.getFlowCanvas, "GET", "x", nil).Code; c != 404 {
		t.Errorf("get bad id = %d", c)
	}

	// Patch: needs something, validates the viewport.
	if c := canvasReq(t, s.updateFlowCanvas, "PATCH", "1", map[string]any{}).Code; c != 400 {
		t.Errorf("empty patch = %d", c)
	}
	if c := canvasReq(t, s.updateFlowCanvas, "PATCH", "1", map[string]any{"viewport": map[string]any{"x": 1, "y": 2, "zoom": 0}}).Code; c != 400 {
		t.Errorf("zero zoom = %d", c)
	}
	if c := canvasReq(t, s.updateFlowCanvas, "PATCH", "1", map[string]any{"viewport": map[string]any{"x": 1}}).Code; c != 400 {
		t.Errorf("partial viewport = %d", c)
	}
	rec := canvasReq(t, s.updateFlowCanvas, "PATCH", "1", map[string]any{"name": "HQ", "viewport": map[string]any{"x": 10, "y": -5, "zoom": 0.5}})
	if rec.Code != 200 || f.canvas[1].Name != "HQ" || len(f.canvas[1].Viewport) == 0 {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}
	if c := canvasReq(t, s.updateFlowCanvas, "PATCH", "7", map[string]any{"name": "x"}).Code; c != 404 {
		t.Errorf("patch unknown = %d", c)
	}

	// Layout.
	put := func(body any) *httptest.ResponseRecorder { return canvasReq(t, s.putFlowLayout, "PUT", "1", body) }
	node := func(id string, x, y float64) map[string]any { return map[string]any{"node": id, "x": x, "y": y} }
	for name, body := range map[string]map[string]any{
		"no version":   {"nodes": []any{}},
		"neg version":  {"layoutVersion": -1},
		"unknown kind": {"layoutVersion": 0, "nodes": []any{node("bogus:1", 0, 0)}},
		"no id":        {"layoutVersion": 0, "nodes": []any{node("extension:", 0, 0)}},
		"far away":     {"layoutVersion": 0, "nodes": []any{node("extension:1", 5e6, 0)}},
		"bad remove":   {"layoutVersion": 0, "remove": []any{"nope"}},
	} {
		if c := put(body).Code; c != 400 {
			t.Errorf("layout %s = %d, want 400", name, c)
		}
	}
	if c := canvasReq(t, s.putFlowLayout, "PUT", "9", map[string]any{"layoutVersion": 0}).Code; c != 404 {
		t.Errorf("layout unknown canvas = %d", c)
	}

	rec = put(map[string]any{"layoutVersion": 0, "nodes": []any{node("extension:1", 10, 20), node("extension:99", 1, 1), node("ringGroup:5", 3, 3)}})
	if rec.Code != 200 {
		t.Fatalf("layout = %d %s", rec.Code, rec.Body)
	}
	var got flowCanvasDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Objects that do not exist (extension 99, ring group 5) are pruned.
	if got.LayoutVersion != 1 || len(got.Nodes) != 1 || got.Nodes[0].Node != "extension:1" || got.Nodes[0].X != 10 {
		t.Fatalf("saved layout = %+v", got)
	}
	if rev := f.rev; rev != 3 {
		t.Errorf("a layout save moved the configuration revision to %d", rev)
	}

	// A save made against the old version changes nothing.
	rec = put(map[string]any{"layoutVersion": 0, "nodes": []any{node("extension:1", 500, 500)}})
	if rec.Code != 409 || f.nodes[1]["extension:1"].X != 10 {
		t.Fatalf("stale layout = %d %s, x = %v", rec.Code, rec.Body, f.nodes[1]["extension:1"].X)
	}
	var e struct{ Error struct{ Code string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error.Code != "layout_stale" {
		t.Errorf("stale code = %q", e.Error.Code)
	}

	// The object disappears: the canvas no longer returns its position.
	f.mu.Lock()
	f.nodes[1]["extension:42"] = store.FlowCanvasNode{Node: "extension:42"}
	f.mu.Unlock()
	rec = canvasReq(t, s.getFlowCanvas, "GET", "1", nil)
	got = flowCanvasDetail{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || len(got.Nodes) != 1 || got.NodeCount != 1 {
		t.Fatalf("get = %d %+v", rec.Code, got)
	}

	rec = put(map[string]any{"layoutVersion": 1, "remove": []any{"extension:1"}})
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || len(f.nodes[1]) != 0 {
		t.Fatalf("remove = %d %v", rec.Code, f.nodes[1])
	}

	if c := canvasReq(t, s.deleteFlowCanvas, "DELETE", "1", nil).Code; c != 204 {
		t.Errorf("delete = %d", c)
	}
	if c := canvasReq(t, s.deleteFlowCanvas, "DELETE", "1", nil).Code; c != 404 {
		t.Errorf("delete again = %d", c)
	}
	rec = canvasReq(t, s.listFlowCanvases, "GET", "", nil)
	if rec.Code != 200 || rec.Body.String() != "{\"items\":[]}\n" {
		t.Errorf("list = %d %q", rec.Code, rec.Body)
	}
}

// TestFlowCanvasAPI runs the canvases through the real API and database: the
// roles, every documented status, the audit trail, and that saving a layout
// leaves the configuration revision alone. It needs the test database.
func TestFlowCanvasAPI(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	viewer, _ := e.asRole("vera", auth.RoleViewer)
	ctx := context.Background()

	created := c.must(http.StatusCreated, "POST", "/api/v1/flow/canvases", map[string]any{"name": "Main office", "description": "HQ"}).json(t)
	id := fmt.Sprint(int64(created["id"].(float64)))
	base := "/api/v1/flow/canvases/" + id
	c.must(http.StatusConflict, "POST", "/api/v1/flow/canvases", map[string]any{"name": "Main office"})
	c.must(http.StatusBadRequest, "POST", "/api/v1/flow/canvases", map[string]any{"name": ""})
	c.must(http.StatusNotFound, "GET", "/api/v1/flow/canvases/987654", nil)
	c.must(http.StatusNotFound, "PATCH", "/api/v1/flow/canvases/987654", map[string]any{"name": "x"})
	c.must(http.StatusBadRequest, "PATCH", base, map[string]any{})
	other := c.must(http.StatusCreated, "POST", "/api/v1/flow/canvases", map[string]any{"name": "After hours"}).json(t)
	c.must(http.StatusConflict, "PATCH", base, map[string]any{"name": "After hours"})
	c.must(http.StatusOK, "PATCH", base, map[string]any{"viewport": map[string]any{"x": 1, "y": 2, "zoom": 0.75}})
	if got := c.must(http.StatusOK, "GET", base, nil).json(t); got["description"] != "HQ" || got["viewport"] == nil {
		t.Fatalf("canvas = %v", got)
	}

	// Viewers read, operators write.
	viewer.must(http.StatusOK, "GET", "/api/v1/flow/canvases", nil)
	viewer.must(http.StatusOK, "GET", base, nil)
	viewer.must(http.StatusForbidden, "POST", "/api/v1/flow/canvases", map[string]any{"name": "v"})
	viewer.must(http.StatusForbidden, "PATCH", base, map[string]any{"name": "v"})
	viewer.must(http.StatusForbidden, "DELETE", base, nil)
	viewer.must(http.StatusForbidden, "PUT", base+"/layout", map[string]any{"layoutVersion": 0})

	ext := fmt.Sprintf("extension:%v", e.ext101["id"])
	rev0, err := e.st.ConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	put := func(want int, version int, nodes ...map[string]any) response {
		body := map[string]any{"layoutVersion": version}
		if len(nodes) > 0 {
			body["nodes"] = nodes
		}
		return c.must(want, "PUT", base+"/layout", body)
	}
	node := func(id string, x, y float64) map[string]any { return map[string]any{"node": id, "x": x, "y": y} }
	put(http.StatusBadRequest, 0, node("bogus:1", 0, 0))
	c.must(http.StatusNotFound, "PUT", "/api/v1/flow/canvases/987654/layout", map[string]any{"layoutVersion": 0})
	got := put(http.StatusOK, 0, node(ext, 12.5, -40), node("extension:987654", 1, 1)).json(t)
	nodes := got["nodes"].([]any)
	if got["layoutVersion"] != float64(1) || len(nodes) != 1 || nodes[0].(map[string]any)["x"] != 12.5 {
		t.Fatalf("layout = %v", got)
	}
	put(http.StatusConflict, 0, node(ext, 99, 99))
	if rev1, err := e.st.ConfigRevision(ctx); err != nil || rev1 != rev0 {
		t.Fatalf("revision %d -> %d (%v): a layout save is not a configuration change", rev0, rev1, err)
	}
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE resource = 'flow_canvas' AND action = 'layout'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("layout audit rows = %d, %v (the stale save must leave none)", n, err)
	}

	// The same extension can sit on another canvas.
	otherID := fmt.Sprint(int64(other["id"].(float64)))
	c.must(http.StatusOK, "PUT", "/api/v1/flow/canvases/"+otherID+"/layout",
		map[string]any{"layoutVersion": 0, "nodes": []any{node(ext, 1, 1)}})

	// Deleting the object drops its layout on read and on the next save.
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/extensions/%v", e.ext101["id"]), nil)
	if got := c.must(http.StatusOK, "GET", base, nil).json(t); len(got["nodes"].([]any)) != 0 {
		t.Fatalf("layout of a deleted extension still returned: %v", got["nodes"])
	}
	put(http.StatusOK, 1)
	if err := e.db.QueryRow(`SELECT count(*) FROM flow_canvas_nodes WHERE canvas_id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("saved nodes after the prune = %d, %v", n, err)
	}

	// Deleting a canvas takes its layout and leaves the configuration.
	c.must(http.StatusNoContent, "DELETE", base, nil)
	c.must(http.StatusNotFound, "DELETE", base, nil)
	if err := e.db.QueryRow(`SELECT count(*) FROM flow_canvas_nodes WHERE canvas_id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("layout rows after delete = %d, %v", n, err)
	}
	list := c.must(http.StatusOK, "GET", "/api/v1/flow/canvases", nil).json(t)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != "After hours" {
		t.Fatalf("canvases = %v", list)
	}
}
