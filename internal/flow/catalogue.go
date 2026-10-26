// Package flow is the read-only model behind the flow canvas (spec
// flow-canvas): a catalogue of node and port types, and a graph built from
// the stored configuration. The canvas is a view: nothing here is stored.
package flow

import "sort"

// PortType decides a port's colour and shape in the UI and which ports can
// be joined: an edge always joins an output to an input.
type PortType string

// Port types (spec S-2).
const (
	PortCall      PortType = "call"      // a live call handed on
	PortVoicemail PortType = "voicemail" // leave a message in a box
	PortCarrier   PortType = "carrier"   // trunk-side routing
	PortMember    PortType = "member"    // an extension belongs to a group
	PortPrompt    PortType = "prompt"    // an audio prompt used by a node
	PortSchedule  PortType = "schedule"  // a schedule used by a node
	PortHoldMusic PortType = "holdMusic" // an MOH class used by a node
	PortDevice    PortType = "device"    // device/phone binding
)

// Direction of a port.
type Direction string

// Port directions.
const (
	In  Direction = "in"
	Out Direction = "out"
)

// Node kinds.
const (
	KindTrunk         = "trunk"
	KindAnyTrunk      = "anyTrunk"
	KindInboundRoute  = "inboundRoute"
	KindDialPlan      = "dialPlan"
	KindOutboundRoute = "outboundRoute"
	KindExtension     = "extension"
	KindDevice        = "device"
	KindPhone         = "phone"
	KindRingGroup     = "ringGroup"
	KindIVR           = "ivr"
	KindAnnouncement  = "announcement"
	KindPagingGroup   = "pagingGroup"
	KindConference    = "conference"
	KindFeatureCode   = "featureCode"
	KindTimeCond      = "timeCondition"
	KindSchedule      = "schedule"
	KindHolidayCal    = "holidayCalendar"
	KindMOHClass      = "mohClass"
	KindExternal      = "external"
	KindSIPURI        = "sipUri"
	KindInternalDID   = "internalDid"
	KindMissing       = "missing"
)

// Requires names what must exist before a node kind or a connection works.
// Empty means it works on the stored configuration today.
const (
	// ReqDestinations is the unified destination kinds migration (spec S-3,
	// decision 3): inbound routes and ring-group fallback accept every call
	// target kind.
	ReqDestinations = "destination-unification"
	ReqIVR          = "ivr"
	ReqTimeCond     = "time-conditions"
	ReqMOH          = "music-on-hold"
)

// Port is one connector dot on a node.
type Port struct {
	ID        string    `json:"id"`
	Direction Direction `json:"direction"`
	Type      PortType  `json:"type"`
	// Many is true when several edges may use the port.
	Many  bool   `json:"many"`
	Label string `json:"label"`
	// Field is the stored field an edge on this port is kept in (its
	// owner, spec S-4); empty for implicit edges.
	Field string `json:"field,omitempty"`
	// Targets lists, for an output, every input it may connect to.
	Targets []Target `json:"targets,omitempty"`
}

// Target is one input an output may connect to.
type Target struct {
	Kind string `json:"kind"`
	Port string `json:"port"`
	// Requires is non-empty when the connection cannot be stored yet.
	Requires string `json:"requires,omitempty"`
	// Note explains a restriction, shown as the tooltip.
	Note string `json:"note,omitempty"`
}

// NodeType is one kind of node on the canvas.
type NodeType struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Pseudo nodes stand for something that is not a stored object.
	Pseudo bool `json:"pseudo,omitempty"`
	// Terminal nodes are created from a target string.
	Terminal bool `json:"terminal,omitempty"`
	// Requires is non-empty when the object is not stored yet.
	Requires string `json:"requires,omitempty"`
	Ports    []Port `json:"ports"`
}

// PortStyle is a port type's presentation.
type PortStyle struct {
	Type   PortType `json:"type"`
	Label  string   `json:"label"`
	Colour string   `json:"colour"`
	Shape  string   `json:"shape"`
}

// Catalogue is the single table the UI, the batch validator and the AI read
// (spec S-1).
type Catalogue struct {
	PortTypes []PortStyle `json:"portTypes"`
	Nodes     []NodeType  `json:"nodes"`
}

func in(id string, t PortType, many bool, label string) Port {
	return Port{ID: id, Direction: In, Type: t, Many: many, Label: label}
}

