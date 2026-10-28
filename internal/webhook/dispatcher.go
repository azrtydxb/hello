package webhook

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// Dispatcher defaults (spec I-42): 4 deliveries in flight per endpoint, 32
// per replica; dead letters are kept 30 days and at most 100,000 per endpoint.
const (
	DefaultPerEndpoint = 4
	DefaultPerReplica  = 32
	DefaultPoll        = time.Second
	DefaultLease       = 30 * time.Second
	DeadRetention      = 30 * 24 * time.Hour
	DeadCap            = 100000
	purgeLockKey       = "hello:webhooks:purge"
	gaugeEvery         = 15 * time.Second
)

// Dispatcher sends queued deliveries. One runs on every control replica:
// they claim rows through the store, which leases each row to one of them.
type Dispatcher struct {
	Svc *Service
	// Locker serialises the retention job across replicas; nil skips it.
	Locker events.Locker
	// Notify is told when an endpoint was switched off by the failures or a
	// 410 (to e-mail the administrators); it runs in its own goroutine.
	Notify  func(ctx context.Context, e Endpoint, reason string)
	Metrics *Metrics
	Log     *slog.Logger
	// Poll, PerEndpoint, PerReplica and Lease override the defaults.
	Poll        time.Duration
	PerEndpoint int
	PerReplica  int
	Lease       time.Duration
	// RetentionDays is how long finished deliveries are kept (7 when zero).
	RetentionDays int

	wg sync.WaitGroup
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return d.Log
}

func (d *Dispatcher) perEndpoint() int {
	if d.PerEndpoint > 0 {
		return d.PerEndpoint
	}
	return DefaultPerEndpoint
}

func (d *Dispatcher) perReplica() int {
	if d.PerReplica > 0 {
		return d.PerReplica
	}
	return DefaultPerReplica
}

func (d *Dispatcher) lease() time.Duration {
	if d.Lease > 0 {
		return d.Lease
	}
	return DefaultLease
}

// Run claims and sends deliveries until ctx ends, then waits for the ones in
// flight.
func (d *Dispatcher) Run(ctx context.Context) {
	poll := d.Poll
	if poll <= 0 {
		poll = DefaultPoll
	}
	var inflight sync.WaitGroup
	slots := make(chan struct{}, d.perReplica())
	go d.housekeeping(ctx)
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		free := cap(slots) - len(slots)
		if free > 0 {
			claims, err := d.Svc.Store.Claim(ctx, d.Svc.now(), free, d.perEndpoint(), d.lease())
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				d.log().Warn("webhooks: claiming deliveries", "error", err)
			}
			for _, c := range claims {
				slots <- struct{}{}
				inflight.Add(1)
				go func() {
					defer func() { <-slots; inflight.Done() }()
					d.deliver(ctx, c)
				}()
			}
			if len(claims) == free {
				continue // a full batch: more may be due, do not wait a tick
			}
		}
		select {
		case <-ctx.Done():
			inflight.Wait()
			d.wg.Wait()
			return
		case <-t.C:
		}
	}
	inflight.Wait()
	d.wg.Wait()
}

// RunOnce claims what is due and delivers it, waiting for the sends; it
// returns the number of deliveries attempted. Tests drive the dispatcher
// with it.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	claims, err := d.Svc.Store.Claim(ctx, d.Svc.now(), d.perReplica(), d.perEndpoint(), d.lease())
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, c := range claims {
		wg.Add(1)
		go func() { defer wg.Done(); d.deliver(ctx, c) }()
	}
	wg.Wait()
	d.wg.Wait()
	return len(claims), nil
}

