package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/flow"
	"github.com/azrtydxb/hello/internal/routing"
)

// matchedRouteRe reads the name of a route out of a routing trace step; the
// first one is the inbound route, even when it hands the call to an
// outbound route.
var matchedRouteRe = regexp.MustCompile(`^Route ("(?:[^"\\]|\\.)*") matched`)

// traceDigitsRe is the keys a caller can press in a menu.
var traceDigitsRe = regexp.MustCompile(`^[0-9*#]{0,32}$`)

type traceResult struct {
	ConfigRevision int64            `json:"configRevision"`
	At             string           `json:"at"`
	From           string           `json:"from"`
	Number         string           `json:"number"`
	Outcome        string           `json:"outcome"`
	Route          string           `json:"route,omitempty"`
	Reason         string           `json:"reason,omitempty"`
	Steps          []flow.TraceStep `json:"steps"`
	RoutingTrace   routing.Trace    `json:"routingTrace"`
}

// getFlowTrace is GET /api/v1/flow/trace: where a call from `from` to
// `number` at `at` would go. It decides the call as routingTest does, then
// walks the flow graph from the matched route or extension. It places and
// writes nothing.
func (s *server) getFlowTrace(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f fieldErrs
	from, number, callerID, digits := q.Get("from"), q.Get("number"), q.Get("callerId"), q.Get("digits")
	call := routing.Call{
		Number: number, CallerID: callerID, SIPDomain: s.SIPDomain,
		Header: func(string) string { return "" }, At: time.Now(),
	}
	if v, ok := strings.CutPrefix(from, "trunk:"); ok {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			f.add("from", `must be an extension number or "trunk:<id>"`)
		}
		call.FromTrunk = id
	} else if numberRe.MatchString(from) {
		call.FromExtension = from
	} else {
		f.add("from", `must be an extension number or "trunk:<id>"`)
	}
	if number == "" || !printable(number, 64, true) {
		f.add("number", "must be 1-64 printable characters")
	}
	if !printable(callerID, 64, true) {
		f.add("callerId", "must be at most 64 printable characters")
	}
	if !traceDigitsRe.MatchString(digits) {
		f.add("digits", "must be at most 32 of the keys 0-9, * and #")
	}
	if at := q.Get("at"); at != "" {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			f.add("at", "must be an RFC 3339 time")
		}
		call.At = t
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}

	var in flow.Input
	var rev int64
	for range flowReadAttempts {
		before, err := s.Store.ConfigRevision(r.Context())
		if err != nil {
			s.internal(w, "read config revision", err)
			return
		}
		in, err = s.flowInput(r.Context(), false)
		if err != nil {
			s.internal(w, "read configuration for the flow trace", err)
			return
		}
		after, err := s.Store.ConfigRevision(r.Context())
		if err != nil {
			s.internal(w, "read config revision", err)
			return
		}
		if before == after {
			rev = after
			break
		}
	}
	if rev == 0 {
		writeError(w, http.StatusServiceUnavailable, "config_changing", "the configuration kept changing; try again")
		return
	}
	in.Revision = rev

	res := traceResult{
		ConfigRevision: rev, At: call.At.UTC().Format(time.RFC3339), From: from, Number: number,
		Steps: []flow.TraceStep{}, RoutingTrace: routing.Trace{},
	}
	opt := flow.TraceOptions{At: call.At, Digits: digits}
	var start flow.TraceStart
	if call.FromExtension == "" || !s.startFromExtension(&start, &res, in, call) {
		if !s.decide(w, r, &start, &res, in, call) {
			return
		}
	}
	steps, err := flow.Trace(in, start, opt)
	if err != nil {
		s.internal(w, "trace the call", err)
		return
	}
	res.Steps = steps
	if s.BatchMetrics != nil && s.BatchMetrics.Traces != nil {
		s.BatchMetrics.Traces.Inc()
	}
	writeJSON(w, http.StatusOK, res)
}

