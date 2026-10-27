package flow

import (
	"fmt"
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
	port, dest := "timeout", q.Timeout
	if len(q.Members) == 0 {
		port, dest = "noAgents", q.NoAgents
		s.Text = fmt.Sprintf("Queue %s has no agents", q.Name)
	} else {
		s.Text = fmt.Sprintf("Joins queue %s (%s, %d agents); an agent who answers ends the trace; after %d s of waiting", q.Name, q.Strategy, len(q.Members), q.MaxWait)
	}
	if NoDestination[dest.Kind] {
		s.Text += ": the call is hung up"
		return []TraceStep{s}
	}
	s.Port = port
	s.Text += ": it goes to " + dest.Kind + " " + dest.Target
	return append([]TraceStep{s}, w.follow(Ref{id, port}, digits, depth, st)...)
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
