package api

// Routing and ACD of the unified inbox (spec messaging-channels M-13, M-14):
// where a number's new conversations go and when it is open, what a
// supervisor sees of each text queue, and an agent's login and pause for the
// queues. The queues' own text settings are fields of the queue.

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/store"
)

// routingBody replaces a number's routing; what it leaves out keeps its
// value.
type routingBody struct {
	RouteKind   *string             `json:"routeKind"`
	RouteTarget *string             `json:"routeTarget"`
	Schedule    *messaging.Schedule `json:"schedule"`
	AfterHours  *afterHoursBody     `json:"afterHours"`
	// FirstContactText is sent once to a customer who texts the number for
	// the first time.
	FirstContactText *string `json:"firstContactText"`
}

type afterHoursBody struct {
	Action      *string `json:"action"`
	Text        *string `json:"text"`
	RouteKind   *string `json:"routeKind"`
	RouteTarget *string `json:"routeTarget"`
}

func routingInput(b routingBody, cur store.MsgRouting) (store.MsgRoutingInput, fieldErrs) {
	var f fieldErrs
	in := store.MsgRoutingInput{RouteKind: cur.RouteKind, RouteTarget: cur.RouteTarget, Schedule: cur.Schedule, AfterHours: cur.AfterHours,
		FirstContactText: cur.FirstContactText}
	if b.FirstContactText != nil {
		in.FirstContactText = strings.TrimSpace(*b.FirstContactText)
	}
	if !printable(in.FirstContactText, 480, true) {
		f.add("firstContactText", "must be at most 480 printable characters")
	}
	if b.RouteKind != nil {
		in.RouteKind = *b.RouteKind
		in.RouteTarget = "" // a new kind needs its own target
	}
	if b.RouteTarget != nil {
		in.RouteTarget = strings.TrimSpace(*b.RouteTarget)
	}
	if !messaging.ValidRouteKind(in.RouteKind) {
		f.add("routeKind", "must be one of %v", messaging.RouteKinds)
	} else if in.RouteKind == messaging.RouteInbox {
		in.RouteTarget = ""
	}
	if b.Schedule != nil {
		in.Schedule = *b.Schedule
	}
	if err := in.Schedule.Validate(); err != nil {
		f.add("schedule", "%s", err.Error())
	}
	if a := b.AfterHours; a != nil {
		if a.Action != nil {
			in.AfterHours.Action = *a.Action
		}
		if a.Text != nil {
			in.AfterHours.Text = strings.TrimSpace(*a.Text)
		}
		if a.RouteKind != nil {
			in.AfterHours.RouteKind = *a.RouteKind
			in.AfterHours.RouteTarget = ""
		}
		if a.RouteTarget != nil {
			in.AfterHours.RouteTarget = strings.TrimSpace(*a.RouteTarget)
		}
	}
	ah := &in.AfterHours
	if !messaging.ValidAfterHours(ah.Action) {
		f.add("afterHours.action", "must be none, reply or route")
	}
	if !printable(ah.Text, 480, true) {
		f.add("afterHours.text", "must be at most 480 printable characters")
	}
	if ah.Action == messaging.AfterReply && ah.Text == "" {
		f.add("afterHours.text", "is required to reply after hours")
	}
	if !messaging.ValidRouteKind(ah.RouteKind) {
		f.add("afterHours.routeKind", "must be one of %v", messaging.RouteKinds)
	} else if ah.RouteKind == messaging.RouteInbox {
		ah.RouteTarget = ""
	}
	return in, f
}

func (s *server) getMessagingRouting(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rt, err := is.GetMsgRouting(r.Context(), id)
	if err != nil {
		s.storeError(w, "messaging number", err)
		return
	}
	writeJSON(w, http.StatusOK, rt)
}

func (s *server) putMessagingRouting(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b routingBody
	if !decode(w, r, &b) {
		return
	}
	cur, err := is.GetMsgRouting(r.Context(), id)
	if err != nil {
		s.storeError(w, "messaging number", err)
		return
	}
	in, f := routingInput(b, cur)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	rt, err := is.PutMsgRouting(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.inboxError(w, "messaging number", err)
		return
	}
	writeJSON(w, http.StatusOK, rt)
}

// busyExtensions is the extensions with a call in progress, empty when the
// live state cannot be read.
func (s *server) busyExtensions(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if s.Live == nil {
		return out
	}
	states, err := s.Live.DeviceStates(ctx)
	if err != nil {
		return out
	}
	for _, d := range states {
		if d.State == "on-call" {
			out[d.Extension] = true
		}
	}
	return out
}

