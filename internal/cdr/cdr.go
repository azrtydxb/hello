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

	"github.com/azrtydxb/hello/internal/routing"
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

const insert = `INSERT INTO cdrs (correlation_id, sip_call_id, source, destination,
    start_time, ring_time, answer_time, end_time, duration_ms, billable_ms,
    sip_node, media_mode, final_status, termination_side, failure_reason,
    direction, original_destination, rewritten_destination, route_name, trunk_name, trace,
    rtp_packets, rtp_lost, rtp_jitter_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
    $16, $17, $18, $19, $20, $21, $22, $23, $24)
ON CONFLICT (correlation_id) DO NOTHING`

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
	trace := r.Trace
	if trace == nil {
		trace = routing.Trace{}
	}
	tj, err := json.Marshal(trace)
	if err != nil {
		return err
	}
	_, err = w.db.Exec(wctx, insert, r.CorrelationID, r.SIPCallID, r.Source, r.Destination,
		r.StartTime, nullTime(r.RingTime), nullTime(r.AnswerTime), r.EndTime, r.DurationMs, r.BillableMs,
		r.SIPNode, media, r.FinalStatus, r.TerminationSide, r.FailureReason,
		direction, r.OriginalDestination, r.RewrittenDestination, r.Route, r.Trunk, string(tj),
		r.RTPPackets, r.RTPLost, r.RTPJitterMs)
	return err
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
