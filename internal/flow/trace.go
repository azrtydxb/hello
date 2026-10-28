package flow

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/timecond"
)

// Limits of a trace walk.
const (
	// MaxTraceDepth is how many levels of IVR menus are listed branch by
	// branch when no key is given (spec S-18).
	MaxTraceDepth = 6
	// maxTraceSteps stops a runaway walk in one branch.
	maxTraceSteps = 64
)

// TraceStep is one stop on the path of a call. Node is a graph node id.
// Port is the output port of Node the call leaves through (the canvas
// highlights the edge from it to the next step), or the input port a
// terminal step concerns. At is set on time condition steps: the instant
// that was evaluated. Branches list the alternatives of an IVR menu that
// was traced without a key.
type TraceStep struct {
	Node     string        `json:"node"`
	Port     string        `json:"port,omitempty"`
	Text     string        `json:"text"`
	At       string        `json:"at,omitempty"`
	Branches []TraceBranch `json:"branches,omitempty"`
}

// TraceBranch is one alternative path of a step.
type TraceBranch struct {
	Port  string      `json:"port"`
	Label string      `json:"label"`
	Steps []TraceStep `json:"steps"`
}

// Unknown is what the trace says about live state it could not read.
const Unknown = "unknown (live state unavailable)"

// Live is the live state a trace reads (spec S-18): which park slots hold a
// call and the agents' login and pause state. A nil *Live, or a part that is
// not Known, is state that could not be read: the trace says so and branches
// on both possibilities instead of guessing.
type Live struct {
	ParkKnown bool
	// Parked maps an occupied slot to the extension that parked the call.
	Parked map[string]string
	// AgentsKnown says Agents was read.
	AgentsKnown bool
	// Agents maps an extension number to its shared state; an extension
	// with no entry has never logged in or paused.
	Agents map[string]AgentState
}

// AgentState is an agent's login and pause state.
type AgentState struct {
	LoggedIn bool
	// Queues limits the login to those queue names; empty means all.
	Queues []string
	Paused bool
	Reason string
}

func (a AgentState) loggedInTo(queue string) bool {
	if !a.LoggedIn {
		return false
	}
	if len(a.Queues) == 0 {
		return true
	}
	for _, q := range a.Queues {
		if q == queue {
			return true
		}
	}
	return false
}

// agentStatus says how an agent stands in a queue: staffed, or why not.
func agentStatus(l *Live, queue string, dynamic bool, ext string) (staffed bool, why string) {
	st := l.Agents[ext]
	switch {
	case st.Paused:
		why = "paused"
		if st.Reason != "" {
			why += " (" + st.Reason + ")"
		}
		return false, why
	case !dynamic:
		return true, "available"
	case st.loggedInTo(queue):
		return true, "logged in"
	}
	return false, "logged out"
}

// TraceStart says where the walk begins. The routing decision has already
// produced Steps (the trunk, the matched route); the walk then continues
// from the output port Follow, or enters the node input Enter.
type TraceStart struct {
	Steps  []TraceStep
	Follow *Ref
	Enter  *Ref
	// Tail is appended after the walk: the outbound hop of a call that
	// leaves again through a trunk.
	Tail []TraceStep
}

// TraceOptions are the inputs of the walk.
type TraceOptions struct {
	// At is the instant time conditions are evaluated at.
	At time.Time
	// Digits are the keys pressed in IVR menus, in order. With none left at
	// a menu, every configured key is listed as a branch.
	Digits string
	// Live is the live state; nil means it could not be read.
	Live *Live
}

type walker struct {
	g      Graph
	in     Input
	opt    TraceOptions
	out    map[string][]Edge
	nodes  map[string]Node
	exts   map[string]store.Extension // node id
	byNum  map[string]string          // extension number -> node id
	groups map[string]store.RingGroup
	ivrs   map[string]store.IVRMenu
	queues map[string]store.Queue
	tcs    map[string]string // time condition name -> node id
}

