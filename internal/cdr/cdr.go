// Package cdr queues call detail records in memory and writes them to the
// cdrs table from a background goroutine, so the SIP path never waits on
// PostgreSQL. The queue is bounded: when it is full a record is dropped and
// hello_cdr_dropped_total is incremented.
package cdr

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/momo"
	"github.com/prometheus/client_golang/prometheus"
)

// QueueSize is the number of records buffered while PostgreSQL is slow or
// unavailable.
const QueueSize = 1000

// Call directions.
const (
	DirectionInternal = "internal"
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// Call kinds (migration 00013; conference, 00034).
const (
	KindCall       = "call"
	KindPage       = "page"
	KindIntercom   = "intercom"
	KindConference = "conference"
	KindDoor       = "door"     // a door station's call or a call to a Door group (migration 00053)
	KindCallback   = "callback" // a call Hello placed to a queue caller who asked for a callback (migration 00049)
	KindAlarm      = "alarm"    // a *55 alarm call (migration 00080)
	KindFax        = "fax"      // a fax call, received or sent (migration 00073); the fax is in the faxes table, by correlation id
)

// Call origins (migration 00057).
const (
	OriginPhone = "phone"
	OriginAPI   = "api"
)

// Termination sides.
const (
	SideCaller = "caller"
	SideCallee = "callee"
	SideSystem = "system"
)

// Record is one call attempt. Zero times are stored as NULL.
type Record struct {
	CorrelationID   string
	SIPCallID       string
	Source          string // calling extension number
	Destination     string // dialled number
	StartTime       time.Time
	RingTime        time.Time
	AnswerTime      time.Time
	EndTime         time.Time
	DurationMs      int64
	BillableMs      int64
	SIPNode         string
	MediaMode       string // direct
	FinalStatus     int    // final SIP status sent to the caller
	TerminationSide string // caller | callee | system
	FailureReason   string
	// Routing detail (Phase 2). Direction is internal, inbound or outbound;
	// zero values store as the column defaults.
	Direction            string
	OriginalDestination  string
	RewrittenDestination string
	Route                string
	Trunk                string
	Trace                routing.Trace // never holds secrets
	// Call quality (spec S-5.1): nil (NULL) for directly-media calls and
	// calls that never answered.
	RTPPackets  *int64
	RTPLost     *int64
	RTPJitterMs *float64
	// CallerCodec and CalleeCodec are the audio codec each leg ended on, as
	// "name/clock rate" ("PCMU/8000", "opus/48000"); empty (NULL) for a leg
	// that had no media negotiated (migration 00055).
	CallerCodec string
	CalleeCodec string
	// Kind is call (default), page or intercom.
	Kind string
	// AnsweredBy is the extension that answered when it was not the one
	// dialled: the assistant of a boss link (migration 00065). Empty otherwise.
	AnsweredBy string
	// Origin is phone (default: dialled at a phone) or api (a click-to-call
	// request, spec integrations I-37); OriginClient names the OAuth client
	// or token id behind an api call (migration 00057).
	Origin       string
	OriginClient string
	// RingTag is the ring tag the call's device legs carried and RingRule
	// what chose it (migration 00050); empty for a call that rang no
	// internal device.
	RingTag, RingRule string
	// SRTP says which legs of the call carried SRTP (migration 00081):
	// none (the default), caller, callee or both.
	SRTP string
	// SRTPKeyEx is how the encrypted legs agreed their keys (migration
	// 00079): none (the default), sdes, dtls or mixed; ICE says a leg ran
	// ICE-lite.
	SRTPKeyEx string
	ICE       bool
	// Video says the call carried video at some point (migration 00097).
	Video bool
	// PeerCallID is the Call-ID of the answering callee leg (migration
	// 00066), so an RTCP-XR report from the callee's phone finds the record;
	// empty when no leg answered.
	PeerCallID string
	// PageLegs is a page's outcome per member (migration 00014), one row
	// each; empty for any other call.
	PageLegs []PageLeg
	// FollowMeLegs is what each follow-me target did (migration 00079), one
	// row each in cdr_legs with the role follow_me; empty for a call that
	// ran no follow-me.
	FollowMeLegs []FollowMeLeg
	// QueueLegs is what the call did in a call queue (migration 00040): one
	// row per event, in order; empty for a call that never queued.
	QueueLegs []QueueLeg
	// Events are the call.completed events of the call, one per extension it
	// involved. They are not stored: the Writer publishes them, carrying the
	// new row's id, once the record is in the table.
	Events []events.Event
}

// The SRTP column's values: which legs of an anchored call were encrypted.
const (
	SRTPNone   = "none"
	SRTPCaller = "caller"
	SRTPCallee = "callee"
	SRTPBoth   = "both"
)

// The SRTPKeyEx column's values.
const (
	KeyExNone  = "none"
	KeyExSDES  = "sdes"
	KeyExDTLS  = "dtls"
	KeyExMixed = "mixed"
)

// Queue leg events.
const (
	QueueEnter    = "enter"    // the caller joined the queue
	QueueAgent    = "agent"    // an agent was rung (Result: no_answer, busy, answered)
	QueueAnswered = "answered" // an agent answered
	QueueWait     = "wait"     // how long the caller waited (WaitMs)
	QueueAbandon  = "abandon"  // the caller hung up while waiting
	QueueExit     = "exit"     // the queue sent the caller elsewhere (Result: timeout, full, no_agents, breakout)

	// Callback events (migration 00049, pbx-gaps S-9, S-10).
	QueueCallbackRequested = "callback_requested" // the caller asked to be called back (Result: accepted)
	QueueCallbackDialled   = "callback_dialled"   // Hello rang the caller back
	QueueCallbackConnected = "callback_connected" // the caller and an agent were connected (Extension: the agent)
	QueueCallbackFailed    = "callback_failed"    // an attempt failed (Result: no_answer, busy, failed, abandoned)
)

// QueueLeg is one event of a call in a queue.
type QueueLeg struct {
	Event     string    `json:"event"`
	Queue     string    `json:"queue"`
	Extension string    `json:"extension"`
	WaitMs    int64     `json:"waitMs"`
	Result    string    `json:"result"`
	At        time.Time `json:"at"`
}

// Roles of a cdr_legs row (pbx-gaps S-22); follow-me writes follow_me.
const LegRoleFollowMe = "follow_me"

// FollowMeLeg is one follow-me leg: the target's kind (external, extension,
// ring_group or own), the extension it rang or the trunk it left on, and how
// it ended (answered, declined, busy, no_answer, failed, skipped or
// cancelled).
type FollowMeLeg struct {
	Kind      string `json:"kind"`
	Extension string `json:"extension"`
	Trunk     string `json:"trunk"`
	Result    string `json:"result"`
}

// PageLeg is one paged member's outcome: answered, busy, dnd, no_answer or
// no_registration. Device is the member contact's device user name, empty
// for a member that was never rung.
type PageLeg struct {
	Extension string `json:"extension"`
	Device    string `json:"device"`
	Result    string `json:"result"`
}

// EventSink receives events; *events.Emitter satisfies it.
type EventSink interface {
	Emit(events.Event)
}

// Writer is the bounded queue plus its background writer.
type Writer struct {
	db      momo.Querier
	log     *slog.Logger
	dropped prometheus.Counter
	queue   chan Record
	// Events, when set, receives each record's call.completed events after
	// the record is stored. Set it before Run.
	Events EventSink

	// MaxBackoff caps the retry delay; InitialBackoff is the first delay.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// NewDroppedCounter registers hello_cdr_dropped_total on reg.
func NewDroppedCounter(reg prometheus.Registerer) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "hello_cdr_dropped_total",
		Help: "CDRs dropped because the in-memory queue was full.",
	})
	reg.MustRegister(c)
	return c
}

