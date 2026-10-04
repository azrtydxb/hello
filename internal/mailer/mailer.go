package mailer

// Package mailer delivers voicemail notification emails: it polls the
// voicemail_messages queue for email_status='pending' rows, attaches the
// message audio and sends via SMTP, retrying three times with backoff before
// the message is marked failed. Nothing on the SIP path or the API waits
// for SMTP.

import (
	"context"
	"log/slog"
	"time"
)

// Job is one message waiting for delivery.
type Job struct {
	MessageID  int64
	To         string
	Caller     string
	DurationMs int64
	CreatedAt  time.Time
	Object     string // MinIO key of the WAV attachment
}

// Queue is the persistence the worker needs; *store.Store implements it.
type Queue interface {
	PendingEmails(ctx context.Context, limit int) ([]Job, error)
	MarkEmail(ctx context.Context, id int64, status string) error
}

// Objects fetches message audio from the voicemail bucket.
type Objects interface {
	Get(ctx context.Context, object string) ([]byte, error)
}

// Sender delivers one built message.
type Sender interface {
	Send(ctx context.Context, from, to string, msg []byte) error
}

// Statuses recorded on the message row.
const (
	StatusSent   = "sent"
	StatusFailed = "failed"
)

// Config wires the worker.
type Config struct {
	// From is the envelope sender and From header; empty disables the worker.
	From    string
	Queue   Queue
	Objects Objects
	Sender  Sender
	Log     *slog.Logger

	// MaxAttempts is how often one email is tried (spec S-8: three).
	MaxAttempts int
	// Backoff waits before retry attempt n+1; tests replace it.
	Backoff func(attempt int) time.Duration
	// PollEvery is how often the queue is checked.
	PollEvery time.Duration
	// FetchTimeout bounds one attachment download and one SMTP send.
	FetchTimeout time.Duration
}

// defaults for unset Config fields.
const (
	defaultMaxAttempts = 3
	defaultPollEvery   = 30 * time.Second
	defaultFetch       = 10 * time.Second
	defaultBackoff     = 2 * time.Second
)

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
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = defaultFetch
	}
	if cfg.Backoff == nil {
		cfg.Backoff = func(int) time.Duration { return defaultBackoff }
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Worker{cfg: cfg, log: cfg.Log}
}

// Enabled reports whether the worker can send (spec: the mailer is disabled
// when SMTP is not configured).
func (w *Worker) Enabled() bool { return w.cfg.Sender != nil && w.cfg.From != "" }

// Run delivers queued emails until ctx ends. A disabled worker returns at
// once.
func (w *Worker) Run(ctx context.Context) {
	if !w.Enabled() {
		w.log.Info("voicemail email delivery disabled (SMTP not configured)")
		return
	}
	t := time.NewTicker(w.cfg.PollEvery)
	defer t.Stop()
	for {
		w.process(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// process is one queue pass; a disabled worker does nothing.
func (w *Worker) process(ctx context.Context) {
	if !w.Enabled() {
		return
	}
	jobs, err := w.cfg.Queue.PendingEmails(ctx, 50)
	if err != nil {
		w.log.Warn("voicemail email queue", "error", err)
		return
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		w.deliver(ctx, j)
	}
}

// backoff waits d or until ctx ends, and reports whether the wait completed.
func backoff(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// deliver sends j, retrying up to MaxAttempts with backoff, then records the
// result on the message row.
func (w *Worker) deliver(ctx context.Context, j Job) {
	log := w.log.With("message_id", j.MessageID)
	var wav []byte
	if w.cfg.Objects != nil {
		fctx, cancel := context.WithTimeout(ctx, w.cfg.FetchTimeout)
		b, err := w.cfg.Objects.Get(fctx, j.Object)
		cancel()
		if err != nil {
			// The email still goes out, without the attachment.
			log.Warn("voicemail email: fetch attachment", "error", err)
		} else {
			wav = b
		}
	}
	msg, err := BuildMessage(w.cfg.From, j, wav)
	if err != nil {
		log.Error("voicemail email: build message", "error", err)
		_ = w.cfg.Queue.MarkEmail(ctx, j.MessageID, StatusFailed)
		return
	}
	for attempt := 1; ; attempt++ {
		sctx, cancel := context.WithTimeout(ctx, w.cfg.FetchTimeout)
		serr := w.cfg.Sender.Send(sctx, w.cfg.From, j.To, msg)
		cancel()
		if serr == nil {
			if merr := w.cfg.Queue.MarkEmail(ctx, j.MessageID, StatusSent); merr != nil {
				log.Warn("voicemail email: record sent", "error", merr)
			}
			return
		}
		log.Warn("voicemail email send failed", "attempt", attempt, "max", w.cfg.MaxAttempts, "error", serr)
		if attempt >= w.cfg.MaxAttempts || !backoff(ctx, w.cfg.Backoff(attempt)) {
			break
		}
	}
	if merr := w.cfg.Queue.MarkEmail(ctx, j.MessageID, StatusFailed); merr != nil {
		log.Warn("voicemail email: record failed", "error", merr)
	}
}