// Trace walks the call's path from start through the graph built from in.
func Trace(in Input, start TraceStart, opt TraceOptions) ([]TraceStep, error) {
	g, err := Build(in, Options{})
	if err != nil {
		return nil, err
	}
	w := &walker{
		g: g, in: in, opt: opt,
		out: map[string][]Edge{}, nodes: map[string]Node{},
		exts: map[string]store.Extension{}, byNum: map[string]string{},
		groups: map[string]store.RingGroup{}, ivrs: map[string]store.IVRMenu{},
		queues: map[string]store.Queue{}, tcs: map[string]string{},
	}
	for _, n := range g.Nodes {
		w.nodes[n.ID] = n
	}
	for _, e := range g.Edges {
		k := e.Source.Node + "\x00" + e.Source.Port
		w.out[k] = append(w.out[k], e)
	}
	for _, e := range in.Extensions {
		id := nid(KindExtension, e.ID)
		w.exts[id] = e
		w.byNum[e.Number] = id
	}
	for _, x := range in.RingGroups {
		w.groups[nid(KindRingGroup, x.ID)] = x
	}
	for _, x := range in.IVRMenus {
		w.ivrs[nid(KindIVR, x.ID)] = x
	}
	for _, x := range in.Queues {
		w.queues[nid(KindQueue, x.ID)] = x
	}
	for _, x := range in.TimeConditions {
		w.tcs[x.Name] = nid(KindTimeCond, x.ID)
	}
	steps := append([]TraceStep{}, start.Steps...)
	st := &state{visited: map[string]bool{}}
	switch {
	case start.Follow != nil:
		steps = append(steps, w.follow(*start.Follow, w.opt.Digits, 0, st)...)
	case start.Enter != nil:
		steps = append(steps, w.visit(*start.Enter, nil, w.opt.Digits, 0, st)...)
	}
	return append(steps, start.Tail...), nil
}

// state is per path: the (node, port, digits) triples passed, to cut loops.
type state struct {
	visited map[string]bool
	n       int
}

func (s *state) clone() *state {
	c := &state{visited: make(map[string]bool, len(s.visited)), n: s.n}
	for k := range s.visited {
		c.visited[k] = true
	}
	return c
}

func (w *walker) label(id string) string {
	if n, ok := w.nodes[id]; ok {
		return n.Label
	}
	return id
}

// follow takes the edge stored at an output port.
func (w *walker) follow(src Ref, digits string, depth int, st *state) []TraceStep {
	es := w.out[src.Node+"\x00"+src.Port]
	if len(es) == 0 {
		return nil
	}
	e := es[0]
	return w.visit(e.Target, &e, digits, depth, st)
}

func (w *walker) visit(in Ref, via *Edge, digits string, depth int, st *state) []TraceStep {
	key := in.Node + "\x00" + in.Port + "\x00" + digits
	if st.visited[key] {
		return []TraceStep{{Node: in.Node, Port: in.Port, Text: "The call returns to " + w.label(in.Node) + ": a loop, it would keep going round"}}
	}
	st.visited[key] = true
	st.n++
	if st.n > maxTraceSteps {
		return []TraceStep{{Node: in.Node, Text: "The trace stops here: the path is longer than " + strconv.Itoa(maxTraceSteps) + " steps"}}
	}
	n, ok := w.nodes[in.Node]
	if !ok {
		return []TraceStep{{Node: in.Node, Text: "Unknown node"}}
	}
	switch n.Kind {
	case KindExtension:
		if in.Port == "voicemail" {
			return w.voicemail(in.Node)
		}
		return w.extension(in.Node, digits, depth, st)
	case KindRingGroup:
		return w.ringGroup(in.Node, digits, depth, st)
	case KindIVR:
		return w.ivr(in.Node, digits, depth, st)
	case KindQueue:
		return w.queue(in.Node, digits, depth, st)
	case KindTimeCond:
		return w.timeCondition(in.Node, digits, depth, st)
	case KindAnnouncement:
		then := ""
		if via != nil {
			then, _ = via.Data["thenVoicemail"].(string)
		}
		return w.announcement(in.Node, then)
	case KindPagingGroup:
		return []TraceStep{{Node: in.Node, Text: "Pages group " + n.Label + ", then the call ends"}}
	case KindConference:
		return []TraceStep{{Node: in.Node, Text: "Joins conference room " + n.Label}}
	case KindExternal:
		return []TraceStep{{Node: in.Node, Text: "Dials the external number " + n.Label}}
	case KindSIPURI:
		return []TraceStep{{Node: in.Node, Text: "Sends the call to the SIP address " + n.Label}}
	case KindInternalDID:
		return []TraceStep{{Node: in.Node, Text: "The dialled DID is looked up as an internal extension"}}
	case KindMissing:
		return []TraceStep{{Node: in.Node, Text: "Destination " + n.Label + " does not exist: the call is rejected"}}
	}
	return []TraceStep{{Node: in.Node, Text: "Reaches " + n.Label}}
}

