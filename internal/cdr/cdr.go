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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	// Kind is call (default), page or intercom.
	Kind string
	// AnsweredBy is the extension that answered when it was not the one
	// dialled: the assistant of a boss link (migration 00046). Empty otherwise.
	AnsweredBy string
	// PageLegs is a page's outcome per member (migration 00014), one row
	// each; empty for any other call.
	PageLegs []PageLeg
	// QueueLegs is what the call did in a call queue (migration 00040): one
	// row per event, in order; empty for a call that never queued.
	QueueLegs []QueueLeg
	// Events are the call.completed events of the call, one per extension it
	// involved. They are not stored: the Writer publishes them, carrying the
	// new row's id, once the record is in the table.
	Events []events.Event
}

// Queue leg events.
const (
	QueueEnter    = "enter"    // the caller joined the queue
	QueueAgent    = "agent"    // an agent was rung (Result: no_answer, busy, answered)
	QueueAnswered = "answered" // an agent answered
	QueueWait     = "wait"     // how long the caller waited (WaitMs)
	QueueAbandon  = "abandon"  // the caller hung up while waiting
	QueueExit     = "exit"     // the queue sent the caller elsewhere (Result: timeout, full, no_agents, breakout)
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

// rowQuerier is the optional part of a pgx pool the Writer uses to read the
// id of the row it inserted (only for the call.completed events).
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Execer is the subset of a pgx pool or connection the writer needs.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Writer is the bounded queue plus its background writer.
type Writer struct {
	db      Execer
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

// NewWriter returns a writer with a QueueSize queue; call Run to drain it.
func NewWriter(db Execer, dropped prometheus.Counter, log *slog.Logger) *Writer {
	return newWriter(db, dropped, log, QueueSize)
}

func newWriter(db Execer, dropped prometheus.Counter, log *slog.Logger, size int) *Writer {
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

const insert = `WITH c AS (INSERT INTO cdrs (correlation_id, sip_call_id, source, destination,
    start_time, ring_time, answer_time, end_time, duration_ms, billable_ms,
    sip_node, media_mode, final_status, termination_side, failure_reason,
    direction, original_destination, rewritten_destination, route_name, trunk_name, trace,
    rtp_packets, rtp_lost, rtp_jitter_ms, kind, answered_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
    $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $28)
ON CONFLICT (correlation_id) DO NOTHING RETURNING id),
p AS (INSERT INTO cdr_page_legs (cdr_id, position, extension, device, result)
SELECT c.id, l.ord, l.leg->>'extension', l.leg->>'device', l.leg->>'result'
FROM c, jsonb_array_elements($26::jsonb) WITH ORDINALITY AS l(leg, ord) RETURNING 1)
INSERT INTO cdr_queue_legs (cdr_id, position, event, queue, extension, wait_ms, result, at)
SELECT c.id, l.ord, l.leg->>'event', l.leg->>'queue', l.leg->>'extension', (l.leg->>'waitMs')::bigint,
       l.leg->>'result', (l.leg->>'at')::timestamptz
FROM c, jsonb_array_elements($27::jsonb) WITH ORDINALITY AS l(leg, ord)`

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
	trace := r.Trace
	if trace == nil {
		trace = routing.Trace{}
	}
	tj, err := json.Marshal(trace)
	if err != nil {
		return err
	}
	legs := r.PageLegs
	if legs == nil {
		legs = []PageLeg{}
	}
	lj, err := json.Marshal(legs)
	if err != nil {
		return err
	}
	qlegs := r.QueueLegs
	if qlegs == nil {
		qlegs = []QueueLeg{}
	}
	qj, err := json.Marshal(qlegs)
	if err != nil {
		return err
	}
	_, err = w.db.Exec(wctx, insert, r.CorrelationID, r.SIPCallID, r.Source, r.Destination,
		r.StartTime, nullTime(r.RingTime), nullTime(r.AnswerTime), r.EndTime, r.DurationMs, r.BillableMs,
		r.SIPNode, media, r.FinalStatus, r.TerminationSide, r.FailureReason,
		direction, r.OriginalDestination, r.RewrittenDestination, r.Route, r.Trunk, string(tj),
		r.RTPPackets, r.RTPLost, r.RTPJitterMs, kind, string(lj), string(qj), r.AnsweredBy)
	if err != nil {
		return err
	}
	w.publish(wctx, r)
	return nil
}

// publish emits the record's call.completed events with the stored row's
// id. A lookup that fails still emits them (cdrId 0): the event matters more
// than the id, and the call id finds the row.
func (w *Writer) publish(ctx context.Context, r Record) {
	if w.Events == nil || len(r.Events) == 0 {
		return
	}
	var id int64
	if q, ok := w.db.(rowQuerier); ok {
		if err := q.QueryRow(ctx, `SELECT id FROM cdrs WHERE correlation_id = $1`, r.CorrelationID).Scan(&id); err != nil {
			w.log.Debug("cdr id lookup for events failed", "correlation_id", r.CorrelationID, "error", err)
		}
	}
	for _, e := range r.Events {
		e.Data.CDRID = id
		w.Events.Emit(e)
	}
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
