package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/store"
)

// ACD call queues (migration 00023): the configuration (CRUD), the agents'
// login and pause state, and the live status the wallboard reads. Reads are
// open to a viewer; changes need the write scope, like ring groups.

var (
	queueNumberRe = regexp.MustCompile(`^([0-9]{2,16})?$`)
	mohClassRe    = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)
	digitsPrefix  = regexp.MustCompile(`^[A-Za-z0-9._-]{0,32}$`)
	breakoutKeyRe = regexp.MustCompile(`^[0-9*#]?$`)
	pauseReasonRe = regexp.MustCompile(`^[A-Za-z0-9 ._-]{0,32}$`)
)

// queueBody is the create and replace body; absent optional fields take the
// defaults of toInput.
type queueBody struct {
	Name              *string            `json:"name"`
	Number            *string            `json:"number"`
	Enabled           *bool              `json:"enabled"`
	Strategy          *string            `json:"strategy"`
	AgentTimeout      *int               `json:"agentTimeout"`
	RetryDelay        *int               `json:"retryDelay"`
	WrapUp            *int               `json:"wrapUp"`
	MaxCallers        *int               `json:"maxCallers"`
	MaxWait           *int               `json:"maxWait"`
	JoinEmpty         *bool              `json:"joinEmpty"`
	LeaveEmpty        *bool              `json:"leaveEmpty"`
	Timeout           *ivrDestBody       `json:"timeout"`
	Full              *ivrDestBody       `json:"full"`
	NoAgents          *ivrDestBody       `json:"noAgents"`
	MOHClass          *string            `json:"mohClass"`
	AnnounceFrequency *int               `json:"announceFrequency"`
	AnnouncePosition  *bool              `json:"announcePosition"`
	AnnounceWait      *bool              `json:"announceWait"`
	PeriodicPromptID  *int64             `json:"periodicPromptId"`
	PositionPromptID  *int64             `json:"positionPromptId"`
	WaitPromptID      *int64             `json:"waitPromptId"`
	MinutesPromptID   *int64             `json:"minutesPromptId"`
	DigitsPrefix      *string            `json:"digitsPrefix"`
	BreakoutKey       *string            `json:"breakoutKey"`
	BreakoutPromptID  *int64             `json:"breakoutPromptId"`
	BreakoutVoicemail *string            `json:"breakoutVoicemail"`
	Members           *[]queueMemberBody `json:"members"`
}

type queueMemberBody struct {
	ExtensionID int64 `json:"extensionId"`
	Dynamic     bool  `json:"dynamic"`
	Penalty     int   `json:"penalty"`
	Position    int   `json:"position"`
}

func (b queueBody) toInput() store.QueueInput {
	hang := store.IVRDestination{Kind: store.IVRHangup}
	in := store.QueueInput{
		Enabled: true, Strategy: queue.RingAll, AgentTimeout: 20, RetryDelay: 5, MaxWait: 600, JoinEmpty: true,
		Timeout: hang, Full: hang, NoAgents: hang, Members: []store.QueueMember{},
	}
	setS := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	setI := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	setB := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	setD := func(dst *store.IVRDestination, v *ivrDestBody) {
		if v != nil {
			*dst = store.IVRDestination{Kind: v.Kind, Target: v.Target}
		}
	}
	setS(&in.Name, b.Name)
	setS(&in.Number, b.Number)
	setB(&in.Enabled, b.Enabled)
	setS(&in.Strategy, b.Strategy)
	setI(&in.AgentTimeout, b.AgentTimeout)
	setI(&in.RetryDelay, b.RetryDelay)
	setI(&in.WrapUp, b.WrapUp)
	setI(&in.MaxCallers, b.MaxCallers)
	setI(&in.MaxWait, b.MaxWait)
	setB(&in.JoinEmpty, b.JoinEmpty)
	setB(&in.LeaveEmpty, b.LeaveEmpty)
	setD(&in.Timeout, b.Timeout)
	setD(&in.Full, b.Full)
	setD(&in.NoAgents, b.NoAgents)
	setS(&in.MOHClass, b.MOHClass)
	setI(&in.AnnounceFrequency, b.AnnounceFrequency)
	setB(&in.AnnouncePosition, b.AnnouncePosition)
	setB(&in.AnnounceWait, b.AnnounceWait)
	in.PeriodicPromptID, in.PositionPromptID = b.PeriodicPromptID, b.PositionPromptID
	in.WaitPromptID, in.MinutesPromptID, in.BreakoutPromptID = b.WaitPromptID, b.MinutesPromptID, b.BreakoutPromptID
	setS(&in.DigitsPrefix, b.DigitsPrefix)
	setS(&in.BreakoutKey, b.BreakoutKey)
	setS(&in.BreakoutVoicemail, b.BreakoutVoicemail)
	if b.Members != nil {
		for i, m := range *b.Members {
			if m.Position == 0 {
				m.Position = i + 1
			}
			in.Members = append(in.Members, store.QueueMember{ExtensionID: m.ExtensionID, Dynamic: m.Dynamic, Penalty: m.Penalty, Position: m.Position})
		}
	}
	return in
}

