package flow

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/azrtydxb/hello/internal/store"
)

// ErrUnknownNode is returned for a closure from a node that does not exist.
var ErrUnknownNode = errors.New("flow: unknown node")

var numberRe = regexp.MustCompile(`^\+?[0-9*#]{1,32}$`)

type builder struct {
	g     Graph
	cat   Catalogue
	nodes map[string]bool
	kinds map[string]string // node id -> node kind
	edges map[string]bool
	// ends holds the nodes where a call can end by itself: a hang-up
	// destination, or members/agents who answer. The loop check reads it.
	ends map[string]bool
	// entries holds the nodes a call can start at without an edge.
	entries map[string]bool

	groupByName  map[string]string // ring group name -> node id
	extByNumber  map[string]string // extension number -> node id
	exts         map[string]store.Extension
	annByName    map[string]string
	annByID      map[int64]string
	pagingByCode map[string]string
	// vmGroupByNumber maps a voicemail group's number to its node id.
	vmGroupByNumber map[string]string
	confByNumber    map[string]string
	ivrByName       map[string]string
	queueByName     map[string]string
	tcByName        map[string]string
	mohByID         map[int64]string
	schedByID       map[int64]string
	mcByID          map[int64]string
}

// Build draws the configuration. The result is deterministic: nodes and
// edges are sorted by id.
func Build(in Input, opt Options) (Graph, error) {
	b := &builder{
		g:               Graph{ConfigRevision: in.Revision, Nodes: []Node{}, Edges: []Edge{}, Warnings: []Warning{}},
		cat:             buildCatalogue(),
		nodes:           map[string]bool{},
		kinds:           map[string]string{},
		edges:           map[string]bool{},
		ends:            map[string]bool{},
		entries:         map[string]bool{},
		groupByName:     map[string]string{},
		extByNumber:     map[string]string{},
		exts:            map[string]store.Extension{},
		annByName:       map[string]string{},
		annByID:         map[int64]string{},
		pagingByCode:    map[string]string{},
		vmGroupByNumber: map[string]string{},
		confByNumber:    map[string]string{},
		ivrByName:       map[string]string{},
		queueByName:     map[string]string{},
		tcByName:        map[string]string{},
		mohByID:         map[int64]string{},
		schedByID:       map[int64]string{},
		mcByID:          map[int64]string{},
	}
	b.addObjects(in)
	b.addRoutes(in)
	b.addExtensions(in)
	b.addGroups(in)
	b.addIVR(in)
	b.addQueues(in)
	b.addTimeConditions(in)
	b.addResources(in)
	b.addFeatureCodes(in)
	if opt.Devices {
		b.addDevices(in)
	}
	b.analyse(in)
	sort.Slice(b.g.Nodes, func(i, j int) bool { return b.g.Nodes[i].ID < b.g.Nodes[j].ID })
	sort.Slice(b.g.Edges, func(i, j int) bool { return b.g.Edges[i].ID < b.g.Edges[j].ID })
	sort.Slice(b.g.Warnings, func(i, j int) bool {
		a, c := b.g.Warnings[i], b.g.Warnings[j]
		if a.Node != c.Node {
			return a.Node < c.Node
		}
		if a.Code != c.Code {
			return a.Code < c.Code
		}
		return a.Message < c.Message
	})
	if opt.Closure != "" {
		return b.g.closure(opt.Closure)
	}
	return b.g, nil
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
	b.kinds[id] = kind
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

func nid(kind string, id int64) string { return kind + ":" + strconv.FormatInt(id, 10) }

func (b *builder) addObjects(in Input) {
	for _, t := range in.Trunks {
		id := nid(KindTrunk, t.ID)
		b.entries[id] = true
		b.node(id, KindTrunk, t.Name, map[string]any{
			"mode": t.Mode, "enabled": t.Enabled, "maxCalls": t.MaxCalls,
		}, t)
	}
	for _, e := range in.Extensions {
		id := nid(KindExtension, e.ID)
		b.extByNumber[e.Number] = id
		b.exts[id] = e
		b.entries[id] = true // dialled by its number
		// The no-answer path ends in the extension's own voicemail when no
		// forward is set; it is a summary, not an edge (it would be a loop
		// on the same node).
		b.node(id, KindExtension, e.Number+" "+e.Name, map[string]any{
			"number": e.Number, "name": e.Name, "dnd": e.DND,
			"voicemail":   e.VoicemailEnabled,
			"noAnswerEnd": noAnswerEnd(e),
		}, e)
		if e.ForwardAlways == "" {
			b.ends[id] = true // its phones ring
		}
	}
	for _, g := range in.RingGroups {
		id := nid(KindRingGroup, g.ID)
		b.groupByName[g.Name] = id
		if numberRe.MatchString(g.Name) {
			b.entries[id] = true // dialled by its name
		}
		if len(g.Members) > 0 {
			b.ends[id] = true // a member answers
		}
		b.node(id, KindRingGroup, g.Name, map[string]any{
			"strategy": g.Strategy, "members": len(g.Members), "ringTimeout": g.RingTimeout,
		}, g)
	}
	for _, a := range in.Announcements {
		id := nid(KindAnnouncement, a.ID)
		b.annByName[a.Name] = id
		b.annByID[a.ID] = id
		b.ends[id] = true // plays, then hangs up
		sum := map[string]any{"generated": a.Generated}
		if a.Generated {
			// The announcement was made from text (tts-prompts): badge it.
			sum["textEdited"] = a.TextEdited
			sum["voiceId"] = a.VoiceID
			sum["modelId"] = a.ModelID
		}
		b.node(id, KindAnnouncement, a.Name, sum, a)
	}
	for _, c := range in.Conferences {
		id := nid(KindConference, c.ID)
		b.confByNumber[c.Number] = id
		b.entries[id] = true
		b.ends[id] = true
		b.node(id, KindConference, c.Name, map[string]any{
			"number": c.Number, "maxParticipants": c.MaxParticipants, "enabled": c.Enabled,
			"waitForLeader": c.WaitForLeader, "record": c.Record, "pin": c.HasUserPIN,
		}, c)
	}
	for _, p := range in.PagingGroups {
		id := nid(KindPagingGroup, p.ID)
		b.pagingByCode[p.Code] = id
		b.entries[id] = true // dialled by its code
		b.ends[id] = true
		b.node(id, KindPagingGroup, p.Name, map[string]any{
			"code": p.Code, "priority": p.Priority, "enabled": p.Enabled, "members": len(p.Members),
		}, p)
	}
	for _, c := range in.MulticastChannels {
		id := nid(KindMulticast, c.ID)
		b.mcByID[c.ID] = id
		b.node(id, KindMulticast, c.Label, map[string]any{
			"address": c.Address, "port": c.Port, "codec": c.Codec, "kind": c.Kind, "enabled": c.Enabled,
		}, c)
	}
	for _, m := range in.IVRMenus {
		id := nid(KindIVR, m.ID)
		b.ivrByName[m.Name] = id
		if m.Number != "" {
			b.entries[id] = true // dialled by its number
		}
		b.node(id, KindIVR, m.Name, map[string]any{
			"number": m.Number, "enabled": m.Enabled, "options": len(m.Options),
			"allowDirectDial": m.AllowDirectDial, "maxRetries": m.MaxRetries, "digitTimeout": m.DigitTimeout,
			// The direct dial setting is a badge, not a port.
			"directDial": m.AllowDirectDial,
		}, m)
	}
	for _, q := range in.Queues {
		id := nid(KindQueue, q.ID)
		b.queueByName[q.Name] = id
		if q.Number != "" {
			b.entries[id] = true
		}
		if len(q.Members) > 0 {
			b.ends[id] = true // an agent answers
		}
		b.node(id, KindQueue, q.Name, map[string]any{
			"number": q.Number, "enabled": q.Enabled, "strategy": q.Strategy, "agents": len(q.Members),
			"maxWait": q.MaxWait, "maxCallers": q.MaxCallers,
		}, q)
	}
	for _, t := range in.TimeConditions {
		id := nid(KindTimeCond, t.ID)
		b.tcByName[t.Name] = id
		sum := map[string]any{
			"schedule": t.ScheduleName, "override": t.Override, "forced": t.Override != "" && t.Override != "auto",
			"toggleNumber": t.ToggleNumber,
		}
		if t.OverrideAt != nil {
			sum["overrideAt"] = t.OverrideAt
		}
		b.node(id, KindTimeCond, t.Name, sum, t)
	}
	for _, s := range in.Schedules {
		id := nid(KindSchedule, s.ID)
		b.schedByID[s.ID] = id
		b.node(id, KindSchedule, s.Name, map[string]any{
			"timeZone": s.TimeZone, "ranges": len(s.Ranges), "calendars": len(s.CalendarIDs),
		}, s)
	}
	for _, c := range in.Calendars {
		b.node(nid(KindHolidayCal, c.ID), KindHolidayCal, c.Name, map[string]any{
			"entries": len(c.Entries), "schedules": len(c.Schedules),
		}, c)
	}
	for _, m := range in.MOHClasses {
		id := nid(KindMOHClass, m.ID)
		b.mohByID[m.ID] = id
		b.node(id, KindMOHClass, m.Name, map[string]any{
			"mode": m.Mode, "isDefault": m.IsDefault, "tracks": len(m.Tracks),
		}, m)
	}
	for _, l := range in.ParkLots {
		b.node(nid(KindParkLot, l.ID), KindParkLot, l.Name, map[string]any{
			"slotStart": l.SlotStart, "slotEnd": l.SlotEnd, "enabled": l.Enabled,
			"returnSecs": l.ReturnSecs, "voicemailSecs": l.VoicemailSecs,
		}, l)
	}
	for _, g := range in.PickupGroups {
		b.node(nid(KindPickupGroup, g.ID), KindPickupGroup, g.Name, map[string]any{"members": len(g.Members)}, g)
	}
	for _, g := range in.VoicemailGroups {
		id := nid(KindVMGroup, g.ID)
		b.vmGroupByNumber[g.Number] = id
		b.entries[id] = true // dialled by its number
		b.ends[id] = true    // the message is left in the members' boxes
		b.node(id, KindVMGroup, g.Name, map[string]any{
			"number": g.Number, "enabled": g.Enabled, "members": len(g.Members), "maxSeconds": g.MaxSeconds,
		}, g)
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
	b.ends[id] = true
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
// (spec S-5). Extension forwards use it.
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

// resolve is the one place a stored (kind, target) destination becomes a
// node input. It follows hello-sip's lookups: a ring group by name, an
// extension or voicemail box by number, an announcement, IVR menu, queue or
// time condition by name, a paging group by code. A target that names
// nothing becomes a missing node and a dangling_reference warning on owner.
// The "extension" kind of an inbound route also takes a ring group name,
// the older way of routing to a numeric group (resolveInbound).
func (b *builder) resolve(owner, kind, target string) Ref {
	lookup := func(m map[string]string, port, what string) Ref {
		if id, ok := m[target]; ok {
			return Ref{id, port}
		}
		return Ref{b.missing(owner, target, fmt.Sprintf("%s %q does not exist", what, target)), "call"}
	}
	switch kind {
	case "extension":
		if id, ok := b.extByNumber[target]; ok {
			return Ref{id, "ring"}
		}
		return Ref{b.missing(owner, target, fmt.Sprintf("extension %q does not exist", target)), "call"}
	case "voicemail":
		id, ok := b.extByNumber[target]
		if !ok {
			return Ref{b.missing(owner, target, fmt.Sprintf("voicemail box %q is not an extension", target)), "call"}
		}
		if !b.exts[id].VoicemailEnabled {
			b.warn(owner, WarnVoicemailDisable, fmt.Sprintf("voicemail is off for extension %s", target))
		}
		return Ref{id, "voicemail"}
	case "voicemail_group":
		return lookup(b.vmGroupByNumber, "call", "voicemail group")
	case "ring_group":
		return lookup(b.groupByName, "ring", "ring group")
	case "announcement":
		return lookup(b.annByName, "call", "announcement")
	case "paging":
		return lookup(b.pagingByCode, "call", "paging group")
	case "ivr":
		return lookup(b.ivrByName, "call", "IVR menu")
	case "queue":
		return lookup(b.queueByName, "call", "queue")
	case "time_condition":
		return lookup(b.tcByName, "call", "time condition")
	case "conference":
		return lookup(b.confByNumber, "call", "conference room")
	case "external":
		return Ref{b.external(target), "call"}
	case "sip_uri":
		id := KindSIPURI + ":" + target
		b.node(id, KindSIPURI, target, map[string]any{"uri": target}, nil)
		b.ends[id] = true
		return Ref{id, "call"}
	}
	return Ref{b.missing(owner, target, fmt.Sprintf("unknown destination kind %q", kind)), "call"}
}

// dest draws the edge of a stored destination from src. A kind that stores
// no destination (hangup, none) draws nothing and marks the node as one a
// call can end at.
func (b *builder) dest(src Ref, owner Owner, kind, target string, data map[string]any) {
	if NoDestination[kind] {
		b.ends[src.Node] = true
		return
	}
	b.edge(src, b.resolve(owner.Node, kind, target), owner, false, data)
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
		b.useMOH(rid, r.MOHClassID)
		src := Ref{rid, "destination"}
		owner := Owner{rid, "destinationKind+destination"}
		switch r.DestinationKind {
		case "extension":
			// A numeric ring group is dialled by name, and a group is looked
			// up before an extension.
			if id, ok := b.groupByName[r.Destination]; ok {
				b.edge(src, Ref{id, "ring"}, owner, false, nil)
			} else if id, ok := b.extByNumber[r.Destination]; ok {
				b.edge(src, Ref{id, "ring"}, owner, false, nil)
			} else {
				dst := b.missing(rid, r.Destination, fmt.Sprintf("destination %q is not an extension or ring group", r.Destination))
				b.edge(src, Ref{dst, "call"}, owner, false, nil)
			}
		case "internal":
			id := nid(KindInternalDID, r.ID)
			b.node(id, KindInternalDID, "DID "+r.DID+" as extension", map[string]any{"did": r.DID}, nil)
			b.ends[id] = true
			b.edge(src, Ref{id, "call"}, owner, false, nil)
		case "conference", "external", "sip_uri", "ring_group", "voicemail", "voicemail_group", "announcement", "paging", "ivr", "queue", "time_condition":
			b.dest(src, owner, r.DestinationKind, r.Destination, nil)
		}
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
		b.entries[KindAnyTrunk+":any"] = true
		b.node(KindAnyTrunk+":any", KindAnyTrunk, "Any trunk", nil, nil)
	}
	if dial {
		b.entries[KindDialPlan+":internal"] = true
		b.node(KindDialPlan+":internal", KindDialPlan, "Internal callers", nil, nil)
	}
	b.shadowedRoutes(in)
}

// useMOH draws the hold-music edge of an object that names a class.
func (b *builder) useMOH(node string, class *int64) {
	if class == nil {
		return
	}
	cid, ok := b.mohByID[*class]
	if !ok {
		b.warn(node, WarnDangling, fmt.Sprintf("hold music class %d does not exist", *class))
		return
	}
	b.edge(Ref{cid, "use"}, Ref{node, "holdMusic"}, Owner{node, "mohClassId"}, false, nil)
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
		b.useMOH(id, e.MOHClassID)
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
		b.useMOH(id, g.MOHClassID)
		b.dest(Ref{id, "noAnswer"}, Owner{id, "failureKind+failureTarget"}, g.FailureKind, g.FailureTarget, nil)
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
		if p.MulticastChannelID != nil {
			if mc, ok := b.mcByID[*p.MulticastChannelID]; ok {
				b.edge(Ref{id, "multicast"}, Ref{mc, "page"}, Owner{id, "multicastChannelId"}, false, nil)
			} else {
				b.warn(id, WarnDangling, fmt.Sprintf("multicast channel %d does not exist", *p.MulticastChannelID))
			}
		}
	}
	for _, g := range in.PickupGroups {
		id := nid(KindPickupGroup, g.ID)
		for _, m := range g.Members {
			b.member(m.ExtensionID, id, "members", id, "members", nil)
		}
	}
	for _, g := range in.VoicemailGroups {
		id := nid(KindVMGroup, g.ID)
		for _, m := range g.Members {
			b.member(m.ExtensionID, id, "members", id, "members", nil)
		}
	}
}

// prompt draws the edge from an announcement to a prompt input.
func (b *builder) prompt(ann string, dstNode, dstPort, field string) {
	if ann == "" {
		return
	}
	if !b.nodes[ann] {
		b.warn(dstNode, WarnDangling, fmt.Sprintf("the announcement of %s does not exist", dstPort))
		return
	}
	b.edge(Ref{ann, "audio"}, Ref{dstNode, dstPort}, Owner{dstNode, field}, false, nil)
}

func (b *builder) promptID(id *int64, dstNode, dstPort, field string) {
	if id == nil {
		return
	}
	b.prompt(nid(KindAnnouncement, *id), dstNode, dstPort, field)
}

func (b *builder) addIVR(in Input) {
	for _, m := range in.IVRMenus {
		id := nid(KindIVR, m.ID)
		greet := nid(KindAnnouncement, m.GreetingID)
		if m.GreetingID == 0 || !b.nodes[greet] {
			b.warn(id, WarnNoGreeting, "the menu has no greeting announcement")
		} else {
			if a := in.announcement(m.GreetingID); a != nil && a.Object == "" {
				b.warn(id, WarnNoGreeting, fmt.Sprintf("the greeting %q has no audio", a.Name))
			}
			b.prompt(greet, id, "greeting", "greetingId")
		}
		b.promptID(m.InvalidPromptID, id, "invalidPrompt", "invalidPromptId")
		b.promptID(m.TimeoutPromptID, id, "timeoutPrompt", "timeoutPromptId")
		for _, o := range m.Options {
			b.dest(Ref{id, "key:" + o.Digit}, Owner{id, "options"}, o.Kind, o.Target, map[string]any{"digit": o.Digit})
		}
		b.dest(Ref{id, "timeout"}, Owner{id, "timeout"}, m.Timeout.Kind, m.Timeout.Target, nil)
		b.dest(Ref{id, "invalid"}, Owner{id, "invalid"}, m.Invalid.Kind, m.Invalid.Target, nil)
	}
}

func (in Input) announcement(id int64) *store.Announcement {
	for i := range in.Announcements {
		if in.Announcements[i].ID == id {
			return &in.Announcements[i]
		}
	}
	return nil
}

func (b *builder) addQueues(in Input) {
	for _, q := range in.Queues {
		id := nid(KindQueue, q.ID)
		if len(q.Members) == 0 {
			b.warn(id, WarnEmptyGroup, "the queue has no agents")
		}
		for _, m := range q.Members {
			b.member(m.ExtensionID, id, "members", id, "members",
				map[string]any{"position": m.Position, "penalty": m.Penalty, "dynamic": m.Dynamic})
		}
		b.useMOH(id, q.MOHClassID)
		b.promptID(q.PeriodicPromptID, id, "periodicPrompt", "periodicPromptId")
		b.promptID(q.PositionPromptID, id, "positionPrompt", "positionPromptId")
		b.promptID(q.WaitPromptID, id, "waitPrompt", "waitPromptId")
		b.promptID(q.MinutesPromptID, id, "minutesPrompt", "minutesPromptId")
		b.promptID(q.BreakoutPromptID, id, "breakoutPrompt", "breakoutPromptId")
		b.dest(Ref{id, "timeout"}, Owner{id, "timeout"}, q.Timeout.Kind, q.Timeout.Target, nil)
		b.dest(Ref{id, "full"}, Owner{id, "full"}, q.Full.Kind, q.Full.Target, nil)
		b.dest(Ref{id, "noAgents"}, Owner{id, "noAgents"}, q.NoAgents.Kind, q.NoAgents.Target, nil)
		if q.BreakoutVoicemail != "" {
			ref := b.resolve(id, "voicemail", q.BreakoutVoicemail)
			b.edge(Ref{id, "breakout"}, ref, Owner{id, "breakoutVoicemail"}, false, nil)
		}
	}
}

func (b *builder) addTimeConditions(in Input) {
	for _, t := range in.TimeConditions {
		id := nid(KindTimeCond, t.ID)
		if s, ok := b.schedByID[t.ScheduleID]; ok {
			b.edge(Ref{s, "use"}, Ref{id, "schedule"}, Owner{id, "scheduleId"}, false, nil)
		} else {
			b.warn(id, WarnDangling, fmt.Sprintf("schedule %d does not exist", t.ScheduleID))
		}
		b.tcDest(id, "open", Owner{id, "open"}, t.Open.Kind, t.Open.Target, t.Open.ThenVoicemail)
		b.tcDest(id, "closed", Owner{id, "closed"}, t.Closed.Kind, t.Closed.Target, t.Closed.ThenVoicemail)
		// With no holiday branch the closed destination serves holidays.
		if t.Holiday != nil && t.Holiday.Kind != "" {
			b.tcDest(id, "holiday", Owner{id, "holiday"}, t.Holiday.Kind, t.Holiday.Target, t.Holiday.ThenVoicemail)
		}
	}
}

// tcDest draws a time condition branch; an announcement branch may go on
// to a voicemail box, kept on the edge.
func (b *builder) tcDest(node, port string, owner Owner, kind, target, then string) {
	var data map[string]any
	if then != "" {
		data = map[string]any{"thenVoicemail": then}
	}
	b.dest(Ref{node, port}, owner, kind, target, data)
}

// addResources draws the edges of the library objects: calendars to the
// schedules that use them, announcements to the hold music classes that play
// them, announcements to the conference rooms that use them.
func (b *builder) addResources(in Input) {
	for _, s := range in.Schedules {
		sid := nid(KindSchedule, s.ID)
		for _, cid := range s.CalendarIDs {
			c := nid(KindHolidayCal, cid)
			if !b.nodes[c] {
				b.warn(sid, WarnDangling, fmt.Sprintf("calendar %d does not exist", cid))
				continue
			}
			b.edge(Ref{c, "use"}, Ref{sid, "calendars"}, Owner{sid, "calendarIds"}, false, nil)
		}
	}
	for _, m := range in.MOHClasses {
		mid := nid(KindMOHClass, m.ID)
		for i, t := range m.Tracks {
			a := nid(KindAnnouncement, t.AnnouncementID)
			if !b.nodes[a] {
				b.warn(mid, WarnDangling, fmt.Sprintf("track announcement %d does not exist", t.AnnouncementID))
				continue
			}
			b.edge(Ref{a, "audio"}, Ref{mid, "tracks"}, Owner{mid, "announcementIds"}, false, map[string]any{"position": i + 1})
		}
	}
	for _, c := range in.Conferences {
		cid := nid(KindConference, c.ID)
		for _, p := range []struct{ name, port string }{
			{c.EntryAnnouncement, "entryAnnouncement"}, {c.ExitAnnouncement, "exitAnnouncement"},
		} {
			if p.name == "" {
				continue
			}
			a, ok := b.annByName[p.name]
			if !ok {
				b.warn(cid, WarnDangling, fmt.Sprintf("announcement %q does not exist", p.name))
				continue
			}
			b.edge(Ref{a, "audio"}, Ref{cid, p.port}, Owner{cid, p.port}, false, nil)
		}
	}
}

func (b *builder) addFeatureCodes(in Input) {
	for _, c := range in.FeatureCodes {
		kind, ok := FeatureCodeTargets[c.Action]
		if !ok {
			continue // the other actions have no call target
		}
		id := KindFeatureCode + ":" + c.Code
		b.entries[id] = true
		b.node(id, KindFeatureCode, c.Code, map[string]any{"code": c.Code, "action": c.Action}, c)
		if c.Argument == "" {
			continue // the name is dialled after the code
		}
		b.dest(Ref{id, "target"}, Owner{id, "argument"}, kind, c.Argument, nil)
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
