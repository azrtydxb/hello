package slack

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/webhook"
)

// Worker timings.
const (
	DefaultPoll   = 2 * time.Second
	DefaultLease  = time.Minute
	DefaultBatch  = 10
	SucceededKeep = 7 * 24 * time.Hour
	DeadKeep      = 30 * 24 * time.Hour
	// pace spaces the posts to one destination: Slack allows about one
	// message per second per channel.
	pace = 1100 * time.Millisecond
)

// Metrics counts post attempts.
type Metrics struct{ Posts *prometheus.CounterVec }

// NewMetrics registers the counters on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{Posts: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_slack_posts_total", Help: "Slack post attempts by result (success, retry, dead, disabled).",
	}, []string{"result"})}
	if reg != nil {
		reg.MustRegister(m.Posts)
	}
	return m
}

func (m *Metrics) count(o Outcome) {
	if m == nil {
		return
	}
	m.Posts.WithLabelValues([]string{"success", "retry", "dead", "disabled"}[o]).Inc()
}

// Worker sends the due posts; one runs on every control replica and the
// store leases each row to one of them.
type Worker struct {
	Svc     *Service
	Metrics *Metrics
	Log     *slog.Logger
	Poll    time.Duration
	Batch   int
	Lease   time.Duration
	// Pace overrides the spacing between posts to one destination.
	Pace time.Duration
	// Rand is replaced in tests.
	Rand func() float64
}

func (w *Worker) rnd() float64 {
	if w.Rand != nil {
		return w.Rand()
	}
	return rand.Float64() //nolint:gosec // G404: retry jitter is not a secret
}

// Run sends posts until ctx ends and purges old ones hourly.
func (w *Worker) Run(ctx context.Context) {
	poll := w.Poll
	if poll <= 0 {
		poll = DefaultPoll
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	purge := time.NewTimer(time.Minute)
	defer purge.Stop()
	for {
		if n := w.Process(ctx); n >= w.batch() {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-purge.C:
			now := w.Svc.now()
			_, _ = w.Svc.Store.Purge(ctx, now.Add(-SucceededKeep), now.Add(-DeadKeep))
			purge.Reset(time.Hour)
		case <-t.C:
		}
	}
}

func (w *Worker) batch() int {
	if w.Batch > 0 {
		return w.Batch
	}
	return DefaultBatch
}

// Process claims what is due and sends it, returning the number attempted.
// Posts to different destinations go in parallel; posts to one destination go
// in order, paced.
func (w *Worker) Process(ctx context.Context) int {
	lease := w.Lease
	if lease <= 0 {
		lease = DefaultLease
	}
	claims, err := w.Svc.Store.Claim(ctx, w.Svc.now(), w.batch(), lease)
	if err != nil {
		if ctx.Err() == nil && w.Log != nil {
			w.Log.Warn("slack: claiming posts", "error", err)
		}
		return 0
	}
	by := map[int64][]Claim{}
	for _, c := range claims {
		by[c.DestinationID] = append(by[c.DestinationID], c)
	}
	var wg sync.WaitGroup
	for _, list := range by {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i, c := range list {
				if i > 0 {
					p := w.Pace
					if p == 0 {
						p = pace
					}
					select {
					case <-time.After(p):
					case <-ctx.Done():
						return
					}
				}
				w.deliver(ctx, c)
			}
		}()
	}
	wg.Wait()
	return len(claims)
}

func (w *Worker) deliver(ctx context.Context, c Claim) {
	f := Finish{PostID: c.ID, MaxFailures: MaxConsecutiveFailures}
	var ev events.Entry
	body, ok := []byte(nil), false
	if json.Unmarshal(c.Payload, &ev) == nil {
		body, ok = Body(ev.Event, w.Svc.PublicURL, c.Mode, c.Channel)
	}
	if !ok {
		f.Outcome, f.Error = OutcomeDead, "event_not_postable"
	} else {
		sctx, cancel := context.WithTimeout(ctx, RequestTimeout)
		r, err := w.Svc.send(sctx, c.DestinationID, c.Mode, c.SecretEnc, body)
		cancel()
		if err != nil {
			f.Outcome, f.Error = OutcomeDead, "secret_unreadable"
		} else {
			f.Outcome, f.Error, f.Reason = Classify(c.Mode, r)
			if f.Outcome == OutcomeRetry {
				if d, more := webhook.RetryDelay(c.SeriesAttempt+1, w.rnd); more {
					f.NextAttemptAt = w.Svc.now().Add(max(d, r.RetryAfter))
				} else {
					f.Outcome, f.Error = OutcomeDead, f.Error+"; retries exhausted"
				}
			}
		}
	}
	f.Now = w.Svc.now()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := w.Svc.Store.Finish(fctx, f); err != nil {
		if w.Log != nil {
			w.Log.Warn("slack: recording an attempt", "post", c.ID, "error", err)
		}
		return
	}
	w.Metrics.count(f.Outcome)
}