// startFromExtension resolves the numbers hello-sip answers before routing
// (paging code, conference room, IVR number, queue number, ring group
// name), in its order. It reports false when routing must decide.
func (s *server) startFromExtension(start *flow.TraceStart, res *traceResult, in flow.Input, call routing.Call) bool {
	caller := extNode(in, call.FromExtension)
	n := call.Number
	enter := ""
	port := "call"
	switch {
	case pagingNode(in, n) != "":
		enter = pagingNode(in, n)
	case conferenceNode(in, n) != "":
		enter = conferenceNode(in, n)
	case ivrNode(in, n) != "":
		enter = ivrNode(in, n)
	case queueNode(in, n) != "":
		enter = queueNode(in, n)
	case featureOrSlot(in, call.FromExtension, n, start):
		res.Outcome = "internal"
		res.RoutingTrace.Add("Dialled " + n + " is a feature code or a park slot: answered before routing")
		start.Steps = append(callerSteps(caller, call), start.Steps...)
		return true
	case groupNode(in, n) != "":
		enter, port = groupNode(in, n), "ring"
	default:
		return false
	}
	res.Outcome = "internal"
	res.RoutingTrace.Add("Dialled " + n + " is answered before routing: a number of the configuration itself")
	start.Steps = callerSteps(caller, call)
	start.Enter = &flow.Ref{Node: enter, Port: port}
	return true
}

func callerSteps(caller string, call routing.Call) []flow.TraceStep {
	if caller == "" {
		return nil
	}
	return []flow.TraceStep{{Node: caller, Text: "Extension " + call.FromExtension + " dials " + call.Number}}
}

func extNode(in flow.Input, number string) string {
	for _, e := range in.Extensions {
		if e.Number == number {
			return "extension:" + strconv.FormatInt(e.ID, 10)
		}
	}
	return ""
}

func pagingNode(in flow.Input, code string) string {
	for _, p := range in.PagingGroups {
		if p.Code == code {
			return "pagingGroup:" + strconv.FormatInt(p.ID, 10)
		}
	}
	return ""
}

func conferenceNode(in flow.Input, number string) string {
	for _, c := range in.Conferences {
		if c.Number == number {
			return "conference:" + strconv.FormatInt(c.ID, 10)
		}
	}
	return ""
}

func ivrNode(in flow.Input, number string) string {
	for _, m := range in.IVRMenus {
		if m.Number == number {
			return "ivr:" + strconv.FormatInt(m.ID, 10)
		}
	}
	return ""
}

func queueNode(in flow.Input, number string) string {
	for _, q := range in.Queues {
		if q.Number == number {
			return "queue:" + strconv.FormatInt(q.ID, 10)
		}
	}
	return ""
}

func groupNode(in flow.Input, name string) string {
	for _, g := range in.RingGroups {
		if g.Name == name {
			return "ringGroup:" + strconv.FormatInt(g.ID, 10)
		}
	}
	return ""
}

// decide runs the routing decision and turns it into where the walk starts.
// It writes the error response and reports false on failure.
func (s *server) decide(w http.ResponseWriter, r *http.Request, start *flow.TraceStart, res *traceResult, in flow.Input, call routing.Call) bool {
	snap, err := s.Store.RoutingConfig(r.Context())
	if err != nil {
		s.internal(w, "flow trace: load configuration", err)
		return false
	}
	table, errs := s.Router.Compile(snap.Config)
	if len(errs) > 0 || table == nil {
		s.Log.Error("flow trace: saved configuration does not compile", "fields", errs)
		writeError(w, http.StatusInternalServerError, "internal", "the saved routing configuration does not compile")
		return false
	}
	usable, note := s.usability(r.Context(), snap)
	d := table.Decide(call, usable)
	if note != "" {
		res.RoutingTrace.Add(note)
	}
	for _, st := range d.Trace {
		res.RoutingTrace.Add(st.Text)
	}
	res.Outcome, res.Route, res.Reason = string(d.Kind), d.Route, d.Reason
	buildStart(start, in, call, d)
	return true
}

