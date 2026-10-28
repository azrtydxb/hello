// Package flow is the read-only model behind the flow canvas (spec
// flow-canvas): a catalogue of node and port types, and a graph built from
// the stored configuration. The canvas is a view: nothing here is stored.
package flow

import (
	"sort"
	"strings"
)

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
	KindQueue         = "queue"
	KindAnnouncement  = "announcement"
	KindPagingGroup   = "pagingGroup"
	KindMulticast     = "multicastChannel"
	KindConference    = "conference"
	KindParkLot       = "parkLot"
	KindPickupGroup   = "pickupGroup"
	KindVMGroup       = "voicemailGroup"
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

// Port is one connector dot on a node.
type Port struct {
	ID        string    `json:"id"`
	Direction Direction `json:"direction"`
	Type      PortType  `json:"type"`
	// Many is true when several edges may use the port.
	Many  bool   `json:"many"`
	Label string `json:"label"`
	// PerKey marks a port that is instantiated once per configured key: an
	// edge uses the port id "<id>:<key>" (an IVR's "key:5"). The catalogue
	// lists it once under its bare id.
	PerKey bool `json:"perKey,omitempty"`
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
	// Requires is non-empty when the connection cannot be stored yet. It
	// must name a feature in pendingFeatures; TestNoStaleRequires fails
	// when that feature has shipped.
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
	// Requires is non-empty when the object is not stored yet (see
	// Target.Requires).
	Requires string `json:"requires,omitempty"`
	Ports    []Port `json:"ports"`
}

// pendingFeatures names the features a catalogue entry may wait for, each
// with an OpenAPI path that exists once the feature has shipped. It is empty
// when everything the catalogue describes is on main. A new node type for a
// feature still in a branch adds its marker here and sets Requires on its
// node and targets; the guard fails when the path appears in the API.
var pendingFeatures = map[string]string{}

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

func perKey(p Port) Port { p.PerKey = true; return p }

func tg(kind, port string) Target { return Target{Kind: kind, Port: port} }

func (t Target) note(n string) Target { t.Note = n; return t }

// Call targets of the outputs that store a (kind, target) pair. Each output
// lists the inputs whose stored kind its CHECK constraint accepts (see
// StoredKinds); TestCatalogueDrift keeps the two in step.
var (
	tExt       = tg(KindExtension, "ring")
	tVoicemail = tg(KindExtension, "voicemail")
	tVMGroup   = tg(KindVMGroup, "call")
	tGroup     = tg(KindRingGroup, "ring")
	tIVR       = tg(KindIVR, "call")
	tQueue     = tg(KindQueue, "call")
	tAnn       = tg(KindAnnouncement, "call")
	tPaging    = tg(KindPagingGroup, "call")
	tConf      = tg(KindConference, "call")
	tTC        = tg(KindTimeCond, "call")
	tExternal  = tg(KindExternal, "call")
	tSIPURI    = tg(KindSIPURI, "call")
	tDID       = tg(KindInternalDID, "call")
)