func out(id string, t PortType, many bool, label, field string, targets ...Target) Port {
	return Port{ID: id, Direction: Out, Type: t, Many: many, Label: label, Field: field, Targets: targets}
}

func tg(kind, port string) Target { return Target{Kind: kind, Port: port} }

func (t Target) need(req string) Target { t.Requires = req; return t }

func (t Target) note(n string) Target { t.Note = n; return t }

// callTargets are the call inputs an unrestricted "call goes to" output may
// reach (spec S-3, decision 3). The ones the stored kinds cannot express yet
// carry what they wait for; keep needs empty for the kinds an output stores
// today.
func callTargets(kinds map[string]string) []Target {
	all := []Target{
		tg(KindExtension, "ring"),
		tg(KindExtension, "voicemail"),
		tg(KindRingGroup, "ring"),
		tg(KindIVR, "call").need(ReqIVR),
		tg(KindAnnouncement, "call"),
		tg(KindPagingGroup, "call"),
		tg(KindConference, "call"),
		tg(KindTimeCond, "call").need(ReqTimeCond),
		tg(KindExternal, "call"),
		tg(KindSIPURI, "call"),
		tg(KindInternalDID, "call"),
	}
	var out []Target
	for _, t := range all {
		req, ok := kinds[t.Kind+"."+t.Port]
		if !ok {
			continue
		}
		if req != "" {
			t = t.need(req)
		}
		out = append(out, t)
	}
	return out
}