// NewWriter returns a writer with a QueueSize queue that writes through db
// (internal/db); call Run to drain it.
func NewWriter(db momo.Querier, dropped prometheus.Counter, log *slog.Logger) *Writer {
	return newWriter(db, dropped, log, QueueSize)
}

func newWriter(db momo.Querier, dropped prometheus.Counter, log *slog.Logger, size int) *Writer {
	return &Writer{
		db: db, log: log, dropped: dropped, queue: make(chan Record, size),
		InitialBackoff: 100 * time.Millisecond, MaxBackoff: 30 * time.Second,
	}
}

// Enqueue queues r without blocking; it reports false, counting the drop,
// when the queue is full.
func (w *Writer) Enqueue(r Record) bool {
	select {
	case w.queue <- r:
		return true
	default:
		w.dropped.Inc()
		w.log.Warn("cdr queue full, record dropped", "correlation_id", r.CorrelationID)
		return false
	}
}

// insert writes a record and its page, follow-me and queue legs in one
// statement. The legs arrive as JSON arrays and are expanded with
// jsonb_array_elements ... WITH ORDINALITY, which momo has no builder for,
// so the statement stays hand-written; its binds are named (insertArgs), so
// a new column is one @name and one argument, with nothing renumbered. A
// retried record that was already stored is a no-op (ON CONFLICT DO
// NOTHING), and so are its legs. The rendered SQL is frozen in
// testdata/queries/postgres.sql (Queries, TestQueriesGolden).
const insert = `WITH c AS (INSERT INTO cdrs (correlation_id, sip_call_id, source, destination,
    start_time, ring_time, answer_time, end_time, duration_ms, billable_ms,
    sip_node, media_mode, final_status, termination_side, failure_reason,
    direction, original_destination, rewritten_destination, route_name, trunk_name, trace,
    rtp_packets, rtp_lost, rtp_jitter_ms, kind, ring_tag, ring_rule, caller_codec, callee_codec, origin, origin_client, peer_call_id, answered_by, srtp, srtp_keyex, ice, video)
VALUES (@correlation_id, @sip_call_id, @source, @destination,
    @start_time, @ring_time, @answer_time, @end_time, @duration_ms, @billable_ms,
    @sip_node, @media_mode, @final_status, @termination_side, @failure_reason,
    @direction, @original_destination, @rewritten_destination, @route_name, @trunk_name, @trace,
    @rtp_packets, @rtp_lost, @rtp_jitter_ms, @kind, @ring_tag, @ring_rule, @caller_codec, @callee_codec, @origin, @origin_client, @peer_call_id, @answered_by, @srtp, @srtp_keyex, @ice, @video)
ON CONFLICT (correlation_id) DO NOTHING RETURNING id),
p AS (INSERT INTO cdr_page_legs (cdr_id, position, extension, device, result)
SELECT c.id, l.ord, l.leg->>'extension', l.leg->>'device', l.leg->>'result'
FROM c, jsonb_array_elements(@page_legs::jsonb) WITH ORDINALITY AS l(leg, ord) RETURNING 1),
f AS (INSERT INTO cdr_legs (cdr_id, position, role, extension, trunk, result, target_kind)
SELECT c.id, l.ord, 'follow_me', l.leg->>'extension', l.leg->>'trunk', l.leg->>'result', l.leg->>'kind'
FROM c, jsonb_array_elements(@follow_me_legs::jsonb) WITH ORDINALITY AS l(leg, ord) RETURNING 1)
INSERT INTO cdr_queue_legs (cdr_id, position, event, queue, extension, wait_ms, result, at)
SELECT c.id, l.ord, l.leg->>'event', l.leg->>'queue', l.leg->>'extension', (l.leg->>'waitMs')::bigint,
       l.leg->>'result', (l.leg->>'at')::timestamptz
FROM c, jsonb_array_elements(@queue_legs::jsonb) WITH ORDINALITY AS l(leg, ord)`