// validateQueue checks a full input (stage 1); the store checks references.
func validateQueue(in *store.QueueInput) fieldErrs {
	var f fieldErrs
	if !usernameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	if !queueNumberRe.MatchString(in.Number) {
		f.add("number", "must be empty or 2-16 digits")
	}
	if !queue.ValidStrategy(in.Strategy) {
		f.add("strategy", "must be one of %v", queue.Strategies)
	}
	between := func(path string, v, lo, hi int) {
		if v < lo || v > hi {
			f.add(path, "must be %d-%d", lo, hi)
		}
	}
	between("agentTimeout", in.AgentTimeout, 5, 120)
	between("retryDelay", in.RetryDelay, 0, 120)
	between("wrapUp", in.WrapUp, 0, 600)
	between("maxCallers", in.MaxCallers, 0, 1000)
	between("maxWait", in.MaxWait, 0, 86400)
	between("announceFrequency", in.AnnounceFrequency, 0, 600)
	if !mohClassRe.MatchString(in.MOHClass) {
		f.add("mohClass", "must be at most 64 of A-Z a-z 0-9 . _ -")
	}
	if !digitsPrefix.MatchString(in.DigitsPrefix) {
		f.add("digitsPrefix", "must be at most 32 of A-Z a-z 0-9 . _ -")
	}
	if !breakoutKeyRe.MatchString(in.BreakoutKey) {
		f.add("breakoutKey", "must be empty or one of 0-9, * or #")
	}
	if in.BreakoutKey != "" && in.BreakoutVoicemail == "" {
		f.add("breakoutVoicemail", "is required with a breakoutKey")
	}
	if in.BreakoutVoicemail != "" && !numberRe.MatchString(in.BreakoutVoicemail) {
		f.add("breakoutVoicemail", "must be an extension number (2-10 digits)")
	}
	for path, id := range map[string]*int64{"periodicPromptId": in.PeriodicPromptID, "positionPromptId": in.PositionPromptID,
		"waitPromptId": in.WaitPromptID, "minutesPromptId": in.MinutesPromptID, "breakoutPromptId": in.BreakoutPromptID} {
		if id != nil && *id <= 0 {
			f.add(path, "must be an announcement id or null")
		}
	}
	if (in.AnnouncePosition || in.AnnounceWait) && in.AnnounceFrequency == 0 {
		f.add("announceFrequency", "must be set when the position or the wait time is announced")
	}
	if in.AnnounceWait && in.MinutesPromptID == nil {
		f.add("minutesPromptId", "is required to announce the wait time")
	}
	if (in.AnnouncePosition || in.AnnounceWait) && in.DigitsPrefix == "" {
		f.add("digitsPrefix", "is required to announce numbers (announcements <prefix>0 to <prefix>9)")
	}
	if in.AnnouncePosition && in.PositionPromptID == nil {
		f.add("positionPromptId", "is required to announce the position")
	}
	if in.AnnounceWait && in.WaitPromptID == nil {
		f.add("waitPromptId", "is required to announce the wait time")
	}
	for path, d := range map[string]store.IVRDestination{"timeout": in.Timeout, "full": in.Full, "noAgents": in.NoAgents} {
		validateIVRDestination(&f, path, d.Kind, d.Target)
	}
	if len(in.Members) > store.MaxQueueMembers {
		f.add("members", "must have at most %d members", store.MaxQueueMembers)
	}
	pos := map[int]bool{}
	for i, m := range in.Members {
		p := fmt.Sprintf("members[%d]", i)
		if m.ExtensionID <= 0 {
			f.add(p+".extensionId", "must name an extension")
		}
		if m.Penalty < 0 || m.Penalty > 99 {
			f.add(p+".penalty", "must be 0-99")
		}
		if m.Position < 1 {
			f.add(p+".position", "must be 1 or more")
		}
		if pos[m.Position] {
			f.add(p+".position", "position %d is used twice", m.Position)
		}
		pos[m.Position] = true
	}
	return f
}