// buildCatalogue assembles the table. It is a function (not a variable) so
// every caller gets its own copy.
func buildCatalogue() Catalogue {
	const (
		ungated = ""
		unify   = ReqDestinations
	)
	// Inbound routes store extension|external|sip_uri|internal today; the
	// rest need the unification migration (and the branches that add them).
	inboundDest := callTargets(map[string]string{
		"extension.ring":      ungated,
		"extension.voicemail": unify,
		"ringGroup.ring":      ungated, // by numeric group name, kind "extension"
		"ivr.call":            ReqIVR,
		"announcement.call":   unify,
		"pagingGroup.call":    unify,
		"conference.call":     ungated,
		"timeCondition.call":  ReqTimeCond,
		"external.call":       ungated,
		"sipUri.call":         ungated,
		"internalDid.call":    ungated,
	})
	for i := range inboundDest {
		if inboundDest[i].Kind == KindRingGroup {
			inboundDest[i].Note = "Ring group names must be numeric to be dialled from an inbound route"
		}
	}
	// Extension forwards store a number: an extension, a numeric ring group
	// or an external number.
	forward := []Target{
		tg(KindExtension, "ring"),
		tg(KindRingGroup, "ring").note("Ring group names must be numeric to be dialled from a forward"),
		tg(KindIVR, "call").need(ReqIVR).note("The menu needs a number"),
		tg(KindExternal, "call"),
	}
	// Ring-group fallback stores none|voicemail|external|announcement today.
	groupNoAnswer := callTargets(map[string]string{
		"extension.ring":      unify,
		"extension.voicemail": ungated,
		"ringGroup.ring":      unify,
		"ivr.call":            ReqIVR,
		"announcement.call":   ungated,
		"pagingGroup.call":    unify,
		"timeCondition.call":  ReqTimeCond,
		"external.call":       ungated,
	})
	// IVR keys and exits reach every call kind but a SIP URI or a DID.
	ivrExit := callTargets(map[string]string{
		"extension.ring":      ReqIVR,
		"extension.voicemail": ReqIVR,
		"ringGroup.ring":      ReqIVR,
		"ivr.call":            ReqIVR,
		"announcement.call":   ReqIVR,
		"pagingGroup.call":    ReqIVR,
		"conference.call":     ReqIVR,
		"timeCondition.call":  ReqTimeCond,
		"external.call":       ReqIVR,
	})
	tcExit := callTargets(map[string]string{
		"extension.ring":      ReqTimeCond,
		"extension.voicemail": ReqTimeCond,
		"ringGroup.ring":      ReqTimeCond,
		"ivr.call":            ReqTimeCond,
		"announcement.call":   ReqTimeCond,
		"pagingGroup.call":    ReqTimeCond,
		"conference.call":     ReqTimeCond,
		"timeCondition.call":  ReqTimeCond,
		"external.call":       ReqTimeCond,
	})

	c := Catalogue{
		PortTypes: []PortStyle{
			{PortCall, "Call", "#2563EB", "circle"},
			{PortVoicemail, "Voicemail", "#7C3AED", "circle-notch"},
			{PortCarrier, "Carrier", "#EA580C", "square"},
			{PortMember, "Member", "#16A34A", "diamond"},
			{PortPrompt, "Prompt", "#CA8A04", "triangle"},
			{PortSchedule, "Schedule", "#0D9488", "hexagon"},
			{PortHoldMusic, "Hold music", "#DB2777", "triangle"},
			{PortDevice, "Device", "#64748B", "small-square"},
		},
		Nodes: []NodeType{
			{Kind: KindTrunk, Label: "Trunk", Ports: []Port{
				in("carrier", PortCarrier, true, "Outbound routes"),
				out("inbound", PortCarrier, true, "Inbound routes", "trunkId", tg(KindInboundRoute, "trunk")),
			}},
			{Kind: KindAnyTrunk, Label: "Any trunk", Pseudo: true, Ports: []Port{
				out("inbound", PortCarrier, true, "Inbound routes", "trunkId", tg(KindInboundRoute, "trunk")),
			}},
			{Kind: KindInboundRoute, Label: "Inbound route", Ports: []Port{
				in("trunk", PortCarrier, false, "Trunk"),
				out("destination", PortCall, false, "Destination", "destinationKind+destination", inboundDest...),
			}},
			{Kind: KindDialPlan, Label: "Internal callers", Pseudo: true, Ports: []Port{
				out("dial", PortCarrier, true, "Outbound routes", "", tg(KindOutboundRoute, "dial")),
			}},
			{Kind: KindOutboundRoute, Label: "Outbound route", Ports: []Port{
				in("dial", PortCarrier, false, "Dialled by"),
				out("trunks", PortCarrier, true, "Trunks (in try order)", "trunks", tg(KindTrunk, "carrier")),
			}},
			{Kind: KindExtension, Label: "Extension", Ports: []Port{
				in("ring", PortCall, true, "Ring"),
				in("voicemail", PortVoicemail, true, "Voicemail"),
				in("devices", PortDevice, true, "Devices"),
				out("forwardAlways", PortCall, false, "Always forward", "forwardAlways", forward...),
				out("forwardBusy", PortCall, false, "Forward when busy", "forwardBusy", forward...),
				out("forwardNoAnswer", PortCall, false, "Forward on no answer", "forwardNoAnswer", forward...),
				out("member", PortMember, true, "Member of", "members",
					tg(KindRingGroup, "members"), tg(KindPagingGroup, "members"), tg(KindPagingGroup, "callers")),
			}},
			{Kind: KindDevice, Label: "Device", Ports: []Port{
				in("phone", PortDevice, false, "Phone"),
				out("extension", PortDevice, false, "Extension", "extensionId", tg(KindExtension, "devices")),
			}},
			{Kind: KindPhone, Label: "Phone", Ports: []Port{
				out("device", PortDevice, false, "Device", "deviceId", tg(KindDevice, "phone")),
			}},
			{Kind: KindRingGroup, Label: "Ring group", Ports: []Port{
				in("ring", PortCall, true, "Ring"),
				in("members", PortMember, true, "Members"),
				out("noAnswer", PortCall, false, "No answer", "failureKind+failureTarget", groupNoAnswer...),
			}},
			{Kind: KindIVR, Label: "IVR menu", Requires: ReqIVR, Ports: []Port{
				in("call", PortCall, true, "Call"),
				in("greeting", PortPrompt, false, "Greeting"),
				in("invalidPrompt", PortPrompt, false, "Invalid prompt"),
				in("timeoutPrompt", PortPrompt, false, "Timeout prompt"),
				out("key", PortCall, false, "Key (one port per configured key)", "options", ivrExit...),
				out("timeout", PortCall, false, "Timeout", "timeout", ivrExit...),
				out("invalid", PortCall, false, "Invalid", "invalid", ivrExit...),
			}},
			{Kind: KindAnnouncement, Label: "Announcement", Ports: []Port{
				in("call", PortCall, true, "Play, then hang up"),
				out("audio", PortPrompt, true, "Used as prompt", "", tg(KindIVR, "greeting").need(ReqIVR),
					tg(KindIVR, "invalidPrompt").need(ReqIVR), tg(KindIVR, "timeoutPrompt").need(ReqIVR)),
			}},
			{Kind: KindPagingGroup, Label: "Paging group", Ports: []Port{
				in("call", PortCall, true, "Page"),
				in("members", PortMember, true, "Members"),
				in("callers", PortMember, true, "Allowed callers"),
			}},
			{Kind: KindConference, Label: "Conference room", Terminal: true, Ports: []Port{in("call", PortCall, true, "Join the room")}},
			{Kind: KindFeatureCode, Label: "Feature code", Ports: []Port{
				out("target", PortCall, false, "Target", "argument", tg(KindAnnouncement, "call"),
					tg(KindIVR, "call").need(ReqIVR), tg(KindPagingGroup, "call").need(unify)),
			}},
			{Kind: KindTimeCond, Label: "Time condition", Requires: ReqTimeCond, Ports: []Port{
				in("call", PortCall, true, "Call"),
				in("schedule", PortSchedule, false, "Schedule"),
				in("holidays", PortSchedule, false, "Holidays"),
				out("open", PortCall, false, "Open", "open", tcExit...),
				out("closed", PortCall, false, "Closed", "closed", tcExit...),
				out("holiday", PortCall, false, "Holiday", "holiday", tcExit...),
			}},
			{Kind: KindSchedule, Label: "Schedule", Requires: ReqTimeCond, Ports: []Port{
				out("use", PortSchedule, true, "Used by", "", tg(KindTimeCond, "schedule"), tg(KindTimeCond, "holidays")),
			}},
			{Kind: KindHolidayCal, Label: "Holiday calendar", Requires: ReqTimeCond, Ports: []Port{
				out("use", PortSchedule, true, "Used by", "", tg(KindTimeCond, "holidays")),
			}},
			{Kind: KindMOHClass, Label: "Hold music class", Requires: ReqMOH, Ports: []Port{
				out("use", PortHoldMusic, true, "Used by", ""),
			}},
			{Kind: KindExternal, Label: "External number", Terminal: true, Ports: []Port{in("call", PortCall, true, "Call")}},
			{Kind: KindSIPURI, Label: "SIP URI", Terminal: true, Ports: []Port{in("call", PortCall, true, "Call")}},
			{Kind: KindInternalDID, Label: "DID as extension", Terminal: true, Ports: []Port{in("call", PortCall, true, "Call")}},
			{Kind: KindMissing, Label: "Missing target", Terminal: true, Ports: []Port{in("call", PortCall, true, "Call")}},
		},
	}
	return c
}