// buildStart maps a routing decision to the graph: the source (trunk or
// calling extension), the matched route, and the node the walk continues
// from.
func buildStart(start *flow.TraceStart, in flow.Input, call routing.Call, d routing.Decision) {
	var src flow.TraceStep
	var inbound *int
	if call.FromTrunk != 0 {
		tn := "trunk:" + strconv.FormatInt(call.FromTrunk, 10)
		name := tn
		for _, t := range in.Trunks {
			if t.ID == call.FromTrunk {
				name = t.Name
			}
		}
		src = flow.TraceStep{Node: tn, Port: "inbound", Text: "A call to " + call.Number + " arrives on trunk " + name}
		for i := range in.Inbound {
			if routeNameInTrace(d) == in.Inbound[i].Name {
				inbound = &i
				break
			}
		}
	} else {
		caller := extNode(in, call.FromExtension)
		if caller == "" {
			caller = "dialPlan:internal"
		}
		src = flow.TraceStep{Node: caller, Text: "Extension " + call.FromExtension + " dials " + call.Number}
	}
	start.Steps = []flow.TraceStep{src}

	if d.Kind == routing.KindReject {
		if src.Port == "inbound" {
			src.Port = ""
		}
		start.Steps = []flow.TraceStep{src, {Node: src.Node, Text: "The call is rejected (" + strconv.Itoa(d.RejectCode) + "): " + d.Reason}}
		return
	}
	var route *flow.TraceStep
	if inbound != nil {
		ir := in.Inbound[*inbound]
		rid := "inboundRoute:" + strconv.FormatInt(ir.ID, 10)
		start.Steps[0].Port = "inbound"
		route = &flow.TraceStep{Node: rid, Port: "destination", Text: "Inbound route " + ir.Name + " matches; its destination is " + ir.DestinationKind + " " + ir.Destination}
		start.Steps = append(start.Steps, *route)
		switch {
		case ir.DestinationKind == "internal":
			start.Steps = append(start.Steps, flow.TraceStep{Node: "internalDid:" + strconv.FormatInt(ir.ID, 10), Text: "The DID is looked up as an internal extension: " + d.Extension})
			if n := extNode(in, d.Extension); n != "" {
				start.Enter = &flow.Ref{Node: n, Port: "ring"}
			}
			return
		case d.Kind == routing.KindOutbound:
			start.Follow = &flow.Ref{Node: rid, Port: "destination"}
			start.Tail = outboundSteps(in, d)
			return
		}
		start.Follow = &flow.Ref{Node: rid, Port: "destination"}
		return
	}
	switch d.Kind {
	case routing.KindInternal:
		if n := extNode(in, d.Extension); n != "" {
			start.Enter = &flow.Ref{Node: n, Port: "ring"}
		}
	case routing.KindOutbound:
		start.Tail = outboundSteps(in, d)
	}
}

func routeNameInTrace(d routing.Decision) string {
	for _, st := range d.Trace {
		if m := matchedRouteRe.FindStringSubmatch(st.Text); m != nil {
			if name, err := strconv.Unquote(m[1]); err == nil {
				return name
			}
		}
	}
	return ""
}

// outboundSteps are the outbound route and the trunks it tries, in order.
func outboundSteps(in flow.Input, d routing.Decision) []flow.TraceStep {
	var out []flow.TraceStep
	var route string
	for _, o := range in.Outbound {
		if o.Name == d.Route {
			route = "outboundRoute:" + strconv.FormatInt(o.ID, 10)
		}
	}
	if route == "" {
		return nil
	}
	var names []string
	for _, c := range d.Candidates {
		if c.Trunk != nil {
			names = append(names, c.Trunk.Name)
		}
	}
	text := "Outbound route " + d.Route + " matches"
	if len(names) > 0 {
		text += "; it tries trunk " + strings.Join(names, ", then ")
	}
	out = append(out, flow.TraceStep{Node: route, Port: "trunks", Text: text})
	if len(d.Candidates) > 0 && d.Candidates[0].Trunk != nil {
		t := d.Candidates[0].Trunk
		out = append(out, flow.TraceStep{Node: "trunk:" + strconv.FormatInt(t.ID, 10), Port: "carrier",
			Text: "The call leaves through trunk " + t.Name + " as " + d.Number})
	}
	return out
}

// featureOrSlot fills start for a dialled park slot or feature code.
func featureOrSlot(in flow.Input, from, n string, start *flow.TraceStart) bool {
	s, ok := flow.FeatureCodeStart(in, from, n)
	if ok {
		*start = s
	}
	return ok
}
