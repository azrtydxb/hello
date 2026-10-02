package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// stateUnavailableStep is the trace step the tester adds when it cannot
// read trunk live state.
const stateUnavailableStep = "Trunk state unavailable (Valkey unreachable): treating every enabled trunk as usable"

type testDecision struct {
	Kind       routing.Kind `json:"kind"`
	Extension  string       `json:"extension"`
	SIPURI     string       `json:"sipUri"`
	Number     string       `json:"number"`
	CallerID   string       `json:"callerId"`
	Route      string       `json:"route"`
	Trunks     []string     `json:"trunks"`
	RejectCode int          `json:"rejectCode"`
	Reason     string       `json:"reason"`
}

// routingTest is POST /api/v1/routing/test: it decides a call against the
// saved configuration exactly as hello-sip would, and writes nothing.
func (s *server) routingTest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		From      string            `json:"from"`
		Number    string            `json:"number"`
		CallerID  string            `json:"callerId"`
		At        string            `json:"at"`
		SIPDomain *string           `json:"sipDomain"`
		Headers   map[string]string `json:"headers"`
	}
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	// SIP header names are case-insensitive.
	headers := make(map[string]string, len(b.Headers))
	if len(b.Headers) > 20 {
		f.add("headers", "at most 20 headers")
	}
	for name, v := range b.Headers {
		if !headerNameRe.MatchString(name) {
			f.add("headers", "header names must be SIP header names")
		} else if !printable(v, 256, true) {
			f.add("headers."+name, "must be at most 256 printable characters")
		}
		headers[strings.ToLower(name)] = v
	}
	call := routing.Call{
		Number: b.Number, CallerID: b.CallerID, SIPDomain: s.SIPDomain,
		Header: func(name string) string { return headers[strings.ToLower(name)] },
	}
	if b.SIPDomain != nil {
		if !validHost(*b.SIPDomain) {
			f.add("sipDomain", "must be a host name or IP address")
		}
		call.SIPDomain = *b.SIPDomain
	}
	if v, ok := strings.CutPrefix(b.From, "trunk:"); ok {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			f.add("from", `must be an extension number or "trunk:<id>"`)
		}
		call.FromTrunk = id
	} else if numberRe.MatchString(b.From) {
		call.FromExtension = b.From
	} else {
		f.add("from", `must be an extension number or "trunk:<id>"`)
	}
	if b.Number == "" || !printable(b.Number, 64, true) {
		f.add("number", "must be 1-64 printable characters")
	}
	if !printable(b.CallerID, 64, true) {
		f.add("callerId", "must be at most 64 printable characters")
	}
	call.At = time.Now()
	if b.At != "" {
		at, err := time.Parse(time.RFC3339, b.At)
		if err != nil {
			f.add("at", "must be an RFC 3339 time")
		}
		call.At = at
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}

	snap, err := s.Store.RoutingConfig(r.Context())
	if err != nil {
		s.internal(w, "routing test: load configuration", err)
		return
	}
	table, errs := s.Router.Compile(snap.Config)
	if len(errs) > 0 || table == nil {
		s.Log.Error("routing test: saved configuration does not compile", "fields", errs)
		writeError(w, http.StatusInternalServerError, "internal", "the saved routing configuration does not compile")
		return
	}
	usable, note := s.usability(r.Context(), snap)
	d := table.Decide(call, usable)

	trace := routing.Trace{}
	if note != "" {
		trace.Add(note)
	}
	if call.FromTrunk != 0 {
		for _, n := range unevaluated(snap.Config, b.SIPDomain != nil, len(b.Headers) > 0, s.SIPDomain) {
			trace.Add(n)
		}
	}
	for _, st := range d.Trace {
		trace.Add(st.Text)
	}
	for _, c := range d.Candidates {
		if c.Trunk != nil && c.Trunk.MaxCalls > 0 && !d.Emergency {
			trace.Add(fmt.Sprintf("Capacity is checked when the call is placed: trunk %s allows %d concurrent calls", c.Trunk.Name, c.Trunk.MaxCalls))
		}
	}
	out := testDecision{
		Kind: d.Kind, Extension: d.Extension, SIPURI: d.SIPURI, Number: d.Number, CallerID: d.CallerID,
		Route: d.Route, Trunks: []string{}, RejectCode: d.RejectCode, Reason: d.Reason,
	}
	for _, c := range d.Candidates {
		if c.Trunk != nil {
			out.Trunks = append(out.Trunks, c.Trunk.Name)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"decision": out, "trace": trace})
}