// Get returns a copy of the catalogue.
func Get() Catalogue { return buildCatalogue() }

// Node returns the type of kind.
func (c Catalogue) Node(kind string) (NodeType, bool) {
	for _, n := range c.Nodes {
		if n.Kind == kind {
			return n, true
		}
	}
	return NodeType{}, false
}

// Port returns the port of a node kind.
func (c Catalogue) Port(kind, port string) (Port, bool) {
	n, ok := c.Node(kind)
	if !ok {
		return Port{}, false
	}
	for _, p := range n.Ports {
		if p.ID == port {
			return p, true
		}
	}
	return Port{}, false
}

// CanConnect reports whether an output may connect to an input and, when it
// may not or cannot be stored yet, why. A connection that is allowed but
// waits on something returns ok true and the requirement.
func (c Catalogue) CanConnect(srcKind, srcPort, dstKind, dstPort string) (ok bool, requires string, reason string) {
	src, found := c.Port(srcKind, srcPort)
	if !found || src.Direction != Out {
		return false, "", "not an output"
	}
	dst, found := c.Port(dstKind, dstPort)
	if !found || dst.Direction != In {
		return false, "", "not an input"
	}
	for _, t := range src.Targets {
		if t.Kind == dstKind && t.Port == dstPort {
			return true, t.Requires, t.Note
		}
	}
	return false, "", "this output cannot connect to " + dstKind + " " + dstPort
}

// Kinds lists every node kind, sorted.
func (c Catalogue) Kinds() []string {
	out := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		out = append(out, n.Kind)
	}
	sort.Strings(out)
	return out
}