// selectID reads the id of a stored record, for its call.completed events.
const selectID = `SELECT id FROM cdrs WHERE correlation_id = @correlation_id`

// Queries lists the SQL the writer issues, rendered without a database
// (momo.QueryRegistry), so TestQueriesGolden can freeze it. It is this
// package's own registry rather than momo's process-wide one, so the golden
// file holds the CDR statements only, whatever else the test binary links.
var Queries = momo.NewQueryRegistry()

func init() {
	Queries.Register("cdr.insert", func(q momo.Querier) momo.Compilable {
		args, err := insertArgs(Record{})
		if err != nil {
			panic(err) // a zero Record always marshals
		}
		return momo.RawQuery(q, insert, args...)
	})
	Queries.Register("cdr.select_id", func(q momo.Querier) momo.Compilable {
		return momo.RawQuery(q, selectID, momo.Named("correlation_id", ""))
	})
}

// Run writes queued records until ctx is cancelled, retrying each with
// exponential backoff. After cancellation it tries to flush what is left for
// up to flushTimeout.
func (w *Writer) Run(ctx context.Context, flushTimeout time.Duration) {
	for {
		select {
		case <-ctx.Done():
			w.flush(flushTimeout)
			return
		case r := <-w.queue:
			w.writeRetry(ctx, r)
		}
	}
}

func (w *Writer) writeRetry(ctx context.Context, r Record) {
	delay := w.InitialBackoff
	for {
		err := w.write(ctx, r)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			// Shutting down: put it back for the flush, best effort.
			select {
			case w.queue <- r:
			default:
				w.dropped.Inc()
			}
			return
		}
		w.log.Warn("cdr write failed, retrying", "correlation_id", r.CorrelationID, "error", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		delay = min(2*delay, w.MaxBackoff)
	}
}

func (w *Writer) flush(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		select {
		case r := <-w.queue:
			if err := w.write(ctx, r); err != nil {
				w.dropped.Inc()
				w.log.Warn("cdr lost at shutdown", "correlation_id", r.CorrelationID, "error", err)
			}
		default:
			return
		}
	}
}