func (w *walker) voicemail(node string) []TraceStep {
	e := w.exts[node]
	if !e.VoicemailEnabled {
		return []TraceStep{{Node: node, Port: "voicemail", Text: "Voicemail is off for extension " + e.Number + ": the call is declined"}}
	}
	return []TraceStep{{Node: node, Port: "voicemail", Text: "The caller leaves a message in the voicemail box of extension " + e.Number}}
}

func (w *walker) extension(id, digits string, depth int, st *state) []TraceStep {
	e := w.exts[id]
	lbl := w.label(id)
	if e.ForwardAlways != "" {
		s := TraceStep{Node: id, Port: "forwardAlways", Text: "Extension " + lbl + " forwards every call to " + e.ForwardAlways}
		return append([]TraceStep{s}, w.follow(Ref{id, "forwardAlways"}, digits, depth, st)...)
	}
	if e.DND {
		if e.VoicemailEnabled {
			return []TraceStep{{Node: id, Port: "voicemail", Text: "Extension " + lbl + " is on do not disturb: the call goes to its voicemail"}}
		}
		return []TraceStep{{Node: id, Text: "Extension " + lbl + " is on do not disturb and has no voicemail: the call is declined"}}
	}
	ring := TraceStep{Node: id, Port: "ring", Text: "Rings extension " + lbl}
	ring.Branches = w.extensionBranches(id, digits, depth, st)
	switch {
	case e.ForwardNoAnswer != "":
		ring.Port = "forwardNoAnswer"
		ring.Text += "; if nobody answers it forwards to " + e.ForwardNoAnswer
		return append([]TraceStep{ring}, w.follow(Ref{id, "forwardNoAnswer"}, digits, depth, st)...)
	case e.VoicemailEnabled:
		ring.Text += "; if nobody answers the caller reaches its voicemail"
		return append([]TraceStep{ring}, w.voicemail(id)...)
	}
	ring.Text += "; if nobody answers the call ends"
	return []TraceStep{ring}
}

// extensionBranches are the alternatives to ringing that the configuration
// decides: the extension is busy, or no device is registered. Whether either
// is the case is live state, so both are listed (spec S-18).
func (w *walker) extensionBranches(id, digits string, depth int, st *state) []TraceBranch {
	e := w.exts[id]
	var out []TraceBranch
	busy := TraceBranch{Port: "forwardBusy", Label: "If busy"}
	switch {
	case e.ForwardBusy != "":
		busy.Steps = append([]TraceStep{{Node: id, Port: "forwardBusy", Text: "Busy: the call forwards to " + e.ForwardBusy}},
			w.follow(Ref{id, "forwardBusy"}, digits, depth, st.clone())...)
	case e.VoicemailEnabled:
		busy.Steps = append([]TraceStep{{Node: id, Port: "voicemail", Text: "Busy, and no busy forward is set: the caller reaches the voicemail"}}, w.voicemail(id)...)
	default:
		busy.Steps = []TraceStep{{Node: id, Text: "Busy, with no busy forward and no voicemail: the caller hears busy"}}
	}
	out = append(out, busy)
	unreg := TraceBranch{Port: "voicemail", Label: "If not registered"}
	if e.VoicemailEnabled {
		unreg.Steps = append([]TraceStep{{Node: id, Port: "voicemail", Text: "No device is registered: the caller reaches the voicemail"}}, w.voicemail(id)...)
	} else {
		unreg.Steps = []TraceStep{{Node: id, Text: "No device is registered and there is no voicemail: the call is answered temporarily unavailable"}}
	}
	return append(out, unreg)
}

