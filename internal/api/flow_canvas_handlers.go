package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/flow"
	"github.com/azrtydxb/hello/internal/store"
)

// Flow canvases (spec flow-canvas S-7): named, shared views that store only
// node positions. Saving a layout is not a configuration change: no revision
// bump and no notify, only an audit row.

const (
	// maxLayoutWrite bounds the nodes in one layout save.
	maxLayoutWrite = 1000
	// maxLayoutCoord keeps a position inside what the canvas can show.
	maxLayoutCoord = 1_000_000
)

// flowNodeIDRe is a stored node id, <kind>:<id|key>.
var flowNodeIDRe = regexp.MustCompile(`^([A-Za-z]+):\S{1,200}$`)

// flowCanvasDetail is a canvas with the positions saved on it.
type flowCanvasDetail struct {
	store.FlowCanvas
	Nodes []store.FlowCanvasNode `json:"nodes"`
}

type flowCanvasBody struct {
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Viewport    *json.RawMessage `json:"viewport"`
}

func (b flowCanvasBody) validate() fieldErrs {
	var f fieldErrs
	if b.Name != nil {
		if n := strings.TrimSpace(*b.Name); n == "" || len([]rune(n)) > 64 || n != *b.Name {
			f.add("name", "must be 1-64 characters, without leading or trailing spaces")
		}
	}
	if b.Description != nil && len([]rune(*b.Description)) > 500 {
		f.add("description", "must be at most 500 characters")
	}
	if b.Viewport != nil {
		var vp struct {
			X    *float64 `json:"x"`
			Y    *float64 `json:"y"`
			Zoom *float64 `json:"zoom"`
		}
		if err := json.Unmarshal(*b.Viewport, &vp); err != nil || vp.X == nil || vp.Y == nil || vp.Zoom == nil ||
			*vp.Zoom <= 0 || *vp.Zoom > 100 || math.Abs(*vp.X) > maxLayoutCoord*100 || math.Abs(*vp.Y) > maxLayoutCoord*100 {
			f.add("viewport", "must be an object with numeric x, y and a zoom above 0")
		}
	}
	return f
}

// liveFlowNodes is the id of every node the configuration draws now, devices
// and phones included: the layout of anything else is stale.
func (s *server) liveFlowNodes(r *http.Request) (map[string]bool, error) {
	in, err := s.flowInput(r, true)
	if err != nil {
		return nil, err
	}
	g, err := flow.Build(in, flow.Options{Devices: true})
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		live[n.ID] = true
	}
	return live, nil
}

func liveOnly(nodes []store.FlowCanvasNode, live map[string]bool) []store.FlowCanvasNode {
	out := make([]store.FlowCanvasNode, 0, len(nodes))
	for _, n := range nodes {
		if live[n.Node] {
			out = append(out, n)
		}
	}
	return out
}

func (s *server) listFlowCanvases(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Store.ListFlowCanvases(r.Context())
	if err != nil {
		s.internal(w, "list flow canvases", err)
		return
	}
	writeJSON(w, http.StatusOK, items(cs))
}

func (s *server) createFlowCanvas(w http.ResponseWriter, r *http.Request) {
	var b flowCanvasBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil {
		badRequest(w, "name is required")
		return
	}
	if b.Viewport != nil {
		badRequest(w, "viewport is saved with an update, not on create")
		return
	}
	if f := b.validate(); len(f) > 0 {
		writeFields(w, f)
		return
	}
	desc := ""
	if b.Description != nil {
		desc = *b.Description
	}
	c, err := s.Store.CreateFlowCanvas(r.Context(), actor(r).String(), *b.Name, desc)
	if err != nil {
		s.configError(w, "flow canvas", err)
		return
	}
	writeJSON(w, http.StatusCreated, flowCanvasDetail{FlowCanvas: c, Nodes: []store.FlowCanvasNode{}})
}