// listInboxQueues is the supervisors' view of the text queues: who waits and
// who answers. It carries no customer text and no number.
func (s *server) listInboxQueues(w http.ResponseWriter, r *http.Request) {
	is, _, ok := s.inboxCaller(w, r, true, false)
	if !ok {
		return
	}
	qs, err := is.GetTextQueueStatus(r.Context())
	if err != nil {
		s.internal(w, "text queue status", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	var states map[string]queue.AgentState
	live := false
	if s.Queues != nil {
		if states, err = s.Queues.Agents(ctx); err == nil {
			live = true
		}
	}
	busy := s.busyExtensions(ctx)
	for i := range qs {
		q := &qs[i]
		for j := range q.Agents {
			a := &q.Agents[j]
			st, known := states[a.Extension]
			switch {
			case !live:
				a.State = "unknown"
			case known && st.Paused:
				a.State = "paused"
			case !(queue.Member{Ext: a.Extension, Dynamic: a.Dynamic}).Staffed(q.Name, st, known):
				a.State = "logged_out"
			case busy[a.Extension]:
				a.State = "on_call"
			case a.Open >= q.Capacity:
				a.State = "at_capacity"
			default:
				a.State = "available"
			}
		}
	}
	writeJSON(w, http.StatusOK, items(qs))
}

// InboxAgentExt is one of an agent's extensions that answers a text queue.
type InboxAgentExt struct {
	Number   string   `json:"number"`
	Name     string   `json:"name"`
	Queues   []string `json:"queues"`
	Dynamic  bool     `json:"dynamic"`
	LoggedIn bool     `json:"loggedIn"`
	Paused   bool     `json:"paused"`
}

// InboxAgentState is the caller's availability for the text queues. The
// state is the call queues' own: one login and one pause per extension.
type InboxAgentState struct {
	// Member is whether any of the caller's extensions answers a text queue.
	Member bool `json:"member"`
	// NeedsLogin is whether a queue needs the agent to log in (a dynamic
	// member); a static member is always logged in.
	NeedsLogin bool            `json:"needsLogin"`
	LoggedIn   bool            `json:"loggedIn"`
	Paused     bool            `json:"paused"`
	Reason     string          `json:"reason"`
	Extensions []InboxAgentExt `json:"extensions"`
}

func agentSummary(exts []store.TextAgentExt, states map[string]queue.AgentState) InboxAgentState {
	out := InboxAgentState{Member: len(exts) > 0, LoggedIn: len(exts) > 0, Extensions: []InboxAgentExt{}}
	for _, e := range exts {
		st := states[e.Number]
		x := InboxAgentExt{Number: e.Number, Name: e.Name, Queues: e.Queues, Dynamic: e.Dynamic,
			LoggedIn: !e.Dynamic || st.LoggedIn, Paused: st.Paused}
		out.Extensions = append(out.Extensions, x)
		out.NeedsLogin = out.NeedsLogin || e.Dynamic
		out.LoggedIn = out.LoggedIn && x.LoggedIn
		if st.Paused && !out.Paused {
			out.Paused, out.Reason = true, st.Reason
		}
	}
	return out
}

func (s *server) getMyInboxAgent(w http.ResponseWriter, r *http.Request) {
	is, acc, ok := s.inboxCaller(w, r, false, true)
	if !ok {
		return
	}
	exts, err := is.UserTextAgent(r.Context(), acc.UserID)
	if err != nil {
		s.internal(w, "inbox agent", err)
		return
	}
	if len(exts) == 0 {
		writeJSON(w, http.StatusOK, agentSummary(nil, nil))
		return
	}
	if !s.queueLiveOK(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	states, err := s.Queues.Agents(ctx)
	if err != nil {
		s.liveDown(w, "queue agents", err)
		return
	}
	writeJSON(w, http.StatusOK, agentSummary(exts, states))
}

func (s *server) setMyInboxAgent(w http.ResponseWriter, r *http.Request) {
	is, acc, ok := s.inboxCaller(w, r, false, true)
	if !ok {
		return
	}
	var b agentBody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	if b.Queues != nil {
		f.add("queues", "is not accepted here; the inbox logs in to every text queue")
	}
	if b.LoggedIn == nil && b.Paused == nil {
		f.add("loggedIn", "give loggedIn or paused")
	}
	if b.Reason != nil && !pauseReasonRe.MatchString(*b.Reason) {
		f.add("reason", "must be at most 32 of A-Z a-z 0-9 space . _ -")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	exts, err := is.UserTextAgent(r.Context(), acc.UserID)
	if err != nil {
		s.internal(w, "inbox agent", err)
		return
	}
	if len(exts) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "none of your extensions answers a text queue")
		return
	}
	if !s.queueLiveOK(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	states := map[string]queue.AgentState{}
	for _, e := range exts {
		st, _, err := s.Queues.Agent(ctx, e.Number)
		if err != nil {
			s.liveDown(w, "queue agent", err)
			return
		}
		st = applyAgentBody(st, e.Number, b)
		if err := s.Queues.SetAgent(ctx, st); err != nil {
			s.liveDown(w, "set queue agent", err)
			return
		}
		states[e.Number] = st
	}
	writeJSON(w, http.StatusOK, agentSummary(exts, states))
}

// queueIDParam reads the optional queueId query parameter of a conversation
// list; ok is false after answering 400.
func queueIDParam(w http.ResponseWriter, v string) (int64, bool) {
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		badRequest(w, "queueId must be a queue id")
		return 0, false
	}
	return n, true
}