func (w *Writer) write(ctx context.Context, r Record) error {
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args, err := insertArgs(r)
	if err != nil {
		return err
	}
	if _, err := momo.Exec(wctx, w.db, insert, args...); err != nil {
		return err
	}
	w.publish(wctx, r)
	return nil
}

// insertArgs binds r to the @names of insert. Zero values store as the
// column defaults; the legs are JSON arrays, empty for none.
func insertArgs(r Record) ([]any, error) {
	media := r.MediaMode
	if media == "" {
		media = "direct"
	}
	direction := r.Direction
	if direction == "" {
		direction = DirectionInternal
	}
	kind := r.Kind
	if kind == "" {
		kind = KindCall
	}
	srtp := r.SRTP
	if srtp == "" {
		srtp = SRTPNone
	}
	keyex := r.SRTPKeyEx
	if keyex == "" {
		keyex = KeyExNone
	}
	trace := r.Trace
	if trace == nil {
		trace = routing.Trace{}
	}
	tj, err := json.Marshal(trace)
	if err != nil {
		return nil, err
	}
	legs := r.PageLegs
	if legs == nil {
		legs = []PageLeg{}
	}
	lj, err := json.Marshal(legs)
	if err != nil {
		return nil, err
	}
	qlegs := r.QueueLegs
	if qlegs == nil {
		qlegs = []QueueLeg{}
	}
	qj, err := json.Marshal(qlegs)
	if err != nil {
		return nil, err
	}
	fj, err := json.Marshal(append([]FollowMeLeg{}, r.FollowMeLegs...))
	if err != nil {
		return nil, err
	}
	origin := r.Origin
	if origin == "" {
		origin = OriginPhone
	}
	return []any{
		momo.Named("correlation_id", r.CorrelationID),
		momo.Named("sip_call_id", r.SIPCallID),
		momo.Named("source", r.Source),
		momo.Named("destination", r.Destination),
		momo.Named("start_time", r.StartTime),
		momo.Named("ring_time", nullTime(r.RingTime)),
		momo.Named("answer_time", nullTime(r.AnswerTime)),
		momo.Named("end_time", r.EndTime),
		momo.Named("duration_ms", r.DurationMs),
		momo.Named("billable_ms", r.BillableMs),
		momo.Named("sip_node", r.SIPNode),
		momo.Named("media_mode", media),
		momo.Named("final_status", r.FinalStatus),
		momo.Named("termination_side", r.TerminationSide),
		momo.Named("failure_reason", r.FailureReason),
		momo.Named("direction", direction),
		momo.Named("original_destination", r.OriginalDestination),
		momo.Named("rewritten_destination", r.RewrittenDestination),
		momo.Named("route_name", r.Route),
		momo.Named("trunk_name", r.Trunk),
		momo.Named("trace", string(tj)),
		momo.Named("rtp_packets", r.RTPPackets),
		momo.Named("rtp_lost", r.RTPLost),
		momo.Named("rtp_jitter_ms", r.RTPJitterMs),
		momo.Named("kind", kind),
		momo.Named("ring_tag", r.RingTag),
		momo.Named("ring_rule", r.RingRule),
		momo.Named("caller_codec", nullString(r.CallerCodec)),
		momo.Named("callee_codec", nullString(r.CalleeCodec)),
		momo.Named("origin", origin),
		momo.Named("origin_client", r.OriginClient),
		momo.Named("peer_call_id", r.PeerCallID),
		momo.Named("answered_by", r.AnsweredBy),
		momo.Named("srtp", srtp),
		momo.Named("srtp_keyex", keyex),
		momo.Named("ice", r.ICE),
		momo.Named("video", r.Video),
		momo.Named("page_legs", string(lj)),
		momo.Named("follow_me_legs", string(fj)),
		momo.Named("queue_legs", string(qj)),
	}, nil
}

// publish emits the record's call.completed events with the stored row's
// id. A lookup that fails still emits them (cdrId 0): the event matters more
// than the id, and the call id finds the row.
func (w *Writer) publish(ctx context.Context, r Record) {
	if w.Events == nil || len(r.Events) == 0 {
		return
	}
	var id int64
	switch ids, err := momo.Scalars[int64](ctx, w.db, selectID, momo.Named("correlation_id", r.CorrelationID)); {
	case err != nil:
		w.log.Debug("cdr id lookup for events failed", "correlation_id", r.CorrelationID, "error", err)
	case len(ids) == 0:
		w.log.Debug("cdr id lookup for events failed", "correlation_id", r.CorrelationID, "error", "no row")
	default:
		id = ids[0]
	}
	for _, e := range r.Events {
		e.Data.CDRID = id
		w.Events.Emit(e)
	}
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