func (s *server) getFlowCanvas(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, nodes, err := s.Store.GetFlowCanvas(r.Context(), id)
	if err != nil {
		s.configError(w, "flow canvas", err)
		return
	}
	live, err := s.liveFlowNodes(r)
	if err != nil {
		s.internal(w, "read the flow graph for the layout", err)
		return
	}
	nodes = liveOnly(nodes, live)
	c.NodeCount = len(nodes)
	writeJSON(w, http.StatusOK, flowCanvasDetail{FlowCanvas: c, Nodes: nodes})
}

func (s *server) updateFlowCanvas(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b flowCanvasBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil && b.Description == nil && b.Viewport == nil {
		badRequest(w, "nothing to change")
		return
	}
	if f := b.validate(); len(f) > 0 {
		writeFields(w, f)
		return
	}
	c, err := s.Store.UpdateFlowCanvas(r.Context(), actor(r).String(), id,
		store.FlowCanvasChange{Name: b.Name, Description: b.Description, Viewport: b.Viewport})
	if err != nil {
		s.configError(w, "flow canvas", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) deleteFlowCanvas(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteFlowCanvas(r.Context(), actor(r).String(), id); err != nil {
		s.configError(w, "flow canvas", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type flowLayoutBody struct {
	LayoutVersion *int64                 `json:"layoutVersion"`
	Nodes         []store.FlowCanvasNode `json:"nodes"`
	Remove        []string               `json:"remove"`
}

// validate checks every node id against the catalogue's kinds and every
// position for range, so a bad body is refused before anything is written.
func (b flowLayoutBody) validate() fieldErrs {
	var f fieldErrs
	if b.LayoutVersion == nil || *b.LayoutVersion < 0 {
		f.add("layoutVersion", "is required")
	}
	if len(b.Nodes) > maxLayoutWrite || len(b.Remove) > maxLayoutWrite {
		f.add("nodes", "at most %d nodes per save", maxLayoutWrite)
		return f
	}
	kinds := map[string]bool{}
	for _, n := range flow.Get().Nodes {
		kinds[n.Kind] = true
	}
	check := func(path, id string) {
		m := flowNodeIDRe.FindStringSubmatch(id)
		if m == nil || !kinds[m[1]] {
			f.add(path, "must be <kind>:<id> of a catalogue node kind")
		}
	}
	for i, n := range b.Nodes {
		p := "nodes[" + strconv.Itoa(i) + "]"
		check(p+".node", n.Node)
		if math.Abs(n.X) > maxLayoutCoord || math.Abs(n.Y) > maxLayoutCoord {
			f.add(p, "x and y must be within ±%d", maxLayoutCoord)
		}
	}
	for i, n := range b.Remove {
		check("remove["+strconv.Itoa(i)+"]", n)
	}
	return f
}

// putFlowLayout is PUT /api/v1/flow/canvases/{id}/layout.
func (s *server) putFlowLayout(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b flowLayoutBody
	if !decode(w, r, &b) {
		return
	}
	if f := b.validate(); len(f) > 0 {
		writeFields(w, f)
		return
	}
	live, err := s.liveFlowNodes(r)
	if err != nil {
		s.internal(w, "read the flow graph for the layout", err)
		return
	}
	c, nodes, err := s.Store.PutFlowLayout(r.Context(), actor(r).String(), id, store.FlowLayoutWrite{
		LayoutVersion: *b.LayoutVersion, Nodes: b.Nodes, Remove: b.Remove, Keep: live,
	})
	if errors.Is(err, store.ErrLayoutStale) {
		writeError(w, http.StatusConflict, "layout_stale", "the layout changed since it was read; reload the canvas and merge your moves")
		return
	}
	if err != nil {
		s.configError(w, "flow canvas", err)
		return
	}
	writeJSON(w, http.StatusOK, flowCanvasDetail{FlowCanvas: c, Nodes: nodes})
}
