package sip

import (
	"slices"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/events"
)

// EventSink receives call events (spec integrations I-34); *events.Emitter
// satisfies it. Emit never blocks: the SIP path does not wait on the event
// pipeline.
type EventSink interface {
	Emit(events.Event)
}

// Missed-call reasons (call.missed).
const (
	missedNoAnswer    = "no_answer"
	missedBusy        = "busy"
	missedDND         = "dnd"
	missedCancelled   = "cancelled"
	missedUnreachable = "unreachable"
)

// eventsOn reports whether this call produces call events: an ordinary call
// on a node with an event sink. Pages, intercoms and conference legs do not.
func (c *call) eventsOn() bool {
	return c.s.deps.Events != nil && (c.kind == "" || c.kind == cdr.KindCall)
}

// extName is the configured name of an extension, empty when unknown.
func (s *Server) extName(ext string) string {
	if snap := s.deps.Snapshots.Current(); snap != nil {
		if e, ok := snap.Extension(ext); ok {
			return e.Name
		}
	}
	return ""
}

// isExtension reports whether number is a configured extension.
func (s *Server) isExtension(number string) bool {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return false
	}
	_, ok := snap.Extension(number)
	return ok
}

// eventData fills the fields every call event carries; ext is the subject.
// The caller holds no lock.
func (c *call) eventData(ext string) events.Data {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eventDataLocked(ext)
}

func (c *call) eventDataLocked(ext string) events.Data {
	from := events.Party{Number: c.callerNum, Name: c.callerName}
	if c.callerDevice != "" {
		from.Extension = c.callerNum
	}
	to := events.Party{Number: c.dialled}
	if c.s.isExtension(c.dialled) {
		to.Extension, to.Name = c.dialled, c.s.extName(c.dialled)
	}
	if ext != "" {
		if from.Extension == ext && from.Name == "" {
			from.Name = c.s.extName(ext)
		}
		if to.Extension == "" && c.answeredExt == ext {
			// A group, forward or queue member answered: the callee party
			// is the extension that did.
			to.Extension, to.Name = ext, c.s.extName(ext)
		}
	}
	d := events.Data{
		CallID: c.id, Direction: c.direction, Extension: ext, From: from, To: to,
		Trunk: c.trunkName, StartedAt: c.start.UTC(),
	}
	if !c.answerTime.IsZero() {
		d.AnsweredAt = c.answerTime.UTC()
	}
	return d
}

// emit sends one event of type typ about ext.
func (c *call) emit(typ string, d events.Data, extra string) {
	c.s.deps.Events.Emit(events.New(typ, d, extra, time.Now()))
}

// noteRinging reports that ext is ringing (call.ringing, once per extension
// per call; a ring group rings several).
func (c *call) noteRinging(ext string) {
	if ext == "" || !c.eventsOn() {
		return
	}
	c.mu.Lock()
	if slices.Contains(c.ringedExts, ext) {
		c.mu.Unlock()
		return
	}
	c.ringedExts = append(c.ringedExts, ext)
	d := c.eventDataLocked(ext)
	c.mu.Unlock()
	c.emit(events.CallRinging, d, "")
}

// noteAnswered reports that ext answered (call.answered), for the answering
// extension and for a local caller. ext is empty when a trunk answered an
// outbound call.
func (c *call) noteAnswered(ext string) {
	if !c.eventsOn() {
		return
	}
	c.mu.Lock()
	if c.answeredExt == "" {
		c.answeredExt = ext
	}
	subjects := c.subjectsLocked()
	if len(subjects) == 0 {
		c.mu.Unlock()
		return
	}
	var datas []events.Data
	for _, s := range subjects {
		// Only the answering extension and the caller are told of an answer;
		// the others of a group stop ringing and see call.ended.
		if s == ext || s == c.callerExtLocked() {
			datas = append(datas, c.eventDataLocked(s))
		}
	}
	c.mu.Unlock()
	for _, d := range datas {
		c.emit(events.CallAnswered, d, "")
	}
}

// callerExtLocked is the calling extension, empty for a trunk caller.
func (c *call) callerExtLocked() string {
	if c.callerDevice != "" {
		return c.callerNum
	}
	return ""
}