// usability judges each trunk from its live state. When that state cannot
// be read it treats every enabled, correctly configured trunk as usable and
// returns the trace step saying so.
func (s *server) usability(ctx context.Context, snap store.RoutingSnapshot) (routing.TrunkUsability, string) {
	trunks := map[int64]routing.Trunk{}
	for _, t := range snap.Config.Trunks {
		trunks[t.ID] = t
	}
	status := map[int64]livestate.TrunkStatus{}
	live := s.Trunks != nil
	if live {
		ctx, cancel := context.WithTimeout(ctx, liveTimeout)
		defer cancel()
		for id := range trunks {
			st, err := s.Trunks.TrunkStatus(ctx, id)
			if err != nil {
				s.Log.Warn("routing test: trunk state unavailable", "error", err)
				live = false
				break
			}
			status[id] = st
		}
	}
	note := ""
	if !live {
		note = stateUnavailableStep
	}
	return func(id int64, emergency bool) (bool, string) {
		t, ok := trunks[id]
		switch {
		case !ok:
			return false, "unknown trunk"
		case !t.Enabled:
			return false, "disabled"
		case snap.Misconfigured[id]:
			return false, "misconfigured"
		case !live:
			return true, ""
		}
		// The same rule hello-sip applies on the call path, so a tested
		// route and a real call agree. Fullness is not judged here:
		// hello-sip decides it at attempt time with a cluster-wide slot, so
		// the cached count is ignored and the trace says capacity is
		// checked when the call is placed.
		st := status[id]
		st.ActiveCalls = 0
		return st.Usability(t, emergency)
	}, note
}

// unevaluated lists, for an inbound test, the route conditions the request
// gave no input for: a SIP domain condition is tested against defaultDomain,
// a header condition against an absent header. The notes keep the trace
// honest about what was assumed.
func unevaluated(cfg routing.Config, haveDomain, haveHeaders bool, defaultDomain string) []string {
	var out []string
	for _, in := range cfg.Inbound {
		if !in.Enabled {
			continue
		}
		if in.SIPDomain != "" && !haveDomain {
			out = append(out, fmt.Sprintf("Route %s SIP domain condition not evaluated: no sipDomain given, so the call is tested as sent to %s",
				strconv.Quote(in.Name), defaultDomain))
		}
		if in.HeaderName != "" && !haveHeaders {
			out = append(out, fmt.Sprintf("Route %s header condition not evaluated: no headers given, so %s is treated as absent",
				strconv.Quote(in.Name), in.HeaderName))
		}
	}
	return out
}

type cdrDetail struct {
	store.CDR
	Trace       routing.Trace `json:"trace"`
	Explanation string        `json:"explanation"`
}

// getCDR is GET /api/v1/cdrs/{id}: the CDR, its routing trace, and for a
// failed call a one-line explanation (the last trace step).
func (s *server) getCDR(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, trace, err := s.Store.GetCDR(r.Context(), id)
	if err != nil {
		s.storeError(w, "cdr", err)
		return
	}
	writeJSON(w, http.StatusOK, cdrDetail{CDR: c, Trace: trace, Explanation: explain(c, trace)})
}

func explain(c store.CDR, trace routing.Trace) string {
	if c.FinalStatus >= 200 && c.FinalStatus < 300 {
		return ""
	}
	if len(trace) > 0 {
		return trace[len(trace)-1].Text
	}
	return c.FailureReason
}