// requireQueue adds a field error when no queue has that name.
func (s *server) requireQueue(ctx context.Context, f *fieldErrs, path, name string) {
	ok, err := s.Store.QueueNameExists(ctx, name)
	switch {
	case err != nil:
		s.Log.Warn("queue lookup", "error", err)
		f.add(path, "could not check the queue")
	case !ok:
		f.add(path, "queue %q does not exist", name)
	}
}

func (s *server) listQueues(w http.ResponseWriter, r *http.Request) {
	qs, err := s.Store.ListQueues(r.Context())
	if err != nil {
		s.internal(w, "list queues", err)
		return
	}
	writeJSON(w, http.StatusOK, items(qs))
}

func (s *server) getQueue(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q, err := s.Store.GetQueue(r.Context(), id)
	if err != nil {
		s.configError(w, "queue", err)
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func (s *server) createQueue(w http.ResponseWriter, r *http.Request) {
	var b queueBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil {
		badRequest(w, "name is required")
		return
	}
	in := b.toInput()
	if f := validateQueue(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	q, err := s.Store.CreateQueue(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "queue", err)
		return
	}
	writeJSON(w, http.StatusCreated, q)
}

// putQueue replaces a queue as a whole.
func (s *server) putQueue(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b queueBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil {
		badRequest(w, "name is required")
		return
	}
	in := b.toInput()
	if f := validateQueue(&in); len(f) > 0 {
		writeFields(w, f)
		return
	}
	q, err := s.Store.UpdateQueue(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "queue", err)
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func (s *server) deleteQueue(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteQueue(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "queue", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- live: agents and the wallboard ------------------------------------------

// QueueCaller is one waiting caller as the wallboard shows it.
type QueueCaller struct {
	CallID      string `json:"callId"`
	Caller      string `json:"caller"`
	CallerName  string `json:"callerName"`
	Position    int    `json:"position"`
	WaitSeconds int64  `json:"waitSeconds"`
	Offering    string `json:"offering"`
	Node        string `json:"node"`
}

// QueueAgent is one agent in a queue with its live state. State is one of
// available, on_call, ringing, dnd, wrapping, paused or logged_out.
type QueueAgent struct {
	Extension    string `json:"extension"`
	Name         string `json:"name"`
	Dynamic      bool   `json:"dynamic"`
	Penalty      int    `json:"penalty"`
	LoggedIn     bool   `json:"loggedIn"`
	Paused       bool   `json:"paused"`
	PauseReason  string `json:"pauseReason"`
	State        string `json:"state"`
	CallsAnswerd int    `json:"callsAnswered"`
	WrapSeconds  int64  `json:"wrapSeconds"`
}

// QueueStatus is a queue's live state.
type QueueStatus struct {
	Name               string        `json:"name"`
	Number             string        `json:"number"`
	Enabled            bool          `json:"enabled"`
	Strategy           string        `json:"strategy"`
	Waiting            []QueueCaller `json:"waiting"`
	LongestWaitSeconds int64         `json:"longestWaitSeconds"`
	AvgHandleSeconds   int64         `json:"avgHandleSeconds"`
	Staffed            int           `json:"staffed"`
	Available          int           `json:"available"`
	Agents             []QueueAgent  `json:"agents"`
}

// AgentStatus is an agent's global login and pause state.
type AgentStatus struct {
	Extension string   `json:"extension"`
	Name      string   `json:"name"`
	LoggedIn  bool     `json:"loggedIn"`
	Queues    []string `json:"queues"`
	Paused    bool     `json:"paused"`
	Reason    string   `json:"reason"`
	Since     string   `json:"since"`
}

func (s *server) queueLiveOK(w http.ResponseWriter) bool {
	if s.Queues == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "live state (Valkey) is unavailable; try again shortly")
		return false
	}
	return true
}

// agentState maps the shared state, the phone's presence and the wrap-up
// time to the one label the wallboard shows.
func agentState(m store.QueueMember, st queue.AgentState, known bool, stats queue.AgentStats, presence string, now time.Time, queueName string) string {
	qm := queue.Member{Ext: m.Number, Dynamic: m.Dynamic}
	switch {
	case known && st.Paused:
		return "paused"
	case !qm.Staffed(queueName, st, known):
		return "logged_out"
	case presence == "dnd":
		return "dnd"
	case presence == "on-call":
		return "on_call"
	case presence == "ringing":
		return "ringing"
	case stats.Wrapping(now):
		return "wrapping"
	}
	return "available"
}

// listQueueStatus is the wallboard feed: every queue's waiting callers,
// agents and longest wait.
func (s *server) listQueueStatus(w http.ResponseWriter, r *http.Request) {
	if !s.queueLiveOK(w) {
		return
	}
	qs, err := s.Store.ListQueues(r.Context())
	if err != nil {
		s.internal(w, "list queues", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	agents, err := s.Queues.Agents(ctx)
	if err != nil {
		s.liveDown(w, "queue agents", err)
		return
	}
	presence := map[string]string{}
	if s.Live != nil {
		if states, err := s.Live.DeviceStates(ctx); err == nil {
			rank := map[string]int{"idle": 0, "dnd": 1, "ringing": 2, "on-call": 3}
			for _, d := range states {
				if cur, ok := presence[d.Extension]; !ok || rank[d.State] > rank[cur] {
					presence[d.Extension] = d.State
				}
			}
		}
	}
	now := time.Now()
	out := make([]QueueStatus, 0, len(qs))
	for _, q := range qs {
		waiting, err := s.Queues.Waiting(ctx, q.Name)
		if err != nil {
			s.liveDown(w, "queue waiting", err)
			return
		}
		stats, err := s.Queues.Stats(ctx, q.Name)
		if err != nil {
			s.liveDown(w, "queue stats", err)
			return
		}
		avg, err := s.Queues.AvgHandle(ctx, q.Name)
		if err != nil {
			s.liveDown(w, "queue handling time", err)
			return
		}
		st := QueueStatus{Name: q.Name, Number: q.Number, Enabled: q.Enabled, Strategy: q.Strategy,
			Waiting: []QueueCaller{}, Agents: []QueueAgent{}, AvgHandleSeconds: int64(avg / time.Second)}
		for i, e := range waiting {
			wait := int64(now.Sub(e.Entered()) / time.Second)
			st.Waiting = append(st.Waiting, QueueCaller{CallID: e.CallID, Caller: e.Caller, CallerName: e.CallerName,
				Position: i + 1, WaitSeconds: wait, Offering: e.Offering, Node: e.Node})
			st.LongestWaitSeconds = max(st.LongestWaitSeconds, wait)
		}
		for _, m := range q.Members {
			as, known := agents[m.Number]
			ss := stats[m.Number]
			a := QueueAgent{Extension: m.Number, Name: m.Name, Dynamic: m.Dynamic, Penalty: m.Penalty,
				LoggedIn: !m.Dynamic || (known && as.LoggedInTo(q.Name)), Paused: known && as.Paused, PauseReason: as.Reason,
				CallsAnswerd: ss.Calls}
			if ss.Wrapping(now) {
				a.WrapSeconds = (ss.WrapUntilMs - now.UnixMilli() + 999) / 1000
			}
			a.State = agentState(m, as, known, ss, presence[m.Number], now, q.Name)
			if a.State != "logged_out" && a.State != "paused" {
				st.Staffed++
			}
			if a.State == "available" {
				st.Available++
			}
			st.Agents = append(st.Agents, a)
		}
		out = append(out, st)
	}
	writeJSON(w, http.StatusOK, items(out))
}

func agentStatus(ext, name string, st queue.AgentState) AgentStatus {
	a := AgentStatus{Extension: ext, Name: name, LoggedIn: st.LoggedIn, Queues: st.Queues, Paused: st.Paused, Reason: st.Reason}
	if a.Queues == nil {
		a.Queues = []string{}
	}
	if st.SinceMs > 0 {
		a.Since = time.UnixMilli(st.SinceMs).UTC().Format(time.RFC3339)
	}
	return a
}

// memberOf returns the queue member rows of ext: its name and the queues it
// is an agent of.
func (s *server) memberOf(ctx context.Context, ext string) (name string, queues []string, err error) {
	qs, err := s.Store.ListQueues(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, q := range qs {
		for _, m := range q.Members {
			if m.Number == ext {
				name = m.Name
				queues = append(queues, q.Name)
			}
		}
	}
	return name, queues, nil
}

// listQueueAgents lists every agent (an extension in some queue) with its
// login and pause state.
func (s *server) listQueueAgents(w http.ResponseWriter, r *http.Request) {
	if !s.queueLiveOK(w) {
		return
	}
	qs, err := s.Store.ListQueues(r.Context())
	if err != nil {
		s.internal(w, "list queues", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	states, err := s.Queues.Agents(ctx)
	if err != nil {
		s.liveDown(w, "queue agents", err)
		return
	}
	seen := map[string]bool{}
	out := []AgentStatus{}
	for _, q := range qs {
		for _, m := range q.Members {
			if !seen[m.Number] {
				seen[m.Number] = true
				out = append(out, agentStatus(m.Number, m.Name, states[m.Number]))
			}
		}
	}
	slices.SortFunc(out, func(a, b AgentStatus) int {
		switch {
		case a.Extension < b.Extension:
			return -1
		case a.Extension > b.Extension:
			return 1
		}
		return 0
	})
	writeJSON(w, http.StatusOK, items(out))
}

type agentBody struct {
	LoggedIn *bool     `json:"loggedIn"`
	Paused   *bool     `json:"paused"`
	Reason   *string   `json:"reason"`
	Queues   *[]string `json:"queues"`
}

// setQueueAgent logs an agent in or out and pauses or unpauses it; absent
// fields keep their value. Pausing takes a reason (default "pause").
func (s *server) setQueueAgent(w http.ResponseWriter, r *http.Request) {
	ext := r.PathValue("extension")
	if !numberRe.MatchString(ext) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	var b agentBody
	if !decode(w, r, &b) {
		return
	}
	if !s.queueLiveOK(w) {
		return
	}
	name, mine, err := s.memberOf(r.Context(), ext)
	if err != nil {
		s.internal(w, "queue membership", err)
		return
	}
	if len(mine) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "extension "+ext+" is not an agent of any queue")
		return
	}
	var f fieldErrs
	if b.Reason != nil && !pauseReasonRe.MatchString(*b.Reason) {
		f.add("reason", "must be at most 32 of A-Z a-z 0-9 space . _ -")
	}
	if b.Queues != nil {
		for i, qn := range *b.Queues {
			if !slices.Contains(mine, qn) {
				f.add(fmt.Sprintf("queues[%d]", i), "extension %s is not an agent of queue %q", ext, qn)
			}
		}
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	st, _, err := s.Queues.Agent(ctx, ext)
	if err != nil {
		s.liveDown(w, "queue agent", err)
		return
	}
	st.Ext = ext
	if b.LoggedIn != nil {
		st.LoggedIn = *b.LoggedIn
		if !st.LoggedIn {
			st.Queues, st.Paused, st.Reason = nil, false, ""
		}
	}
	if b.Queues != nil {
		st.Queues = *b.Queues
	}
	if b.Paused != nil {
		st.Paused = *b.Paused
		st.Reason = ""
		if st.Paused {
			st.Reason = "pause"
			if b.Reason != nil && *b.Reason != "" {
				st.Reason = *b.Reason
			}
		}
	}
	st.SinceMs = time.Now().UnixMilli()
	if err := s.Queues.SetAgent(ctx, st); err != nil {
		s.liveDown(w, "set queue agent", err)
		return
	}
	writeJSON(w, http.StatusOK, agentStatus(ext, name, st))
}
