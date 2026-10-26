package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/store"
)

// Input is the stored configuration the graph is built from. The caller
// reads it consistently (the API retries when the revision moves).
type Input struct {
	Revision          int64
	Trunks            []store.Trunk
	Inbound           []store.InboundRoute
	Outbound          []store.OutboundRoute
	Extensions        []store.Extension
	RingGroups        []store.RingGroup
	IVRMenus          []store.IVRMenu
	Queues            []store.Queue
	PagingGroups      []store.PagingGroup
	MulticastChannels []phonebook.Channel
	Conferences       []store.Conference
	ParkLots          []store.ParkLot
	PickupGroups      []store.PickupGroup
	Announcements     []store.Announcement
	TimeConditions    []store.TimeCondition
	Schedules         []store.Schedule
	Calendars         []store.Calendar
	MOHClasses        []store.MOHClass
	FeatureCodes      []store.FeatureCode
	Devices           []store.Device
	Phones            []store.Phone
}

// Ref names a port of a node.
type Ref struct {
	Node string `json:"node"`
	Port string `json:"port"`
}

// Owner is the one field of one object an edge is stored in (spec S-4).
type Owner struct {
	Node  string `json:"node"`
	Field string `json:"field"`
}

// Node is one object on the canvas.
type Node struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Summary carries the fields the canvas shows on the node.
	Summary map[string]any `json:"summary"`
	// Version is the first 16 hex characters of the sha256 of the object's
	// JSON; it changes whenever the object does. Pseudo and terminal nodes
	// have none.
	Version string `json:"version,omitempty"`
}

// Edge joins an output port to an input port.
type Edge struct {
	ID     string `json:"id"`
	Source Ref    `json:"source"`
	Target Ref    `json:"target"`
	Owner  Owner  `json:"owner"`
	// Implicit edges follow from the configuration but are not stored in a
	// field: the canvas cannot create or delete them.
	Implicit bool           `json:"implicit,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

// Warning codes (spec S-12).
const (
	WarnDangling         = "dangling_reference"
	WarnEmptyGroup       = "empty_group"
	WarnVoicemailDisable = "voicemail_disabled_target"
	WarnUnreachable      = "unreachable"
	WarnLoop             = "loop"
	WarnShadowedRoute    = "shadowed_route"
	WarnNoGreeting       = "no_greeting"
)

// Warning is a problem found in the configuration; it never blocks a
// change.
type Warning struct {
	Node    string `json:"node"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Graph is the configuration drawn as nodes and edges.
type Graph struct {
	ConfigRevision int64     `json:"configRevision"`
	Nodes          []Node    `json:"nodes"`
	Edges          []Edge    `json:"edges"`
	Warnings       []Warning `json:"warnings"`
}

// Options select what Build returns.
type Options struct {
	// Devices adds devices and phones.
	Devices bool
	// Closure, when set, keeps only the nodes reachable from that node by
	// following edges from output to input (spec S-6).
	Closure string
}

func (g Graph) closure(from string) (Graph, error) {
	idx := map[string]bool{}
	for _, n := range g.Nodes {
		idx[n.ID] = true
	}
	if !idx[from] {
		return Graph{}, ErrUnknownNode
	}
	out := map[string][]string{}
	for _, e := range g.Edges {
		out[e.Source.Node] = append(out[e.Source.Node], e.Target.Node)
	}
	seen := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, t := range out[n] {
			if !seen[t] {
				seen[t] = true
				queue = append(queue, t)
			}
		}
	}
	r := Graph{ConfigRevision: g.ConfigRevision, Nodes: []Node{}, Edges: []Edge{}, Warnings: []Warning{}}
	for _, n := range g.Nodes {
		if seen[n.ID] {
			r.Nodes = append(r.Nodes, n)
		}
	}
	for _, e := range g.Edges {
		if seen[e.Source.Node] && seen[e.Target.Node] {
			r.Edges = append(r.Edges, e)
		}
	}
	for _, w := range g.Warnings {
		if seen[w.Node] {
			r.Warnings = append(r.Warnings, w)
		}
	}
	return r, nil
}

func version(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

// NodeByID returns the node with that id.
func (g Graph) NodeByID(id string) (Node, bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}
