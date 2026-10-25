package flow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/azrtydxb/hello/internal/store"
)

// Input is the stored configuration the graph is built from. The caller
// reads it consistently (the API retries when the revision moves).
type Input struct {
	Revision      int64
	Trunks        []store.Trunk
	Inbound       []store.InboundRoute
	Outbound      []store.OutboundRoute
	Extensions    []store.Extension
	RingGroups    []store.RingGroup
	PagingGroups  []store.PagingGroup
	Conferences   []store.Conference
	Announcements []store.Announcement
	FeatureCodes  []store.FeatureCode
	Devices       []store.Device
	Phones        []store.Phone
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

// Warning codes (spec S-12; the rest arrive with the dry-run).
const (
	WarnDangling         = "dangling_reference"
	WarnEmptyGroup       = "empty_group"
	WarnVoicemailDisable = "voicemail_disabled_target"
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

// ErrUnknownNode is returned for a closure from a node that does not exist.
var ErrUnknownNode = errors.New("flow: unknown node")

var numberRe = regexp.MustCompile(`^\+?[0-9*#]{1,32}$`)

type builder struct {
	g     Graph
	nodes map[string]bool
	edges map[string]bool

	groupByName  map[string]string // ring group name -> node id
	extByNumber  map[string]string // extension number -> node id
	exts         map[string]store.Extension
	annByName    map[string]string
	pagingByKey  map[string]string
	confByNumber map[string]string
}

// Build draws the configuration. The result is deterministic: nodes and
// edges are sorted by id.
func Build(in Input, opt Options) (Graph, error) {
	b := &builder{
		g:            Graph{ConfigRevision: in.Revision, Nodes: []Node{}, Edges: []Edge{}, Warnings: []Warning{}},
		nodes:        map[string]bool{},
		edges:        map[string]bool{},
		groupByName:  map[string]string{},
		extByNumber:  map[string]string{},
		exts:         map[string]store.Extension{},
		annByName:    map[string]string{},
		pagingByKey:  map[string]string{},
		confByNumber: map[string]string{},
	}
	b.addObjects(in)
	b.addRoutes(in)
	b.addExtensions(in)
	b.addGroups(in)
	b.addFeatureCodes(in)
	if opt.Devices {
		b.addDevices(in)
	}
	sort.Slice(b.g.Nodes, func(i, j int) bool { return b.g.Nodes[i].ID < b.g.Nodes[j].ID })
	sort.Slice(b.g.Edges, func(i, j int) bool { return b.g.Edges[i].ID < b.g.Edges[j].ID })
	sort.Slice(b.g.Warnings, func(i, j int) bool {
		a, c := b.g.Warnings[i], b.g.Warnings[j]
		if a.Node != c.Node {
			return a.Node < c.Node
		}
		return a.Code < c.Code
	})
	if opt.Closure != "" {
		return b.g.closure(opt.Closure)
	}
	return b.g, nil
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

func (b *builder) node(id, kind, label string, summary map[string]any, obj any) {
	if b.nodes[id] {
		return
	}
	if summary == nil {
		summary = map[string]any{}
	}
	n := Node{ID: id, Kind: kind, Label: label, Summary: summary}
	if obj != nil {
		n.Version = version(obj)
	}
	b.g.Nodes = append(b.g.Nodes, n)
	b.nodes[id] = true
}

func (b *builder) edge(src Ref, dst Ref, owner Owner, implicit bool, data map[string]any) {
	id := src.Node + "." + src.Port + "->" + dst.Node + "." + dst.Port
	if b.edges[id] {
		return
	}
	b.edges[id] = true
	b.g.Edges = append(b.g.Edges, Edge{ID: id, Source: src, Target: dst, Owner: owner, Implicit: implicit, Data: data})
}

func (b *builder) warn(node, code, msg string) {
	b.g.Warnings = append(b.g.Warnings, Warning{Node: node, Code: code, Message: msg})
}

func nid(kind string, id int64) string { return fmt.Sprintf("%s:%d", kind, id) }

func (b *builder) addObjects(in Input) {
	for _, t := range in.Trunks {
		b.node(nid(KindTrunk, t.ID), KindTrunk, t.Name, map[string]any{
			"mode": t.Mode, "enabled": t.Enabled, "maxCalls": t.MaxCalls,
		}, t)
	}
	for _, e := range in.Extensions {
		id := nid(KindExtension, e.ID)
		b.extByNumber[e.Number] = id
		b.exts[id] = e
		// The no-answer path ends in the extension's own voicemail when no
		// forward is set; it is a summary, not an edge (it would be a loop
		// on the same node).
		b.node(id, KindExtension, e.Number+" "+e.Name, map[string]any{
			"number": e.Number, "name": e.Name, "dnd": e.DND,
			"voicemail":   e.VoicemailEnabled,
			"noAnswerEnd": noAnswerEnd(e),
		}, e)
	}
	for _, g := range in.RingGroups {
		b.groupByName[g.Name] = nid(KindRingGroup, g.ID)
		b.node(nid(KindRingGroup, g.ID), KindRingGroup, g.Name, map[string]any{
			"strategy": g.Strategy, "members": len(g.Members), "ringTimeout": g.RingTimeout,
		}, g)
	}
	for _, a := range in.Announcements {
		b.annByName[a.Name] = nid(KindAnnouncement, a.ID)
		b.node(nid(KindAnnouncement, a.ID), KindAnnouncement, a.Name, nil, a)
	}
	for _, c := range in.Conferences {
		b.confByNumber[c.Number] = nid(KindConference, c.ID)
		b.node(nid(KindConference, c.ID), KindConference, c.Name, map[string]any{
			"number": c.Number, "maxParticipants": c.MaxParticipants, "enabled": c.Enabled,
			"waitForLeader": c.WaitForLeader, "record": c.Record, "pin": c.HasUserPIN,
		}, c)
	}
	for _, p := range in.PagingGroups {
		b.pagingByKey[p.Code] = nid(KindPagingGroup, p.ID)
		b.pagingByKey[p.Name] = nid(KindPagingGroup, p.ID)
		b.node(nid(KindPagingGroup, p.ID), KindPagingGroup, p.Name, map[string]any{
			"code": p.Code, "priority": p.Priority, "enabled": p.Enabled, "members": len(p.Members),
		}, p)
	}
}

func noAnswerEnd(e store.Extension) string {
	switch {
	case e.ForwardNoAnswer != "":
		return "forward"
	case e.VoicemailEnabled:
		return "voicemail"
	}
	return "hangup"
}

// terminal returns the id of the terminal node for a number, creating it.
func (b *builder) external(number string) string {
	id := KindExternal + ":" + number
	b.node(id, KindExternal, number, map[string]any{"number": number}, nil)
	return id
}

func (b *builder) missing(owner, target, why string) string {
	id := KindMissing + ":" + target
	b.node(id, KindMissing, target, map[string]any{"target": target}, nil)
	b.warn(owner, WarnDangling, why)
	return id
}

// callTarget resolves a dialled number the way hello-sip does: a ring group
// by name first, then an extension by number, otherwise an external number
// (spec S-5).
func (b *builder) callTarget(owner, target string) Ref {
	if id, ok := b.groupByName[target]; ok {
		return Ref{id, "ring"}
	}
	if id, ok := b.extByNumber[target]; ok {
		return Ref{id, "ring"}
	}
	if numberRe.MatchString(target) {
		return Ref{b.external(target), "call"}
	}
	return Ref{b.missing(owner, target, fmt.Sprintf("%q is not a ring group, an extension or a number", target)), "call"}
}

func (b *builder) addRoutes(in Input) {
	anyTrunk, dial := false, false
	for _, r := range in.Inbound {
		rid := nid(KindInboundRoute, r.ID)
		b.node(rid, KindInboundRoute, r.Name, map[string]any{
			"did": r.DID, "didKind": r.DIDKind, "position": r.Position, "enabled": r.Enabled,
		}, r)
		switch {
		case r.TrunkID != nil:
			t := nid(KindTrunk, *r.TrunkID)
			if b.nodes[t] {
				b.edge(Ref{t, "inbound"}, Ref{rid, "trunk"}, Owner{rid, "trunkId"}, false, nil)
			} else {
				b.warn(rid, WarnDangling, fmt.Sprintf("trunk %d does not exist", *r.TrunkID))
			}
		default:
			anyTrunk = true
			b.edge(Ref{KindAnyTrunk + ":any", "inbound"}, Ref{rid, "trunk"}, Owner{rid, "trunkId"}, false, nil)
		}
		var dst Ref
		switch r.DestinationKind {
		case "extension":
			if id, ok := b.groupByName[r.Destination]; ok {
				dst = Ref{id, "ring"}
			} else if id, ok := b.extByNumber[r.Destination]; ok {
				dst = Ref{id, "ring"}
			} else {
				dst = Ref{b.missing(rid, r.Destination, fmt.Sprintf("destination %q is not an extension or ring group", r.Destination)), "call"}
			}
		case "external":
			dst = Ref{b.external(r.Destination), "call"}
		case "conference":
			if id, ok := b.confByNumber[r.Destination]; ok {
				dst = Ref{id, "call"}
			} else {
				dst = Ref{b.missing(rid, r.Destination, fmt.Sprintf("conference room %q does not exist", r.Destination)), "call"}
			}
		case "sip_uri":
			id := KindSIPURI + ":" + r.Destination
			b.node(id, KindSIPURI, r.Destination, map[string]any{"uri": r.Destination}, nil)
			dst = Ref{id, "call"}
		case "internal":
			id := nid(KindInternalDID, r.ID)
			b.node(id, KindInternalDID, "DID "+r.DID+" as extension", map[string]any{"did": r.DID}, nil)
			dst = Ref{id, "call"}
		default:
			continue
		}
		b.edge(Ref{rid, "destination"}, dst, Owner{rid, "destinationKind+destination"}, false, nil)
	}
	for _, r := range in.Outbound {
		rid := nid(KindOutboundRoute, r.ID)
		b.node(rid, KindOutboundRoute, r.Name, map[string]any{
			"matchKind": r.MatchKind, "match": r.Match, "position": r.Position,
			"emergency": r.Emergency, "enabled": r.Enabled,
		}, r)
		dial = true
		b.edge(Ref{KindDialPlan + ":internal", "dial"}, Ref{rid, "dial"}, Owner{rid, ""}, true, nil)
		for i, tid := range r.Trunks {
			t := nid(KindTrunk, tid)
			if !b.nodes[t] {
				b.warn(rid, WarnDangling, fmt.Sprintf("trunk %d does not exist", tid))
				continue
			}
			b.edge(Ref{rid, "trunks"}, Ref{t, "carrier"}, Owner{rid, "trunks"}, false, map[string]any{"order": i + 1})
		}
	}
	if anyTrunk {
		b.node(KindAnyTrunk+":any", KindAnyTrunk, "Any trunk", nil, nil)
	}
	if dial {
		b.node(KindDialPlan+":internal", KindDialPlan, "Internal callers", nil, nil)
	}
}

func (b *builder) addExtensions(in Input) {
	for _, e := range in.Extensions {
		id := nid(KindExtension, e.ID)
		for _, f := range []struct{ port, v string }{
			{"forwardAlways", e.ForwardAlways}, {"forwardBusy", e.ForwardBusy}, {"forwardNoAnswer", e.ForwardNoAnswer},
		} {
			if f.v == "" {
				continue
			}
			dst := b.callTarget(id, f.v)
			b.edge(Ref{id, f.port}, dst, Owner{id, f.port}, false, nil)
		}
	}
}

func (b *builder) member(ext int64, dstNode, dstPort, owner, field string, data map[string]any) {
	src := nid(KindExtension, ext)
	if !b.nodes[src] {
		b.warn(dstNode, WarnDangling, fmt.Sprintf("member extension %d does not exist", ext))
		return
	}
	b.edge(Ref{src, "member"}, Ref{dstNode, dstPort}, Owner{owner, field}, false, data)
}

func (b *builder) addGroups(in Input) {
	for _, g := range in.RingGroups {
		id := nid(KindRingGroup, g.ID)
		if len(g.Members) == 0 {
			b.warn(id, WarnEmptyGroup, "the ring group has no members")
		}
		for _, m := range g.Members {
			b.member(m.ExtensionID, id, "members", id, "members",
				map[string]any{"position": m.Position, "weight": m.Weight, "delay": m.Delay})
		}
		switch g.FailureKind {
		case "voicemail":
			if ext, ok := b.extByNumber[g.FailureTarget]; ok {
				b.edge(Ref{id, "noAnswer"}, Ref{ext, "voicemail"}, Owner{id, "failureKind+failureTarget"}, false, nil)
				if !b.exts[ext].VoicemailEnabled {
					b.warn(id, WarnVoicemailDisable, fmt.Sprintf("voicemail is off for extension %s", g.FailureTarget))
				}
			} else {
				dst := b.missing(id, g.FailureTarget, fmt.Sprintf("voicemail box %q is not an extension", g.FailureTarget))
				b.edge(Ref{id, "noAnswer"}, Ref{dst, "call"}, Owner{id, "failureKind+failureTarget"}, false, nil)
			}
		case "external":
			b.edge(Ref{id, "noAnswer"}, Ref{b.external(g.FailureTarget), "call"}, Owner{id, "failureKind+failureTarget"}, false, nil)
		case "announcement":
			if ann, ok := b.annByName[g.FailureTarget]; ok {
				b.edge(Ref{id, "noAnswer"}, Ref{ann, "call"}, Owner{id, "failureKind+failureTarget"}, false, nil)
			} else {
				dst := b.missing(id, g.FailureTarget, fmt.Sprintf("announcement %q does not exist", g.FailureTarget))
				b.edge(Ref{id, "noAnswer"}, Ref{dst, "call"}, Owner{id, "failureKind+failureTarget"}, false, nil)
			}
		}
	}
	for _, p := range in.PagingGroups {
		id := nid(KindPagingGroup, p.ID)
		if len(p.Members) == 0 {
			b.warn(id, WarnEmptyGroup, "the paging group has no members")
		}
		for i, m := range p.Members {
			b.member(m.ExtensionID, id, "members", id, "members", map[string]any{"position": i + 1})
		}
		for _, m := range p.Callers {
			b.member(m.ExtensionID, id, "callers", id, "callers", nil)
		}
	}
}

func (b *builder) addFeatureCodes(in Input) {
	for _, c := range in.FeatureCodes {
		if c.Action != "announcement" {
			continue // the other actions have no call target
		}
		id := KindFeatureCode + ":" + c.Code
		b.node(id, KindFeatureCode, c.Code, map[string]any{"code": c.Code, "action": c.Action}, c)
		dst, ok := b.annByName[c.Argument]
		if !ok {
			dst = b.missing(id, c.Argument, fmt.Sprintf("announcement %q does not exist", c.Argument))
			b.edge(Ref{id, "target"}, Ref{dst, "call"}, Owner{id, "argument"}, false, nil)
			continue
		}
		b.edge(Ref{id, "target"}, Ref{dst, "call"}, Owner{id, "argument"}, false, nil)
	}
}

func (b *builder) addDevices(in Input) {
	for _, d := range in.Devices {
		id := nid(KindDevice, d.ID)
		b.node(id, KindDevice, d.SIPUsername, map[string]any{"enabled": d.Enabled}, d)
		ext := nid(KindExtension, d.ExtensionID)
		if b.nodes[ext] {
			b.edge(Ref{id, "extension"}, Ref{ext, "devices"}, Owner{id, "extensionId"}, false, nil)
		}
	}
	for _, p := range in.Phones {
		id := nid(KindPhone, p.ID)
		label := p.Label
		if label == "" {
			label = p.MAC
		}
		b.node(id, KindPhone, label, map[string]any{"mac": p.MAC, "vendor": string(p.Vendor), "model": p.Model}, nil)
		if p.DeviceID != nil {
			dev := nid(KindDevice, *p.DeviceID)
			if b.nodes[dev] {
				b.edge(Ref{id, "device"}, Ref{dev, "phone"}, Owner{id, "deviceId"}, false, nil)
			}
		}
	}
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
