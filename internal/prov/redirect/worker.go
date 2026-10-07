package redirect

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/prov"
)

// Worker timing (spec S-11): back-off doubles from retryBase to an hour,
// and a job is given up as failed 24 hours after it was first queued.
const (
	retryBase  = 30 * time.Second
	retryMax   = time.Hour
	giveUp     = 24 * time.Hour
	batchSize  = 50
	pollEvery  = 5 * time.Second
	driftEvery = 24 * time.Hour
)

// Worker works the prov_redirect_jobs queue and runs the daily drift
// check. hello-control runs it only while it holds the redirect lease.
type Worker struct {
	// Metrics counts hello_prov_redirect_ops_total (spec S-17): op is
	// register, unregister or lookup (the drift check), result ok or
	// error. NewWorker sets an unregistered set; hello-control replaces it
	// with the registered one before Run.
	Metrics *prov.Metrics

	store      Store
	deployment map[prov.Vendor]Credentials
	log        *slog.Logger

	hc         *http.Client
	now        func() time.Time
	poll       time.Duration
	driftEvery time.Duration
	clients    map[prov.Vendor]cachedClient // only touched by Run's goroutine
	lastDrift  time.Time                    // when the drift check last completed
	driftKnown bool                         // lastDrift was read from the store
}

type cachedClient struct {
	key    [32]byte // hash of the credentials and settings it was built with
	client Client
}

// NewWorker returns a worker over s; deployment holds the credentials the
// deployment sets (Deployment), which win over stored ones.
func NewWorker(s Store, deployment map[prov.Vendor]Credentials, log *slog.Logger) *Worker {
	return &Worker{
		Metrics: prov.NewMetrics(prometheus.NewRegistry()),
		store:   s, deployment: deployment, log: log,
		hc: &http.Client{Timeout: 30 * time.Second}, now: time.Now,
		poll: pollEvery, driftEvery: driftEvery, clients: map[prov.Vendor]cachedClient{},
	}
}

// op counts one redirect-service operation.
func (w *Worker) op(v prov.Vendor, op, result string) { w.Metrics.RedirectOp(v, op, result) }

// Run processes due jobs every few seconds and runs the drift check when
// the last one (kept by the store) is a day old, at start included, so a
// restart or a lease hand-over does not postpone it, until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	poll := time.NewTicker(w.poll)
	defer poll.Stop()
	for {
		w.work(ctx)
		if w.driftDue(ctx) {
			w.reconcile(ctx)
			w.driftDone(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
	}
}

// driftDue reports whether the drift check is due, reading when it last
// ran from the store once; a read error defers it to the next poll.
func (w *Worker) driftDue(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if !w.driftKnown {
		t, err := w.store.LastDriftCheck(ctx)
		if err != nil {
			w.log.Error("redirect: reading the last drift check", "err", err)
			return false
		}
		w.lastDrift, w.driftKnown = t, true
	}
	return w.now().Sub(w.lastDrift) >= w.driftEvery
}

// driftDone records a completed drift check. A failed write is logged and
// the time kept in memory, so the check is not repeated every poll.
func (w *Worker) driftDone(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	w.lastDrift = w.now()
	if err := w.store.SetLastDriftCheck(ctx, w.lastDrift); err != nil {
		w.log.Error("redirect: recording the drift check", "err", err)
	}
}

// work processes one batch of due jobs.
func (w *Worker) work(ctx context.Context) {
	jobs, err := w.store.DueJobs(ctx, batchSize)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Error("redirect: reading due jobs", "err", err)
		}
		return
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		w.process(ctx, j)
	}
}

// process runs one job. A store error leaves the job due, to be retried
// on the next poll; a vendor error schedules a retry with back-off.
func (w *Worker) process(ctx context.Context, j Job) {
	now := w.now()
	c, st, err := w.client(ctx, j.Vendor)
	if err != nil {
		w.log.Error("redirect: reading the account", "vendor", j.Vendor, "err", err)
		return
	}
	if c == nil {
		// Not configured or not supported: nothing to call. An unregister
		// leaves the status alone (the phone is gone or changed vendor).
		if j.Op == OpUnregister {
			w.drop(ctx, j)
			return
		}
		w.finish(ctx, j, st)
		return
	}
	switch j.Op {
	case OpRegister:
		t, err := w.store.Target(ctx, j.Vendor, j.MAC)
		if errors.Is(err, prov.ErrNotFound) {
			w.drop(ctx, j) // the phone is gone or has another vendor
			return
		}
		if err != nil {
			w.log.Error("redirect: reading the phone", "vendor", j.Vendor, "mac", j.MAC, "err", err)
			return
		}
		if c.Capabilities().NeedsSerial && strings.TrimSpace(t.Serial) == "" {
			w.op(j.Vendor, string(j.Op), "error")
			w.finish(ctx, j, Status{State: StateFailed, Reason: "the vendor needs the phone's serial number", At: now})
			return
		}
		err = c.Register(ctx, t.MAC, t.Serial, t.URL)
		if errors.Is(err, ErrRejected) {
			// Retrying cannot fix it: failed at once with the vendor's message.
			reason := redactURL(err.Error(), t.URL)
			w.op(j.Vendor, string(j.Op), "error")
			w.log.Warn("redirect: operation rejected", "vendor", j.Vendor, "op", j.Op, "mac", j.MAC, "err", reason)
			w.finish(ctx, j, Status{State: StateFailed, Reason: reason, At: now})
			return
		}
		if err != nil {
			w.failed(ctx, j, redactURL(err.Error(), t.URL))
			return
		}
		w.op(j.Vendor, string(j.Op), "ok")
		w.finish(ctx, j, Status{State: StateRegistered, At: now})
	case OpUnregister:
		if err := c.Unregister(ctx, j.MAC); err != nil {
			w.failed(ctx, j, err.Error())
			return
		}
		w.op(j.Vendor, string(j.Op), "ok")
		w.drop(ctx, j)
	default:
		w.log.Error("redirect: unknown job operation", "vendor", j.Vendor, "op", j.Op)
		w.drop(ctx, j)
	}
}