func (w *walker) members(ms []int64) string {
	var nums []string
	for _, m := range ms {
		if e, ok := w.exts[nid(KindExtension, m)]; ok {
			nums = append(nums, e.Number)
		}
	}
	return strings.Join(nums, ", ")
}

func (w *walker) ringGroup(id, digits string, depth int, st *state) []TraceStep {
	g := w.groups[id]
	ids := make([]int64, 0, len(g.Members))
	for _, m := range g.Members {
		ids = append(ids, m.ExtensionID)
	}
	s := TraceStep{Node: id, Port: "noAnswer"}
	if len(ids) == 0 {
		s.Text = fmt.Sprintf("Ring group %s has no members", g.Name)
	} else {
		s.Text = fmt.Sprintf("Rings group %s (%s, %d members: %s) for %d s; a member who answers ends the trace", g.Name, g.Strategy, len(ids), w.members(ids), g.RingTimeout)
	}
	if NoDestination[g.FailureKind] {
		s.Port = ""
		s.Text += "; when nobody answers the call is hung up"
		return []TraceStep{s}
	}
	s.Text += "; when nobody answers it goes to " + g.FailureKind + " " + g.FailureTarget
	return append([]TraceStep{s}, w.follow(Ref{id, "noAnswer"}, digits, depth, st)...)
}

func (w *walker) queue(id, digits string, depth int, st *state) []TraceStep {
	q := w.queues[id]
	s := TraceStep{Node: id}
	exit := func(port string, dest store.IVRDestination, st *state) []TraceStep {
		if NoDestination[dest.Kind] {
			return []TraceStep{{Node: id, Text: "The call is hung up"}}
		}
		return append([]TraceStep{{Node: id, Port: port, Text: "It goes to " + dest.Kind + " " + dest.Target}}, w.follow(Ref{id, port}, digits, depth, st)...)
	}
	if len(q.Members) == 0 {
		s.Text = fmt.Sprintf("Queue %s has no agents", q.Name)
		return append([]TraceStep{s}, exit("noAgents", q.NoAgents, st)...)
	}
	s.Text = fmt.Sprintf("Joins queue %s (%s, %d agents)", q.Name, q.Strategy, len(q.Members))
	live := w.opt.Live
	if live == nil || !live.AgentsKnown {
		// The agents' state is live: both outcomes are shown.
		s.Text += "; whether an agent is logged in and not paused is " + Unknown
		yes := TraceBranch{Port: "timeout", Label: "If an agent is available"}
		yes.Steps = append([]TraceStep{{Node: id, Text: fmt.Sprintf("An agent who answers ends the trace; after %d s of waiting", q.MaxWait)}},
			exit("timeout", q.Timeout, st.clone())...)
		no := TraceBranch{Port: "noAgents", Label: "If no agent is available"}
		no.Steps = exit("noAgents", q.NoAgents, st.clone())
		s.Branches = []TraceBranch{yes, no}
		return []TraceStep{s}
	}
	var staffed int
	var parts []string
	for _, m := range q.Members {
		ok, why := agentStatus(live, q.Name, m.Dynamic, m.Number)
		if ok {
			staffed++
		}
		parts = append(parts, m.Number+" "+why)
	}
	s.Text += "; agents: " + strings.Join(parts, ", ")
	if staffed == 0 {
		s.Text += "; none is available, so the call takes the no-agents exit"
		return append([]TraceStep{s}, exit("noAgents", q.NoAgents, st)...)
	}
	s.Text += fmt.Sprintf("; an agent who answers ends the trace; after %d s of waiting", q.MaxWait)
	return append([]TraceStep{s}, exit("timeout", q.Timeout, st)...)
}

