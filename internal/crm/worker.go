package crm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// LinkMinter mints and revokes the recording links written into a CRM
// (spec I-33, I-53). The recording-link service of integrations phase P4
// satisfies it; without one, recordings are not sent to a CRM.
type LinkMinter interface {
	// Mint returns a public https link to the recording that lives ttl.
	Mint(ctx context.Context, recordingID int64, label string, ttl time.Duration) (url string, expires time.Time, err error)
	// RevokeAll revokes every link minted with label.
	RevokeAll(ctx context.Context, label string) error
}

// LinkLabel is the label a connector's links carry.
func LinkLabel(connectorID int64) string { return "crm:" + strconv.FormatInt(connectorID, 10) }

// RenewWindow is how long before expiry a recording link is renewed, and
// RenewMaxAge how old a call may be for its link to be renewed.
const (
	RenewWindow = 5 * 24 * time.Hour
	RenewMaxAge = 180 * 24 * time.Hour
)

// Worker runs the logging jobs: it claims due rows, resolves the owner,
// writes the call (or patches it) and settles the row.
type Worker struct {
	Repo    Repo
	Tokens  *TokenManager
	Links   LinkMinter
	Metrics *Metrics
	Log     *slog.Logger
	Now     func() time.Time
	// Rnd spreads retry delays; nil uses math/rand.
	Rnd func() float64
	// Batch is the jobs claimed per pass (20), Interval the wait between
	// passes (2 s) and Lease how long a claimed job stays invisible to other
	// replicas (2 min).
	Batch    int
	Interval time.Duration
	Lease    time.Duration

	lastStats time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (w *Worker) rnd() float64 {
	if w.Rnd != nil {
		return w.Rnd()
	}
	return rand.Float64() //nolint:gosec // retry jitter
}

// Run processes jobs until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	iv := w.Interval
	if iv <= 0 {
		iv = 2 * time.Second
	}
	for ctx.Err() == nil {
		if w.Process(ctx) == 0 {
			t := time.NewTimer(iv)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		if w.Metrics != nil && w.now().Sub(w.lastStats) > 30*time.Second {
			w.lastStats = w.now()
			w.Metrics.Refresh(ctx, w.Repo)
		}
	}
}

// Process claims and settles one batch; it returns the jobs handled.
func (w *Worker) Process(ctx context.Context) int {
	batch := w.Batch
	if batch <= 0 {
		batch = 20
	}
	lease := w.Lease
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	rows, err := w.Repo.ClaimDue(ctx, batch, lease)
	if err != nil {
		w.log().Warn("crm: claiming jobs", "error", err)
		return 0
	}
	for _, r := range rows {
		w.handle(ctx, r)
	}
	return len(rows)
}

// settle writes the outcome of one attempt.
func (w *Worker) settle(ctx context.Context, row LogRow, res LogResult) {
	res.RecordingSeen = row.RecordingID
	if err := w.Repo.Finish(ctx, row.ID, res); err != nil {
		w.log().Error("crm: settling a job", "log", row.ID, "error", err)
	}
}

// fail records a failed attempt: a retry with backoff, or the dead state.
func (w *Worker) fail(ctx context.Context, row LogRow, err error) {
	var ae *APIError
	permanent := errors.As(err, &ae) && !ae.Retry
	n := row.Attempts + 1
	delay, more := RetryDelay(n, w.rnd)
	if ae != nil && ae.After > 0 {
		delay = min(ae.After, MaxRetryAfter)
	}
	msg := scrub(err.Error())
	res := LogResult{Status: row.Status, Attempts: n, LastError: msg, NextAttemptAt: w.now().Add(delay)}
	if permanent || !more {
		res.Status, res.NextAttemptAt = LogDead, w.now()
	}
	w.log().Warn("crm: job failed", "log", row.ID, "connector", row.ConnectorID, "attempt", n, "dead", res.Status == LogDead, "error", msg)
	w.settle(ctx, row, res)
}

// scrub keeps an error message short and single-line for the UI.
func scrub(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func (w *Worker) handle(ctx context.Context, row LogRow) {
	cr, err := w.Repo.GetConnector(ctx, row.ConnectorID)
	if err != nil {
		w.fail(ctx, row, err)
		return
	}
	conn, cc, err := w.Tokens.Conn(ctx, cr)
	if err != nil {
		if errors.Is(err, ErrNeedsReauth) {
			// Not an attempt: the job waits for an administrator to connect
			// again, and ClaimDue skips the connector meanwhile.
			w.settle(ctx, row, LogResult{Status: row.Status, Attempts: row.Attempts, NextAttemptAt: w.now().Add(5 * time.Minute), LastError: "the connection needs re-authorisation"})
			return
		}
		w.fail(ctx, row, err)
		return
	}
	if len(row.Payload) == 0 || string(row.Payload) == "null" {
		w.settle(ctx, row, LogResult{Status: LogDead, Attempts: row.Attempts, NextAttemptAt: w.now(), LastError: "no call data"})
		return
	}
	var d events.Data
	if err := json.Unmarshal(row.Payload, &d); err != nil {
		w.settle(ctx, row, LogResult{Status: LogDead, Attempts: row.Attempts, NextAttemptAt: w.now(), LastError: "unreadable call data"})
		return
	}
	call := BuildCallLog(row.EventType, d)
	caps := conn.Capabilities()

	// The CRM user that owns the call.
	owner, skip, err := w.owner(ctx, cr, conn, &cc, row)
	if err != nil {
		w.fail(ctx, row, err)
		return
	}
	if skip != "" {
		w.settle(ctx, row, LogResult{Status: LogSkipped, Attempts: row.Attempts, NextAttemptAt: w.now(), LastError: skip})
		return
	}
	call.OwnerID = owner

	// The contact, best effort: a call is logged without one rather than
	// not at all.
	if caps.Lookup {
		start := w.now()
		c, lerr := withAuth(ctx, w, cr, &cc, func(cc Conn) (*Contact, error) { return conn.Lookup(ctx, cc, call.Number) })
		w.Metrics.lookup(cr.Kind, w.now().Sub(start))
		w.Metrics.request(cr.Kind, "lookup", lerr)
		if lerr == nil {
			call.Contact = c
		} else if errors.Is(lerr, ErrNeedsReauth) {
			w.settle(ctx, row, LogResult{Status: row.Status, Attempts: row.Attempts, NextAttemptAt: w.now().Add(5 * time.Minute), LastError: "the connection needs re-authorisation"})
			return
		}
	}

	// The recording link.
	var linkExpires *time.Time
	if row.RecordingID != 0 && cr.Recordings && caps.Recordings && w.Links != nil &&
		(row.LinkExpiresAt == nil || row.LinkExpiresAt.Before(w.now().Add(RenewWindow))) {
		ttl := time.Duration(cr.LinkTTLHours) * time.Hour
		url, exp, merr := w.Links.Mint(ctx, row.RecordingID, LinkLabel(cr.ID), ttl)
		if merr != nil {
			w.fail(ctx, row, fmt.Errorf("minting the recording link: %w", merr))
			return
		}
		call.RecordingURL, linkExpires = url, &exp
	}

	ref := row.ExternalID
	if ref == "" {
		ref, err = withAuth(ctx, w, cr, &cc, func(cc Conn) (string, error) { return conn.LogCall(ctx, cc, call) })
		w.Metrics.request(cr.Kind, "log", err)
	} else {
		_, err = withAuth(ctx, w, cr, &cc, func(cc Conn) (struct{}, error) {
			return struct{}{}, conn.UpdateCallLog(ctx, cc, ref, call)
		})
		w.Metrics.request(cr.Kind, "update", err)
	}
	if err != nil {
		if errors.Is(err, ErrNeedsReauth) {
			w.settle(ctx, row, LogResult{Status: row.Status, Attempts: row.Attempts, NextAttemptAt: w.now().Add(5 * time.Minute), LastError: "the connection needs re-authorisation"})
			return
		}
		w.fail(ctx, row, err)
		return
	}
	w.settle(ctx, row, LogResult{Status: LogLogged, ExternalID: ref, NextAttemptAt: w.now(), LinkExpiresAt: linkExpires})
}

// withAuth runs f with the connection's token; when the CRM refuses the
// token it refreshes once and runs f again. A second refusal marks the
// connection needs_reauth.
func withAuth[T any](ctx context.Context, w *Worker, cr ConnectorRow, cc *Conn, f func(Conn) (T, error)) (T, error) {
	v, err := f(*cc)
	if !errors.Is(err, ErrAuth) {
		return v, err
	}
	nc, rerr := w.Tokens.Refresh(ctx, cr)
	if rerr != nil {
		var zero T
		return zero, rerr
	}
	*cc = nc
	v, err = f(nc)
	if errors.Is(err, ErrAuth) {
		w.Tokens.MarkNeedsReauth(ctx, cr, "the CRM refused a freshly refreshed token")
		var zero T
		return zero, ErrNeedsReauth
	}
	return v, err
}

// owner resolves the CRM user for the call's extension: a stored mapping,
// else the CRM user with the Hello user's email (never a display name),
// else the connector's fallback owner. skip is the reason a call is left
// out when none applies.
func (w *Worker) owner(ctx context.Context, cr ConnectorRow, conn Connector, cc *Conn, row LogRow) (owner, skip string, err error) {
	fallback := func(why string) (string, string, error) {
		if cr.FallbackOwner != "" {
			return cr.FallbackOwner, "", nil
		}
		return "", why, nil
	}
	if row.OwnerExtension == "" {
		return fallback("no extension owns this call")
	}
	u, ok, err := w.Repo.UserForExtension(ctx, row.OwnerExtension)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return fallback("extension " + row.OwnerExtension + " belongs to no user")
	}
	maps, err := w.Repo.Mappings(ctx, cr.ID)
	if err != nil {
		return "", "", err
	}
	for _, m := range maps {
		if m.UserID == u.ID {
			return m.ExternalUserID, "", nil
		}
	}
	if u.Email == "" {
		return fallback("user " + u.Username + " has no email to match a CRM user")
	}
	id, rerr := withAuth(ctx, w, cr, cc, func(cc Conn) (string, error) { return conn.ResolveUser(ctx, cc, u.Email) })
	w.Metrics.request(cr.Kind, "owner", rerr)
	if rerr != nil {
		return "", "", rerr
	}
	if id == "" {
		return fallback("no CRM user has the email of " + u.Username)
	}
	if err := w.Repo.SetMapping(ctx, cr.ID, u.ID, id, "email"); err != nil {
		return "", "", err
	}
	return id, "", nil
}

// Renewer marks recording links about to expire for renewal (spec I-53) and
// removes stale OAuth states. It is idempotent, so replicas may all run it.
type Renewer struct {
	Repo     Repo
	Log      *slog.Logger
	Now      func() time.Time
	Interval time.Duration
}

// Run ticks hourly until ctx ends.
func (r *Renewer) Run(ctx context.Context) {
	iv := r.Interval
	if iv <= 0 {
		iv = time.Hour
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		r.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once runs one pass.
func (r *Renewer) Once(ctx context.Context) {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	n, err := r.Repo.MarkRenewals(ctx, now, RenewWindow, RenewMaxAge)
	if err != nil && r.Log != nil {
		r.Log.Warn("crm: renewing recording links", "error", err)
	}
	if n > 0 && r.Log != nil {
		r.Log.Info("crm: recording links queued for renewal", "count", n)
	}
	_ = r.Repo.PurgeStates(ctx, now)
}
