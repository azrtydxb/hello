package flow

import (
	"fmt"
	"sort"
	"strings"

	"github.com/azrtydxb/hello/internal/store"
)

// analyse adds the warnings that need the whole graph: nodes no call can
// reach and calls that can loop for ever. The others (dangling references,
// empty groups, disabled voicemail, missing greetings, shadowed routes) are
// raised where the objects are read.
func (b *builder) analyse(_ Input) {
	b.unreachable()
	b.loops()
}

// callEdge reports whether an edge carries a call: the output's port type is
// call or carrier. Member, prompt, schedule, hold music and device edges say
// who uses what, not where a call goes.
func (b *builder) callEdge(e Edge) bool {
	p, ok := b.cat.Port(b.kinds[e.Source.Node], e.Source.Port)
	return ok && (p.Type == PortCall || p.Type == PortCarrier)
}

// unreachable warns about a call-flow node that nothing sends calls to: no
// trunk route, forward, key, branch or feature code leads to it, and no
// number dials it.
func (b *builder) unreachable() {
	next := map[string][]string{}
	for _, e := range b.g.Edges {
		if b.callEdge(e) {
			next[e.Source.Node] = append(next[e.Source.Node], e.Target.Node)
		}
	}
	seen := map[string]bool{}
	var queue []string
	for id := range b.entries {
		if b.nodes[id] {
			seen[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, t := range next[n] {
			if !seen[t] {
				seen[t] = true
				queue = append(queue, t)
			}
		}
	}
	for _, n := range b.g.Nodes {
		switch n.Kind {
		case KindInboundRoute, KindIVR, KindQueue, KindTimeCond, KindRingGroup:
		default:
			continue
		}
		if seen[n.ID] {
			continue
		}
		why := "nothing sends calls here: no route, forward, menu key, branch or feature code leads to it"
		switch n.Kind {
		case KindInboundRoute:
			why = "no trunk leads to this route"
		case KindRingGroup:
			why += ", and its name is not a number that can be dialled"
		case KindIVR, KindQueue:
			why += ", and it has no number to dial"
		}
		b.warn(n.ID, WarnUnreachable, why)
	}
}

// loops warns about calls that cannot leave a cycle. It finds the strongly
// connected components of the call edges; a component with more than one node
// (or a node that leads to itself) is a loop when no node in it can end the
// call and none has an edge out of it. An IVR key that returns to its own
// menu is not a loop while the menu can still time out into a hang-up.
func (b *builder) loops() {
	next := map[string][]string{}
	var ids []string
	for _, n := range b.g.Nodes {
		ids = append(ids, n.ID)
	}
	forwardAlways := map[string]string{} // extension -> where its unconditional forward goes
	for _, e := range b.g.Edges {
		if !b.callEdge(e) {
			continue
		}
		next[e.Source.Node] = append(next[e.Source.Node], e.Target.Node)
		if e.Source.Port == "forwardAlways" {
			forwardAlways[e.Source.Node] = e.Target.Node
		}
	}
	for _, comp := range scc(ids, next) {
		in := map[string]bool{}
		for _, n := range comp {
			in[n] = true
		}
		cyclic := len(comp) > 1
		if !cyclic {
			for _, t := range next[comp[0]] {
				cyclic = cyclic || t == comp[0]
			}
		}
		if !cyclic {
			continue
		}
		canLeave := false
		for _, n := range comp {
			if t, ok := forwardAlways[n]; ok {
				// An extension that always forwards never rings its phones.
				canLeave = canLeave || !in[t]
				continue
			}
			if b.ends[n] {
				canLeave = true
			}
			for _, t := range next[n] {
				if !in[t] {
					canLeave = true
				}
			}
		}
		if canLeave {
			continue
		}
		sort.Strings(comp)
		labels := make([]string, len(comp))
		for i, n := range comp {
			labels[i] = b.label(n)
		}
		msg := "calls can go round in a circle and never leave: " + strings.Join(labels, " -> ")
		for _, n := range comp {
			switch b.kinds[n] {
			case KindIVR, KindTimeCond, KindQueue, KindRingGroup, KindExtension:
				b.warn(n, WarnLoop, msg)
			}
		}
	}
}

func (b *builder) label(id string) string {
	for _, n := range b.g.Nodes {
		if n.ID == id {
			return n.Label
		}
	}
	return id
}

// scc returns the strongly connected components of a graph (Tarjan).
func scc(ids []string, next map[string][]string) [][]string {
	index := map[string]int{}
	low := map[string]int{}
	on := map[string]bool{}
	var stack []string
	var out [][]string
	n := 0
	var connect func(v string)
	connect = func(v string) {
		index[v], low[v] = n, n
		n++
		stack = append(stack, v)
		on[v] = true
		for _, w := range next[v] {
			if _, seen := index[w]; !seen {
				connect(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var comp []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			out = append(out, comp)
		}
	}
	for _, v := range ids {
		if _, seen := index[v]; !seen {
			connect(v)
		}
	}
	return out
}

// shadowedRoutes warns about an enabled inbound route that an earlier one
// always matches first: the earlier route has no narrowing condition (SIP
// domain, header, schedule) and its trunk scope and DID cover the later
// route's.
func (b *builder) shadowedRoutes(in Input) {
	routes := append([]store.InboundRoute(nil), in.Inbound...)
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Position != routes[j].Position {
			return routes[i].Position < routes[j].Position
		}
		return routes[i].ID < routes[j].ID
	})
	for i, r := range routes {
		if !r.Enabled {
			continue
		}
		for _, e := range routes[:i] {
			if routeCovers(e, r) {
				b.warn(nid(KindInboundRoute, r.ID), WarnShadowedRoute,
					fmt.Sprintf("route %q always matches first for this DID", e.Name))
				break
			}
		}
	}
}

// routeCovers reports whether every call route r would take is taken by e
// first.
func routeCovers(e, r store.InboundRoute) bool {
	if !e.Enabled || e.SIPDomain != "" || e.HeaderName != "" || e.HeaderRegex != "" {
		return false
	}
	if e.Schedule != nil && len(e.Schedule.Windows) > 0 {
		return false
	}
	if e.TrunkID != nil && (r.TrunkID == nil || *r.TrunkID != *e.TrunkID) {
		return false
	}
	bare := func(s string) string {
		return strings.TrimPrefix(strings.Join(strings.Fields(s), ""), "+")
	}
	ed, rd := bare(e.DID), bare(r.DID)
	switch e.DIDKind {
	case "any":
		return true
	case "exact":
		return r.DIDKind == "exact" && ed == rd
	case "prefix":
		return (r.DIDKind == "exact" || r.DIDKind == "prefix") && strings.HasPrefix(rd, ed)
	case "regex":
		return r.DIDKind == "regex" && e.DID == r.DID
	}
	return false
}
