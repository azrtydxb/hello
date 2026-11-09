package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"
)

// Stream is the Valkey stream every event is appended to; StreamMaxLen is its
// approximate length cap (XADD MAXLEN ~).
const (
	StreamKey    = "hello:events"
	StreamMaxLen = 10000
	// streamField is the stream entry field holding the JSON event.
	streamField = "e"
)

// QueueSize is the number of events buffered while Valkey is slow.
const QueueSize = 1000

// appendTimeout bounds one XADD.
const appendTimeout = 2 * time.Second

// EmitterMetrics are hello-sip's event series.
type EmitterMetrics struct {
	Emitted *prometheus.CounterVec
	Dropped prometheus.Counter
}

// NewEmitterMetrics registers hello_events_emitted_total and
// hello_events_dropped_total on reg.
func NewEmitterMetrics(reg prometheus.Registerer) *EmitterMetrics {
	m := &EmitterMetrics{
		Emitted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_events_emitted_total", Help: "Call events appended to the event stream, by type."}, []string{"type"}),
		Dropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hello_events_dropped_total", Help: "Call events dropped because the queue was full or the stream unreachable."}),
	}
	reg.MustRegister(m.Emitted, m.Dropped)
	return m
}

// Appender appends one encoded event to the stream.
type Appender interface {
	Append(ctx context.Context, payload []byte) error
}

// ValkeyAppender appends to the Valkey stream.
type ValkeyAppender struct{ C valkey.Client }

// Append implements Appender (XADD hello:events MAXLEN ~ 10000 * e <json>).
func (a ValkeyAppender) Append(ctx context.Context, payload []byte) error {
	return a.C.Do(ctx, a.C.B().Arbitrary("XADD").Keys(StreamKey).
		Args("MAXLEN", "~", "10000", "*", streamField, string(payload)).Build()).Error()
}

// Emitter queues events without blocking and appends them from a background
// goroutine, like the CDR writer. A full queue drops the new event, and so
// does an unreachable stream: SIP never waits.
type Emitter struct {
	app   Appender
	log   *slog.Logger
	m     *EmitterMetrics
	queue chan Event
}

// NewEmitter returns an emitter with a QueueSize queue; call Run to drain it.
// m may be nil.
func NewEmitter(app Appender, m *EmitterMetrics, log *slog.Logger) *Emitter {
	return newEmitter(app, m, log, QueueSize)
}

func newEmitter(app Appender, m *EmitterMetrics, log *slog.Logger, size int) *Emitter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Emitter{app: app, m: m, log: log, queue: make(chan Event, size)}
}

// Emit queues e. It never blocks; a full queue drops e and counts it.
func (e *Emitter) Emit(ev Event) {
	select {
	case e.queue <- ev:
	default:
		e.drop(ev, "queue full")
	}
}

func (e *Emitter) drop(ev Event, why string) {
	if e.m != nil {
		e.m.Dropped.Inc()
	}
	e.log.Warn("event dropped", "type", ev.Type, "reason", why)
}

// Run appends queued events until ctx ends, then flushes what is left for a
// short while.
func (e *Emitter) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			e.flush()
			return
		case ev := <-e.queue:
			e.send(ctx, ev)
		}
	}
}

func (e *Emitter) flush() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		select {
		case ev := <-e.queue:
			e.send(ctx, ev)
		default:
			return
		}
	}
}

func (e *Emitter) send(ctx context.Context, ev Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		e.drop(ev, "encode")
		return
	}
	actx, cancel := context.WithTimeout(ctx, appendTimeout)
	defer cancel()
	if err := e.app.Append(actx, b); err != nil {
		e.drop(ev, "stream unreachable")
		e.log.Debug("event append failed", "error", err)
		return
	}
	if e.m != nil {
		e.m.Emitted.WithLabelValues(ev.Type).Inc()
	}
}