// failed retries j with exponential back-off, or gives up as failed once
// it has been queued for 24 hours.
func (w *Worker) failed(ctx context.Context, j Job, reason string) {
	w.op(j.Vendor, string(j.Op), "error")
	now := w.now()
	w.log.Warn("redirect: operation failed", "vendor", j.Vendor, "op", j.Op, "mac", j.MAC, "attempt", j.Attempts+1, "err", reason)
	if now.Sub(j.FirstQueuedAt) >= giveUp {
		w.finish(ctx, j, Status{State: StateFailed, Reason: reason, At: now})
		return
	}
	var st *Status // an unregister leaves the phone's status alone
	if j.Op != OpUnregister {
		st = &Status{State: StatePending, Reason: reason, At: now}
	}
	if err := w.store.RetryJob(ctx, j, now.Add(backoff(j.Attempts)), st); err != nil {
		w.log.Error("redirect: scheduling a retry", "vendor", j.Vendor, "mac", j.MAC, "err", err)
	}
}

// finish removes j and sets the phone's status.
func (w *Worker) finish(ctx context.Context, j Job, st Status) {
	w.finishJob(ctx, j, &st)
}

// drop removes j without writing the phone's status.
func (w *Worker) drop(ctx context.Context, j Job) {
	w.finishJob(ctx, j, nil)
}

func (w *Worker) finishJob(ctx context.Context, j Job, st *Status) {
	if err := w.store.FinishJob(ctx, j, st); err != nil {
		w.log.Error("redirect: finishing a job", "vendor", j.Vendor, "mac", j.MAC, "err", err)
	}
}

// backoff is the wait after the given number of earlier attempts:
// 30 s, 1 min, 2 min, ... capped at an hour.
func backoff(attempts int) time.Duration {
	d := retryBase
	for range max(attempts, 0) {
		if d >= retryMax {
			break
		}
		d *= 2
	}
	return min(d, retryMax)
}

// redactURL keeps the phone's provisioning URL (its token) out of a status
// reason and the log, should a vendor echo it.
func redactURL(msg, u string) string {
	if u == "" {
		return msg
	}
	return strings.ReplaceAll(msg, u, "[url]")
}

// client returns the vendor's client, or nil and the status to record when
// there is nothing to call: not_configured without credentials or with a
// disabled account, manual for a vendor Hello cannot drive.
func (w *Worker) client(ctx context.Context, v prov.Vendor) (Client, Status, error) {
	now := w.now()
	creds, fromDeployment := w.deployment[v]
	var settings []byte
	acct, err := w.store.Account(ctx, v)
	switch {
	case errors.Is(err, prov.ErrNotFound):
	case err != nil:
		return nil, Status{}, err
	default:
		settings = acct.Settings
		if !fromDeployment {
			if !acct.Enabled {
				creds = nil
			} else {
				creds = acct.Credentials
			}
		}
	}
	if !drivable(v) {
		return nil, Status{State: StateManual, At: now}, nil
	}
	if len(creds) == 0 {
		return nil, Status{State: StateNotConfigured, At: now}, nil
	}
	raw, _ := json.Marshal(creds)
	key := sha256.Sum256(append(append(raw, 0), settings...))
	if cc, ok := w.clients[v]; ok && cc.key == key {
		return cc.client, Status{}, nil
	}
	c, err := New(v, creds, settings, w.hc)
	if err != nil {
		// Unusable credentials or settings: record why, without a call.
		return nil, Status{State: StateFailed, Reason: err.Error(), At: now}, nil
	}
	w.clients[v] = cachedClient{key: key, client: c}
	return c, Status{}, nil
}

// reconcile is the daily drift check: every phone registered with a vendor
// that keeps a per-device URL is looked up, and one whose URL differs (or
// is gone) is marked failed: drift.
func (w *Worker) reconcile(ctx context.Context) {
	for _, v := range []prov.Vendor{prov.Snom, prov.Yealink, prov.Grandstream} {
		c, st, err := w.client(ctx, v)
		switch {
		case err != nil:
			w.log.Error("redirect: drift check skipped: reading the account", "vendor", v, "err", err)
			continue
		case c == nil && st.State == StateFailed:
			w.log.Warn("redirect: drift check skipped: unusable credentials or settings", "vendor", v, "err", st.Reason)
			continue
		case c == nil || !c.Capabilities().RegistersURL:
			continue // not configured, or no per-device URL to read back
		}
		targets, err := w.store.Registered(ctx, v)
		if err != nil {
			w.log.Error("redirect: listing registered phones", "vendor", v, "err", err)
			continue
		}
		for _, t := range targets {
			if ctx.Err() != nil {
				return
			}
			u, found, err := c.Lookup(ctx, t.MAC)
			if errors.Is(err, ErrUnsupported) {
				break
			}
			if err != nil {
				w.op(v, "lookup", "error")
				w.log.Warn("redirect: drift check failed", "vendor", v, "mac", t.MAC, "err", redactURL(err.Error(), t.URL))
				continue
			}
			w.op(v, "lookup", "ok")
			if found && u == t.URL {
				continue
			}
			if err := w.store.SetStatus(ctx, t.MAC, Status{State: StateFailed, Reason: "drift", At: w.now()}); err != nil {
				w.log.Error("redirect: recording drift", "vendor", v, "mac", t.MAC, "err", err)
			}
		}
	}
}
