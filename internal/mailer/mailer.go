// Package mailer delivers voicemail notification emails. It runs beside call
// handling and never inside it: a worker fans new voicemail messages out into
// one notification per recipient, claims due notifications from the shared
// database queue (one node per notification, so a cluster sends each email
// once), sends them over SMTP and retries failures with exponential backoff
// until they succeed or are given up on.
package mailer

import (
	"context"
	"log/slog"
	"time"
)

// Job is one notification claimed for sending.
type Job struct {
	ID         int64
	MessageID  int64
	To         string
	Caller     string
	DurationMs int64
	CreatedAt  time.Time
	Object     string // storage key of the recording
	// Attach adds the recording to the email.
	Attach   bool
	Attempts int
	// Queued is when the notification was created.
	Queued time.Time
}

// Result is how a send ended, for the queue to record.
type Result struct {
	// Err is the failure text, "" on success. It must not carry secrets.
	Err     string
	Final   bool
	RetryAt time.Time
}

// Queue is the persistence the worker needs; the adapter over *store.Store
// implements it.
type Queue interface {
	// FanOut creates the notifications of new messages.
	FanOut(ctx context.Context, limit int) (int, error)
	// Claim takes due notifications for this node, including ones whose
	// sender vanished (claimed longer than stale ago).
	Claim(ctx context.Context, limit int, stale time.Duration) ([]Job, error)
	// Finish records a result and applies the recipients' after-send action;
	// it returns the recording to delete from storage, if the message went.
	Finish(ctx context.Context, id int64, r Result) (removeObject string, err error)
}

// Objects reads and removes message audio.
type Objects interface {
	Get(ctx context.Context, object string) ([]byte, error)
	Remove(ctx context.Context, object string) error
}

// SettingsFunc returns the SMTP settings now in force; ok=false when email
// is not configured. It is read on every pass, so a change in the console
// applies without a restart.
type SettingsFunc func(ctx context.Context) (s Settings, ok bool, err error)

// Config wires the worker.
type Config struct {
	Queue    Queue
	Objects  Objects
	Settings SettingsFunc
	// Deliver sends one built message; nil uses SMTP.
	Deliver func(ctx context.Context, s Settings, to string, msg []byte) error
	Log     *slog.Logger

	// MaxAttempts is how often one notification is tried.
	MaxAttempts int
	// Backoff is the wait before the retry that follows attempt n.
	Backoff func(attempt int) time.Duration
	// GiveUp stops retrying a notification this long after it was queued.
	GiveUp time.Duration
	// PollEvery is how often the queue is checked.
	PollEvery time.Duration
	// Timeout bounds one attachment download and one SMTP send.
	Timeout time.Duration
	// Stale is how long a claim may sit before another node takes the
	// notification over.
	Stale time.Duration
	Now   func() time.Time
}

const (
	defaultMaxAttempts = 6
	defaultPollEvery   = 10 * time.Second
	defaultTimeout     = 30 * time.Second
	defaultGiveUp      = 24 * time.Hour
	batch              = 25
)

// defaultBackoff doubles from 30 s to one hour.
func defaultBackoff(attempt int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// Worker is the voicemail email queue worker.
type Worker struct {
	cfg Config
	log *slog.Logger
}

// New returns the worker with defaults filled in.
func New(cfg Config) *Worker {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = defaultPollEvery
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.GiveUp <= 0 {
		cfg.GiveUp = defaultGiveUp
	}
	if cfg.Stale <= 0 {
		cfg.Stale = 3 * cfg.Timeout
	}
	if cfg.Backoff == nil {
		cfg.Backoff = defaultBackoff
	}
	if cfg.Deliver == nil {
		cfg.Deliver = Deliver
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Worker{cfg: cfg, log: cfg.Log}
}

// Run works the queue until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.cfg.PollEvery)
	defer t.Stop()
	for {
		w.Process(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Process is one pass: fan out, claim, send. Without SMTP settings it does
// nothing, so notifications wait for them (and are given up on after
// GiveUp).
func (w *Worker) Process(ctx context.Context) {
	st, ok, err := w.cfg.Settings(ctx)
	if err != nil {
		w.log.Warn("voicemail email: read SMTP settings", "error", err)
		return
	}
	if !ok {
		return
	}
	if _, err := w.cfg.Queue.FanOut(ctx, batch); err != nil {
		w.log.Warn("voicemail email: fan out", "error", err)
		return
	}
	jobs, err := w.cfg.Queue.Claim(ctx, batch, w.cfg.Stale)
	if err != nil {
		w.log.Warn("voicemail email: claim", "error", err)
		return
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		w.deliver(ctx, st, j)
	}
}

// deliver makes one attempt for j and records the result.
func (w *Worker) deliver(ctx context.Context, st Settings, j Job) {
	log := w.log.With("notification_id", j.ID, "message_id", j.MessageID, "attempt", j.Attempts)
	lastTry := j.Attempts >= w.cfg.MaxAttempts || w.cfg.Now().Sub(j.Queued) >= w.cfg.GiveUp
	var (
		audio    []byte
		fetchErr error
	)
	if j.Attach && w.cfg.Objects != nil {
		fctx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
		audio, fetchErr = w.cfg.Objects.Get(fctx, j.Object)
		cancel()
		if fetchErr != nil && !lastTry {
			// Retry rather than send without the recording asked for.
			w.finish(ctx, log, j, "fetch recording: "+fetchErr.Error(), false)
			return
		}
		if fetchErr != nil {
			log.Warn("voicemail email: sending without the recording", "error", fetchErr)
			audio = nil
		}
	}
	msg, err := BuildMessage(st.From, j, audio)
	if err != nil {
		log.Error("voicemail email: build message", "error", err)
		w.finish(ctx, log, j, "build message: "+err.Error(), true)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	err = w.cfg.Deliver(sctx, st, j.To, msg)
	cancel()
	if err == nil {
		w.finish(ctx, log, j, "", false)
		return
	}
	log.Warn("voicemail email send failed", "max", w.cfg.MaxAttempts, "error", err)
	w.finish(ctx, log, j, err.Error(), lastTry)
}

func (w *Worker) finish(ctx context.Context, log *slog.Logger, j Job, errText string, final bool) {
	r := Result{Err: errText, Final: final}
	if errText != "" && !final {
		r.RetryAt = w.cfg.Now().Add(w.cfg.Backoff(j.Attempts))
	}
	// Recording the result must survive a shutdown that cancels ctx.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	remove, err := w.cfg.Queue.Finish(fctx, j.ID, r)
	if err != nil {
		log.Warn("voicemail email: record result", "error", err)
		return
	}
	if remove != "" && w.cfg.Objects != nil {
		if err := w.cfg.Objects.Remove(fctx, remove); err != nil {
			log.Warn("voicemail email: remove recording after send", "error", err)
		}
	}
}