// deliver makes one attempt for a claimed delivery and records it.
func (d *Dispatcher) deliver(ctx context.Context, c Claim) {
	ref := newRef()
	f := Finish{DeliveryID: c.ID, Ref: ref, MaxFailures: MaxConsecutiveFailures}
	secrets, serr := d.Svc.signingSecrets(c.EndpointID, c.SecretEnc, c.PreviousSecretEnc, c.PreviousSecretExpires)
	switch {
	case c.Payload == nil:
		// The event was purged while the delivery waited (a redelivery of a
		// very old letter): nothing can be sent, and it is not the
		// receiver's fault.
		f.Outcome, f.Error, f.Internal = OutcomeDead, "event_expired", true
	case serr != nil:
		f.Outcome, f.Error, f.Internal = OutcomeDead, "secret_unreadable", true
	default:
		r := d.Svc.send(ctx, c.URL, c.AllowPrivate, secrets, c.Payload, c.EventID, c.EventType, ref, 0, nil)
		f.Status, f.LatencyMs = r.Status, int(r.Latency.Milliseconds())
		f.Outcome = Classify(r.Status, r.Err)
		if f.Outcome != OutcomeSuccess {
			f.Error = ErrorText(r.Status, r.Err)
		}
		if f.Outcome == OutcomeRetry {
			if delay, ok := RetryDelay(c.SeriesAttempt+1, d.Svc.rnd); ok {
				f.NextAttemptAt = d.Svc.now().Add(max(delay, r.RetryAfter))
			} else {
				f.Outcome = OutcomeDead
				f.Error += "; retries exhausted"
			}
		}
		if d.Metrics != nil {
			d.Metrics.Seconds.Observe(r.Latency.Seconds())
		}
	}
	f.Now = d.Svc.now()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	res, err := d.Svc.Store.Finish(fctx, f)
	if err != nil {
		// The lease runs out and the delivery is sent again: at least once.
		d.log().Warn("webhooks: recording an attempt", "delivery", c.ID, "error", err)
		return
	}
	d.Metrics.result(f.Outcome)
	if res.Disabled {
		d.log().Warn("webhooks: endpoint disabled", "endpoint", res.Endpoint.ID, "reason", res.Reason)
		if d.Notify != nil {
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
				defer cancel()
				d.Notify(nctx, res.Endpoint, res.Reason)
			}()
		}
	}
}

// housekeeping refreshes the gauges and runs the retention job.
func (d *Dispatcher) housekeeping(ctx context.Context) {
	gauges := time.NewTicker(gaugeEvery)
	defer gauges.Stop()
	purge := time.NewTimer(time.Minute)
	defer purge.Stop()
	d.refreshGauges(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-gauges.C:
			d.refreshGauges(ctx)
		case <-purge.C:
			d.Purge(ctx)
			purge.Reset(time.Hour)
		}
	}
}

func (d *Dispatcher) refreshGauges(ctx context.Context) {
	if d.Metrics == nil {
		return
	}
	c, err := d.Svc.Store.Counts(ctx)
	if err != nil {
		return
	}
	d.Metrics.Pending.Set(float64(c.Pending))
	d.Metrics.Dead.Set(float64(c.Dead))
	d.Metrics.Disabled.Set(float64(c.DisabledEndpoints))
}

// Purge deletes finished deliveries past the retention and dead letters past
// 30 days or the per-endpoint cap, on one replica at a time.
func (d *Dispatcher) Purge(ctx context.Context) {
	if d.Locker != nil {
		unlock, ok, err := d.Locker.TryLock(ctx, purgeLockKey)
		if err != nil || !ok {
			return
		}
		defer unlock()
	}
	days := d.RetentionDays
	if days < 1 || days > 30 {
		days = events.DefaultRetentionDays
	}
	now := d.Svc.now()
	r, err := d.Svc.Store.Purge(ctx, now.AddDate(0, 0, -days), now.Add(-DeadRetention), DeadCap)
	if err != nil {
		d.log().Warn("webhooks: retention purge failed", "error", err)
		return
	}
	if d.Metrics != nil && r.Dropped > 0 {
		d.Metrics.Dropped.Add(float64(r.Dropped))
	}
	if r.Succeeded+r.Dead+r.Dropped > 0 {
		d.log().Info("webhooks: purged old deliveries", "succeeded", r.Succeeded, "dead", r.Dead, "dropped", r.Dropped)
	}
}