func (w *walker) announcement(id, then string) []TraceStep {
	s := TraceStep{Node: id, Text: "Plays announcement " + w.label(id) + ", then hangs up"}
	if then == "" {
		return []TraceStep{s}
	}
	s.Text = "Plays announcement " + w.label(id) + ", then goes to the voicemail box of extension " + then
	if vm, ok := w.byNum[then]; ok {
		return append([]TraceStep{s}, w.voicemail(vm)...)
	}
	return []TraceStep{s, {Node: KindMissing + ":" + then, Text: "Voicemail box " + then + " does not exist"}}
}

func (w *walker) ivr(id, digits string, depth int, st *state) []TraceStep {
	m := w.ivrs[id]
	greet := "Menu " + m.Name + " plays its greeting"
	if !m.Enabled {
		return []TraceStep{{Node: id, Text: "Menu " + m.Name + " is disabled"}}
	}
	options := map[string]store.IVROption{}
	for _, o := range m.Options {
		options[o.Digit] = o
	}
	if digits == "" {
		return w.ivrBranches(id, m, greet, depth, st)
	}
	key := digits[:1]
	rest := digits[1:]
	if _, ok := options[key]; ok {
		port := "key:" + key
		s := TraceStep{Node: id, Port: port, Text: fmt.Sprintf("%s; the caller presses %s", greet, key)}
		return append([]TraceStep{s}, w.follow(Ref{id, port}, rest, depth, st)...)
	}
	s := TraceStep{Node: id, Port: "invalid", Text: fmt.Sprintf("%s; the caller presses %s, which is not an option (after %d retries it goes to %s)", greet, key, m.MaxRetries, destText(m.Invalid))}
	if NoDestination[m.Invalid.Kind] {
		s.Port = ""
		return []TraceStep{s}
	}
	return append([]TraceStep{s}, w.follow(Ref{id, "invalid"}, rest, depth, st)...)
}

func destText(d store.IVRDestination) string {
	if NoDestination[d.Kind] {
		return "hang up"
	}
	return d.Kind + " " + d.Target
}

// ivrBranches lists every key (and the time-out) as a branch.
func (w *walker) ivrBranches(id string, m store.IVRMenu, greet string, depth int, st *state) []TraceStep {
	s := TraceStep{Node: id, Text: greet + "; no key was given, so each choice is listed"}
	if depth >= MaxTraceDepth {
		s.Text += " (not expanded: more than " + strconv.Itoa(MaxTraceDepth) + " menus deep)"
		return []TraceStep{s}
	}
	for _, o := range m.Options {
		port := "key:" + o.Digit
		b := TraceBranch{Port: port, Label: "Key " + o.Digit}
		b.Steps = w.follow(Ref{id, port}, "", depth+1, st.clone())
		if b.Steps == nil {
			b.Steps = []TraceStep{{Node: id, Port: port, Text: "Key " + o.Digit + ": the call is hung up"}}
		}
		s.Branches = append(s.Branches, b)
	}
	tb := TraceBranch{Port: "timeout", Label: "No key"}
	tb.Steps = w.follow(Ref{id, "timeout"}, "", depth+1, st.clone())
	if tb.Steps == nil {
		tb.Steps = []TraceStep{{Node: id, Port: "timeout", Text: "No key pressed: the call is hung up"}}
	}
	s.Branches = append(s.Branches, tb)
	return []TraceStep{s}
}