// buildCatalogue assembles the table. It is a function (not a variable) so
// every caller gets its own copy.
func buildCatalogue() Catalogue {
	// Inbound routes store every kind. An "extension" route may also name a
	// numeric ring group (the older way); a "ring_group" route names it
	// directly.
	inboundDest := []Target{tExt, tVoicemail, tVMGroup, tGroup, tIVR, tQueue, tAnn, tPaging, tConf, tTC, tExternal, tSIPURI, tDID}
	// Extension forwards store a number: an extension, a numeric ring group
	// or an external number.
	forward := []Target{tExt, tGroup.note("Ring group names must be numeric to be dialled from a forward"), tExternal}
	// Ring-group fallback stores none|voicemail|external|announcement|ivr|
	// queue|time_condition|extension|ring_group. An extension or another
	// group gets a new ring stage. A call whose group already rang cannot be
	// handed to a paging group or a conference room.
	groupNoAnswer := []Target{tExt, tVoicemail, tGroup, tIVR, tQueue, tAnn, tTC, tExternal}
	// IVR keys and exits, time condition branches and queue exits store the
	// same set: hangup, extension, ring group, voicemail, announcement,
	// paging, IVR, queue, time condition, voicemail group and external number.
	exit := []Target{tExt, tVoicemail, tVMGroup, tGroup, tIVR, tQueue, tAnn, tPaging, tTC, tExternal}

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
				in("holdMusic", PortHoldMusic, false, "Hold music"),
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
				in("holdMusic", PortHoldMusic, false, "Hold music"),
				out("forwardAlways", PortCall, false, "Always forward", "forwardAlways", forward...),
				out("forwardBusy", PortCall, false, "Forward when busy", "forwardBusy", forward...),
				out("forwardNoAnswer", PortCall, false, "Forward on no answer", "forwardNoAnswer", forward...),
				out("member", PortMember, true, "Member of", "members",
					tg(KindRingGroup, "members"), tg(KindPagingGroup, "members"), tg(KindPagingGroup, "callers"),
					tg(KindQueue, "members"), tg(KindPickupGroup, "members"), tg(KindVMGroup, "members")),
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
				in("holdMusic", PortHoldMusic, false, "Hold music"),
				out("noAnswer", PortCall, false, "No answer", "failureKind+failureTarget", groupNoAnswer...),
			}},
			{Kind: KindIVR, Label: "IVR menu", Ports: []Port{
				in("call", PortCall, true, "Call"),
				in("greeting", PortPrompt, false, "Greeting"),
				in("invalidPrompt", PortPrompt, false, "Invalid prompt"),
				in("timeoutPrompt", PortPrompt, false, "Timeout prompt"),
				perKey(out("key", PortCall, false, "Key (one port per configured key)", "options", exit...)),
				out("timeout", PortCall, false, "Timeout", "timeout", exit...),
				out("invalid", PortCall, false, "Invalid", "invalid", exit...),
			}},
			{Kind: KindQueue, Label: "Call queue", Ports: []Port{
				in("call", PortCall, true, "Call"),
				in("members", PortMember, true, "Agents"),
				in("holdMusic", PortHoldMusic, false, "Hold music"),
				in("periodicPrompt", PortPrompt, false, "Periodic announcement"),
				in("positionPrompt", PortPrompt, false, "Position prompt"),
				in("waitPrompt", PortPrompt, false, "Wait prompt"),
				in("minutesPrompt", PortPrompt, false, "Minutes prompt"),
				in("breakoutPrompt", PortPrompt, false, "Voicemail breakout prompt"),
				out("timeout", PortCall, false, "Wait too long", "timeout", exit...),
				out("full", PortCall, false, "Queue full", "full", exit...),
				out("noAgents", PortCall, false, "No agents", "noAgents", exit...),
				out("breakout", PortVoicemail, false, "Breakout voicemail", "breakoutVoicemail", tVoicemail),
			}},
			{Kind: KindAnnouncement, Label: "Announcement", Ports: []Port{
				in("call", PortCall, true, "Play, then hang up"),
				out("audio", PortPrompt, true, "Used as prompt", "",
					tg(KindIVR, "greeting"), tg(KindIVR, "invalidPrompt"), tg(KindIVR, "timeoutPrompt"),
					tg(KindQueue, "periodicPrompt"), tg(KindQueue, "positionPrompt"), tg(KindQueue, "waitPrompt"),
					tg(KindQueue, "minutesPrompt"), tg(KindQueue, "breakoutPrompt"),
					tg(KindMOHClass, "tracks"),
					tg(KindConference, "entryAnnouncement"), tg(KindConference, "exitAnnouncement")),
			}},
			{Kind: KindPagingGroup, Label: "Paging group", Ports: []Port{
				in("call", PortCall, true, "Page"),
				in("members", PortMember, true, "Members"),
				in("callers", PortMember, true, "Allowed callers"),
				out("multicast", PortCall, false, "Also page multicast channel", "multicastChannelId", tg(KindMulticast, "page")),
			}},
			{Kind: KindMulticast, Label: "Multicast channel", Ports: []Port{
				in("page", PortCall, true, "Paged by"),
			}},
			{Kind: KindConference, Label: "Conference room", Ports: []Port{
				in("call", PortCall, true, "Join the room"),
				in("entryAnnouncement", PortPrompt, false, "Entry announcement"),
				in("exitAnnouncement", PortPrompt, false, "Exit announcement"),
			}},
			{Kind: KindParkLot, Label: "Park lot", Ports: []Port{}},
			{Kind: KindPickupGroup, Label: "Pickup group", Ports: []Port{
				in("members", PortMember, true, "Members"),
			}},
			{Kind: KindVMGroup, Label: "Voicemail group", Ports: []Port{
				in("call", PortCall, true, "Leave a message"),
				in("members", PortMember, true, "Members"),
			}},
			{Kind: KindFeatureCode, Label: "Feature code", Ports: []Port{
				out("target", PortCall, false, "Target", "argument", tAnn, tIVR, tTC),
			}},
			{Kind: KindTimeCond, Label: "Time condition", Ports: []Port{
				in("call", PortCall, true, "Call"),
				in("schedule", PortSchedule, false, "Schedule"),
				out("open", PortCall, false, "Open", "open", exit...),
				out("closed", PortCall, false, "Closed", "closed", exit...),
				out("holiday", PortCall, false, "Holiday", "holiday", exit...),
			}},
			{Kind: KindSchedule, Label: "Schedule", Ports: []Port{
				in("calendars", PortSchedule, true, "Holiday calendars"),
				out("use", PortSchedule, true, "Used by", "scheduleId", tg(KindTimeCond, "schedule")),
			}},
			{Kind: KindHolidayCal, Label: "Holiday calendar", Ports: []Port{
				out("use", PortSchedule, true, "Used by", "calendarIds", tg(KindSchedule, "calendars")),
			}},
			{Kind: KindMOHClass, Label: "Hold music class", Ports: []Port{
				in("tracks", PortPrompt, true, "Tracks"),
				out("use", PortHoldMusic, true, "Used by", "mohClassId",
					tg(KindExtension, "holdMusic"), tg(KindRingGroup, "holdMusic"),
					tg(KindInboundRoute, "holdMusic"), tg(KindQueue, "holdMusic")),
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
	// A per-key port is addressed as "<id>:<key>".
	if id, _, ok := strings.Cut(port, ":"); ok {
		for _, p := range n.Ports {
			if p.ID == id && p.PerKey {
				return p, true
			}
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