// subjectsLocked lists the local extensions the call is about: the caller,
// every extension that rang, the one that answered and, when nothing rang, the
// dialled extension of an inbound or internal call.
func (c *call) subjectsLocked() []string {
	var out []string
	add := func(e string) {
		if e != "" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	add(c.callerExtLocked())
	for _, e := range c.ringedExts {
		add(e)
	}
	add(c.answeredExt)
	if len(c.ringedExts) == 0 && c.answeredExt == "" && c.direction != cdr.DirectionOutbound && c.s.isExtension(c.dialled) {
		add(c.dialled)
	}
	return out
}

// dispositionOf maps a call result to the event disposition.
func dispositionOf(result string) string {
	switch result {
	case ResultAnswered, ResultCancelled, ResultBusy, ResultNoAnswer, ResultUnavailable, ResultFailed:
		return result
	case ResultNotFound:
		return ResultFailed
	}
	return result
}

// missedReason maps how a call ended without an answer to a call.missed
// reason; ok is false when the call was not missed by a person (the number
// does not exist, the node failed).
func missedReason(result, reason string) (string, bool) {
	switch {
	case reason == "DND":
		return missedDND, true
	case result == ResultBusy:
		return missedBusy, true
	case result == ResultNoAnswer:
		return missedNoAnswer, true
	case result == ResultCancelled:
		return missedCancelled, true
	case result == ResultUnavailable:
		return missedUnreachable, true
	}
	return "", false
}

// endEvents emits call.ended for every subject and call.missed for the
// extensions the call reached when nobody picked up. It runs from record,
// once per call, and returns the call.completed events the CDR writer
// publishes once the record is stored (with the CDR id).
func (c *call) endEvents(result, reason string, end, answered time.Time, durMs, billMs int64) []events.Event {
	if !c.eventsOn() {
		return nil // a node without an event sink does no event work at all
	}
	c.mu.Lock()
	subjects := c.subjectsLocked()
	c.evSubjects = slices.Clone(subjects)
	answeredExt, callerExt := c.answeredExt, c.callerExtLocked()
	// A call taken over from another node was answered there: its answer is
	// not known here, so it is never reported as missed.
	taken := c.homedCall != nil
	datas := make(map[string]events.Data, len(subjects))
	for _, s := range subjects {
		datas[s] = c.eventDataLocked(s)
	}
	c.mu.Unlock()
	disp := dispositionOf(result)
	var completed []events.Event
	for _, s := range subjects {
		d := datas[s]
		d.EndedAt, d.DurationMs, d.BillableMs, d.Disposition = end.UTC(), durMs, billMs, disp
		d.AnsweredAt = time.Time{}
		if !answered.IsZero() && (s == callerExt || s == answeredExt) {
			d.AnsweredAt = answered.UTC()
		}
		if answered.IsZero() {
			d.BillableMs = 0
		}
		c.emit(events.CallEnded, d, "")
		completed = append(completed, events.New(events.CallCompleted, d, "", end))
	}
	if answeredExt != "" || taken {
		return completed
	}
	mr, ok := missedReason(result, reason)
	if !ok && result == ResultAnswered {
		mr, ok = missedNoAnswer, true // the caller reached voicemail or an announcement: no person picked up
	}
	if !ok {
		return completed
	}
	for _, s := range subjects {
		if s == callerExt {
			continue
		}
		d := datas[s]
		d.EndedAt, d.DurationMs, d.Disposition, d.Reason = end.UTC(), durMs, disp, mr
		c.emit(events.CallMissed, d, "")
	}
	return completed
}

// recordingEvents emits call.recording.ready for every extension the call
// involved (the recording row is stored after the call has ended).
func (s *Server) recordingEvents(c *call, recordingID, durMs int64) {
	if !c.eventsOn() {
		return
	}
	c.mu.Lock()
	subjects := slices.Clone(c.evSubjects)
	if len(subjects) == 0 {
		subjects = c.subjectsLocked() // the recording was stored while the call is still up
	}
	c.mu.Unlock()
	for _, ext := range subjects {
		d := c.eventData(ext)
		d.RecordingID, d.DurationMs = recordingID, durMs
		s.deps.Events.Emit(events.New(events.CallRecordingReady, d, strconv.FormatInt(recordingID, 10), time.Now()))
	}
}
