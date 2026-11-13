package fax

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/mailer"
)

// MailQueue is the persistence of fax email delivery; *store.Store provides it.
type MailQueue interface {
	ClaimFaxMail(ctx context.Context, limit int, stale time.Duration) ([]MailJob, error)
	FinishFaxMail(ctx context.Context, id int64, errText string, final bool, retryAt time.Time) error
}

// Delivery emails received faxes. It runs beside call handling on every
// hello-control node; the queue claims each fax for one node at a time, so a
// cluster sends each email once.
type Delivery struct {
	Queue    MailQueue
	Objects  Objects
	Settings mailer.SettingsFunc
	// Deliver sends one built message; nil uses SMTP.
	Deliver func(ctx context.Context, s mailer.Settings, to string, msg []byte) error
	Log     *slog.Logger
	// Poll is how often the queue is checked (10 s when zero).
	Poll time.Duration
	// Timeout bounds one download and one send (30 s when zero).
	Timeout time.Duration
	Now     func() time.Time
}

const mailBatch = 10

func (d *Delivery) defaults() {
	if d.Poll <= 0 {
		d.Poll = 10 * time.Second
	}
	if d.Timeout <= 0 {
		d.Timeout = 30 * time.Second
	}
	if d.Deliver == nil {
		d.Deliver = mailer.Deliver
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
}

// mailBackoff is the wait before retry n: a minute, doubling to an hour.
func mailBackoff(attempt int) time.Duration {
	d := time.Minute
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// Run works the queue until ctx ends.
func (d *Delivery) Run(ctx context.Context) {
	d.defaults()
	t := time.NewTicker(d.Poll)
	defer t.Stop()
	for {
		d.Process(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Process is one pass: claim due faxes and send each. Without SMTP settings it
// does nothing, so faxes wait for them.
func (d *Delivery) Process(ctx context.Context) {
	d.defaults()
	st, ok, err := d.Settings(ctx)
	if err != nil {
		d.Log.Warn("fax email: read SMTP settings", "error", err)
		return
	}
	if !ok {
		return
	}
	jobs, err := d.Queue.ClaimFaxMail(ctx, mailBatch, 3*d.Timeout)
	if err != nil {
		d.Log.Warn("fax email: claim", "error", err)
		return
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		d.send(ctx, st, j)
	}
}

func (d *Delivery) send(ctx context.Context, st mailer.Settings, j MailJob) {
	log := d.Log.With("fax_id", j.ID, "attempt", j.Attempts)
	finish := func(errText string, final bool) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := d.Queue.FinishFaxMail(fctx, j.ID, errText, final, d.Now().Add(mailBackoff(j.Attempts))); err != nil {
			log.Warn("fax email: record result", "error", err)
		}
	}
	if len(j.To) == 0 {
		finish("no email address for extension "+j.Owner, true)
		return
	}
	last := j.Attempts >= MailAttempts
	gctx, cancel := context.WithTimeout(ctx, d.Timeout)
	pdf, err := d.Objects.Get(gctx, j.Object)
	cancel()
	if err != nil {
		log.Warn("fax email: fetch document", "error", err)
		finish("fetch the document: "+trimErr(err), last)
		return
	}
	var failed []string
	for _, to := range j.To {
		msg, err := mailer.BuildFaxMessage(st.From, mailer.FaxMessage{To: to, Peer: j.Peer, Pages: j.Pages,
			Received: j.CreatedAt, Name: AttachmentName(j.ID, j.CreatedAt), PDF: pdf})
		if err != nil {
			log.Error("fax email: build message", "error", err)
			finish("build the message: "+trimErr(err), true)
			return
		}
		sctx, cancel := context.WithTimeout(ctx, d.Timeout)
		err = d.Deliver(sctx, st, to, msg)
		cancel()
		if err != nil {
			log.Warn("fax email: send failed", "error", err)
			failed = append(failed, trimErr(err))
		}
	}
	if len(failed) > 0 {
		finish(strings.Join(failed, "; "), last)
		return
	}
	finish("", false)
}

func trimErr(err error) string {
	s := err.Error()
	if len(s) > 180 {
		s = s[:180]
	}
	return s
}

// RetentionStore is what the retention job needs from the fax table.
type RetentionStore interface {
	ExpiredFaxes(ctx context.Context, days, limit int) (ids []int64, keys []string, err error)
	DeleteFaxRows(ctx context.Context, ids []int64) error
	ExpireStuckFaxes(ctx context.Context, olderThan time.Duration) (int64, error)
}

// Locker takes a cluster-wide lock (store.Store.TryLock).
type Locker interface {
	TryLock(ctx context.Context, key string) (func(), bool, error)
}

// Retention deletes faxes past the retention period (pbx-gaps S-17): in
// batches of 500, one node at a time, the objects before the rows.
type Retention struct {
	Store   RetentionStore
	Objects Objects
	Locker  Locker
	Days    int
	Log     *slog.Logger
	Every   time.Duration
}

const retentionLock = "fax-retention"

// Run purges every Every (an hour when zero) until ctx ends.
func (r *Retention) Run(ctx context.Context) {
	every := r.Every
	if every <= 0 {
		every = time.Hour
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	}
	for {
		r.Purge(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Purge runs one pass and returns how many faxes it removed.
func (r *Retention) Purge(ctx context.Context) int {
	log := r.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if r.Locker != nil {
		unlock, ok, err := r.Locker.TryLock(ctx, retentionLock)
		if err != nil || !ok {
			return 0
		}
		defer unlock()
	}
	days := r.Days
	if days < MinKeep || days > MaxKeep {
		days = DefaultKeep
	}
	if n, err := r.Store.ExpireStuckFaxes(ctx, 2*time.Hour); err != nil {
		log.Warn("fax: close stuck faxes", "error", err)
	} else if n > 0 {
		log.Info("fax: closed faxes that never finished", "count", n)
	}
	removed := 0
	for ctx.Err() == nil {
		ids, keys, err := r.Store.ExpiredFaxes(ctx, days, PurgeBatch)
		if err != nil {
			log.Warn("fax: list expired faxes", "error", err)
			return removed
		}
		if len(ids) == 0 {
			return removed
		}
		var gone []int64
		for i, id := range ids {
			if keys[i] != "" && r.Objects != nil {
				if err := r.Objects.Remove(ctx, keys[i]); err != nil {
					log.Warn("fax: remove document", "fax_id", id, "error", err)
					continue // the row stays until its object is gone
				}
			}
			gone = append(gone, id)
		}
		if len(gone) > 0 {
			if err := r.Store.DeleteFaxRows(ctx, gone); err != nil {
				log.Warn("fax: delete rows", "error", err)
				return removed
			}
			removed += len(gone)
		}
		if len(gone) == 0 || len(ids) < PurgeBatch {
			return removed
		}
	}
	return removed
}