// conditionSet gives the time conditions as the evaluator reads them.
func (w *walker) conditionSet() map[string]timecond.Condition {
	cals := map[int64]store.Calendar{}
	for _, c := range w.in.Calendars {
		cals[c.ID] = c
	}
	specs := map[int64]timecond.Spec{}
	for _, s := range w.in.Schedules {
		spec := timecond.Spec{TimeZone: s.TimeZone, Ranges: s.Ranges}
		for _, cid := range s.CalendarIDs {
			c := cals[cid]
			for _, e := range c.Entries {
				spec.Entries = append(spec.Entries, timecond.CalendarEntry{Calendar: c.Name, Entry: e})
			}
		}
		specs[s.ID] = spec
	}
	out := map[string]timecond.Condition{}
	for _, t := range w.in.TimeConditions {
		out[t.Name] = timecond.Condition{Name: t.Name, Spec: specs[t.ScheduleID], Override: t.Override,
			Open: t.Open, Closed: t.Closed, Holiday: t.Holiday}
	}
	return out
}

func (w *walker) timeCondition(id, digits string, depth int, st *state) []TraceStep {
	name := w.label(id)
	set := w.conditionSet()
	lookup := func(n string) (timecond.Condition, bool) { c, ok := set[n]; return c, ok }
	at := w.opt.At.UTC().Format(time.RFC3339)
	res, err := timecond.Resolve(lookup, name, w.opt.At)
	var steps []TraceStep
	last := id
	port := ""
	for _, rs := range res.Steps {
		node := w.tcs[rs.Condition]
		c := set[rs.Condition]
		port = rs.Branch
		if rs.Branch == timecond.BranchHoliday && c.Holiday == nil {
			port = timecond.BranchClosed // the closed branch serves holidays
		}
		why := rs.Why
		if rs.Forced {
			why = "forced " + rs.Branch + " by a manual override"
			for _, t := range w.in.TimeConditions {
				if t.Name == rs.Condition && t.OverrideAt != nil {
					why += " set " + t.OverrideAt.UTC().Format(time.RFC3339)
					if t.OverrideBy != "" {
						why += " by " + t.OverrideBy
					}
				}
			}
		}
		steps = append(steps, TraceStep{
			Node: node, Port: port, At: at,
			Text: fmt.Sprintf("Time condition %s at %s: %s (%s) -> %s", rs.Condition, at, rs.Branch, why, rs.Dest),
		})
		last = node
	}
	if err != nil {
		return append(steps, TraceStep{Node: last, Text: "The time conditions cannot be followed: " + err.Error() + "; the call is hung up"})
	}
	if NoDestination[res.Dest.Kind] || res.Dest.Kind == "" {
		return steps
	}
	return append(steps, w.follow(Ref{last, port}, digits, depth, st)...)
}

// FeatureCodeStart starts a trace at what hello-sip does with a number dialled
// from extension from that is a park slot or a feature code (the code, or the
// code and its argument). ok is false when the number is neither. Only the
// configuration and live (which may be nil: unknown) are read.
func FeatureCodeStart(in Input, from, dialled string, live *Live) (TraceStart, bool) {
	caller := ""
	for _, e := range in.Extensions {
		if e.Number == from {
			caller = nid(KindExtension, e.ID)
		}
	}
	if caller == "" {
		caller = KindDialPlan + ":internal"
	}
	step := func(node, port, text string) []TraceStep {
		return []TraceStep{{Node: node, Port: port, Text: text}}
	}
	slotState := func(slot string) string {
		switch {
		case live == nil || !live.ParkKnown:
			return "whether a call is parked there is " + Unknown
		case live.Parked[slot] != "":
			return "a call parked by extension " + live.Parked[slot] + " is there and is connected"
		}
		return "the slot is empty: there is nothing to retrieve"
	}
	if n, err := strconv.Atoi(dialled); err == nil {
		for _, l := range in.ParkLots {
			if l.Enabled && n >= l.SlotStart && n <= l.SlotEnd {
				return TraceStart{Steps: step(nid(KindParkLot, l.ID), "",
					"Dialling slot "+dialled+" of park lot "+l.Name+" retrieves the call parked there: "+slotState(dialled))}, true
			}
		}
	}
	codes := append([]store.FeatureCode{}, in.FeatureCodes...)
	sort.SliceStable(codes, func(i, j int) bool { return len(codes[i].Code) > len(codes[j].Code) })
	var fc store.FeatureCode
	arg, found := "", false
	for _, c := range codes {
		if c.Code != "" && strings.HasPrefix(dialled, c.Code) {
			fc, arg, found = c, strings.TrimPrefix(dialled, c.Code), true
			break
		}
	}
	if !found {
		return TraceStart{}, false
	}
	fcNode := KindFeatureCode + ":" + fc.Code
	head := fmt.Sprintf("Feature code %s (%s)", fc.Code, strings.ReplaceAll(fc.Action, "_", " "))
	ext := func(number string) string {
		for _, e := range in.Extensions {
			if e.Number == number {
				return nid(KindExtension, e.ID)
			}
		}
		return ""
	}
	name := fc.Argument
	if name == "" {
		name = arg
	}
	// target is the node a code that names one hands the call to.
	target := func(port string, find func(string) string, what string) (TraceStart, bool) {
		node := find(name)
		s := TraceStart{}
		if fc.Argument != "" {
			s.Steps = step(fcNode, "target", head+" goes to "+what+" "+name)
		} else {
			s.Steps = step(caller, "", head+" goes to "+what+" "+name)
		}
		if node == "" {
			s.Steps = append(s.Steps, TraceStep{Node: KindMissing + ":" + name, Text: what + " " + name + " does not exist: the call is rejected (404)"})
			return s, true
		}
		s.Enter = &Ref{Node: node, Port: port}
		return s, true
	}
	switch fc.Action {
	case "ivr":
		return target("call", func(n string) string {
			for _, m := range in.IVRMenus {
				if m.Name == n {
					return nid(KindIVR, m.ID)
				}
			}
			return ""
		}, "IVR menu")
	case "time_condition":
		return target("call", func(n string) string {
			for _, tc := range in.TimeConditions {
				if tc.Name == n {
					return nid(KindTimeCond, tc.ID)
				}
			}
			return ""
		}, "time condition")
	case "announcement":
		return target("call", func(n string) string {
			for _, a := range in.Announcements {
				if a.Name == n {
					return nid(KindAnnouncement, a.ID)
				}
			}
			return ""
		}, "announcement")
	case "voicemail":
		if arg == "" {
			return TraceStart{Steps: step(caller, "voicemail", head+" opens the caller's own voicemail box to listen to messages")}, true
		}
		s := TraceStart{Steps: step(caller, "", head+" leaves a message in the voicemail box of extension "+arg)}
		if n := ext(arg); n != "" {
			s.Enter = &Ref{Node: n, Port: "voicemail"}
		} else {
			s.Steps = append(s.Steps, TraceStep{Node: KindMissing + ":" + arg, Text: "There is no voicemail box for extension " + arg + ": the call is rejected (404)"})
		}
		return s, true
	case "intercom":
		s := TraceStart{Steps: step(caller, "", head+" auto-answers the phone of extension "+arg)}
		if n := ext(arg); n != "" {
			s.Steps = append(s.Steps, TraceStep{Node: n, Port: "ring", Text: "Intercom to extension " + arg})
		}
		return s, true
	case "queue_login", "queue_logout", "queue_pause", "queue_unpause":
		what := strings.TrimPrefix(fc.Action, "queue_")
		now := "whether the extension is now logged in or paused is " + Unknown
		if live != nil && live.AgentsKnown {
			a := live.Agents[from]
			now = "the extension is now "
			if a.LoggedIn {
				now += "logged in"
			} else {
				now += "logged out"
			}
			if a.Paused {
				now += " and paused"
			}
		}
		for _, q := range in.Queues {
			if arg != "" && q.Number == arg {
				return TraceStart{Steps: step(nid(KindQueue, q.ID), "", head+": the caller's extension is "+what+"d in queue "+q.Name+" ("+now+")")}, true
			}
		}
		return TraceStart{Steps: step(caller, "", head+": the caller's extension is "+what+"d in the queue it names, or in every queue it is an agent of ("+now+")")}, true
	case "tc_override":
		for _, tc := range in.TimeConditions {
			if tc.ToggleNumber != "" && tc.ToggleNumber == arg {
				return TraceStart{Steps: step(nid(KindTimeCond, tc.ID), "", head+" changes the override of time condition "+tc.Name+" ("+overrideMode(fc.Argument)+"); no call is placed")}, true
			}
		}
		return TraceStart{Steps: append(step(caller, "", head+" names no time condition (toggle number "+arg+")"), TraceStep{Node: caller, Text: "The call is rejected (404)"})}, true
	case "park":
		return TraceStart{Steps: step(caller, "", head+" works only inside a call: dialled as a call it is rejected (403)")}, true
	case "park_retrieve":
		if n, err := strconv.Atoi(arg); err == nil {
			for _, l := range in.ParkLots {
				if l.Enabled && n >= l.SlotStart && n <= l.SlotEnd {
					return TraceStart{Steps: step(nid(KindParkLot, l.ID), "", head+" retrieves the call parked in slot "+arg+" of park lot "+l.Name+": "+slotState(arg))}, true
				}
			}
		}
		return TraceStart{Steps: step(caller, "", head+": slot "+strconv.Quote(arg)+" is in no park lot: the call is rejected (404)")}, true
	case "pickup_group":
		for _, g := range in.PickupGroups {
			for _, m := range g.Members {
				if nid(KindExtension, m.ExtensionID) == caller {
					return TraceStart{Steps: step(nid(KindPickupGroup, g.ID), "", head+" picks up a ringing call of the other members of pickup group "+g.Name)}, true
				}
			}
		}
		return TraceStart{Steps: step(caller, "", head+" picks up a ringing call within the caller's ring groups (it is in no pickup group)")}, true
	case "pickup_directed":
		s := TraceStart{Steps: step(caller, "", head+" picks up the call ringing at extension "+arg)}
		if n := ext(arg); n != "" {
			s.Steps = append(s.Steps, TraceStep{Node: n, Text: "Takes over the call ringing at extension " + arg + ", if one is ringing"})
		}
		return s, true
	case "blind_transfer", "attended_transfer":
		return TraceStart{Steps: step(caller, "", head+" works only inside a call (during it, dial it to transfer): dialled as a call it is rejected (403)")}, true
	}
	// The settings codes: they change the caller's own extension and answer.
	what := map[string]string{
		"forward_always": "forward-always", "forward_busy": "forward-busy", "forward_no_answer": "forward-no-answer",
		"dnd_on": "turns do not disturb on", "dnd_off": "turns do not disturb off",
		"call_waiting_on": "turns call waiting on", "call_waiting_off": "turns call waiting off",
	}[fc.Action]
	text := head + " " + what + " for the caller's extension; the call is answered and ends"
	if strings.HasPrefix(fc.Action, "forward_") {
		switch {
		case fc.Argument == "clear":
			text = head + " clears the " + what + " number of the caller's extension; the call is answered and ends"
		case arg == "":
			text = head + " needs the number to forward to after the code: dialled without it the call is rejected (400)"
		default:
			text = head + " sets the " + what + " number of the caller's extension to " + arg + "; the call is answered and ends"
		}
	}
	return TraceStart{Steps: step(caller, "", text)}, true
}

func overrideMode(arg string) string {
	if arg == "" {
		return "toggle"
	}
	return arg
}
