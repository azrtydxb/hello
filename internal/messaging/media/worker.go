package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

// FetchJob is an inbound attachment waiting to be downloaded, as the store
// hands it to one worker. Ref holds the carrier's URL: it is never logged.
type FetchJob struct {
	ID        int64
	MessageID int64
	ChannelID int64
	Provider  string
	Ref       provider.MediaRef
	CreatedAt time.Time
	// Attempts counts the downloads tried before this one.
	Attempts int
}

// FetchResult is what one download came to.
type FetchResult struct {
	Status string // ready, rejected, expired, or pending to try again
	Reason string
	// Retry is when to try again (Status pending).
	Retry time.Duration
	Key   string
	Inspected
	Declared string
}

// Garbage is an attachment whose object goes: an orphan (its message was
// purged or erased), an upload never sent, or one past retention. Drop says
// whether the row goes too; a row past retention stays, expired, so the
// conversation still shows a file was there.
type Garbage struct {
	ID   int64
	Key  string
	Drop bool
}

// Store is what the workers need of the database; *store.Store implements it.
type Store interface {
	ClaimMediaFetch(ctx context.Context, limit int) ([]FetchJob, error)
	FinishMediaFetch(ctx context.Context, job FetchJob, res FetchResult) error
	ChannelProviderConfig(ctx context.Context, channelID int64) (provider.Config, error)
	MediaGarbage(ctx context.Context, limit int) ([]Garbage, error)
	DropMedia(ctx context.Context, g Garbage) error
}

// Worker downloads inbound carrier media and removes what is no longer kept.
// Any number of replicas may run FetchOnce (the store claims each row once);
// CollectOnce runs on the replica holding the inbox maintenance lease.
type Worker struct {
	Store   Store
	Objects Objects
	// Max is HELLO_MSG_MEDIA_MAX.
	Max int64
	// New builds a carrier adapter; nil uses the registry.
	New func(kind string, cfg provider.Config) (provider.SMSProvider, error)
	// Count counts a result (hello_msg_media_total); nil counts nothing.
	Count func(result string)
	Log   *slog.Logger
	Now   func() time.Time
	// Timeout bounds one download (default 30 s, spec Constraints).
	Timeout time.Duration
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) count(result string) {
	if w.Count != nil {
		w.Count(result)
	}
}

// retryDelay is the wait before the nth try: 1, 5, 15 minutes, then hourly,
// inside FetchWindow.
func retryDelay(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return time.Minute
	case attempts == 2:
		return 5 * time.Minute
	case attempts == 3:
		return 15 * time.Minute
	}
	return time.Hour
}

// FetchOnce downloads up to limit claimed attachments and returns how many it
// handled.
func (w *Worker) FetchOnce(ctx context.Context, limit int) (int, error) {
	jobs, err := w.Store.ClaimMediaFetch(ctx, limit)
	if err != nil {
		return 0, err
	}
	for _, j := range jobs {
		res := w.fetch(ctx, j)
		w.count(res.Status)
		if err := w.Store.FinishMediaFetch(ctx, j, res); err != nil && w.Log != nil {
			// The claim lapses and another pass tries again.
			w.Log.Warn("messaging media result not saved", "media", j.ID, "error", err)
		}
	}
	return len(jobs), nil
}

func (w *Worker) fetch(ctx context.Context, j FetchJob) FetchResult {
	if w.now().Sub(j.CreatedAt) > FetchWindow {
		return FetchResult{Status: StatusExpired, Reason: ReasonWindow}
	}
	retry := FetchResult{Status: StatusPending, Retry: retryDelay(j.Attempts + 1)}
	if w.New == nil && !provider.Known(j.Provider) {
		// A SIP trunk or a chat has no carrier to fetch from.
		return FetchResult{Status: StatusRejected, Reason: ReasonNoFetcher}
	}
	cfg, err := w.Store.ChannelProviderConfig(ctx, j.ChannelID)
	if err != nil {
		return retry
	}
	var p provider.SMSProvider
	if w.New != nil {
		p, err = w.New(j.Provider, cfg)
	} else {
		p, err = provider.New(j.Provider, cfg)
	}
	if err != nil {
		return retry
	}
	f, ok := p.(provider.MediaFetcher)
	if !ok {
		return FetchResult{Status: StatusRejected, Reason: ReasonNoFetcher}
	}
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	m, err := f.FetchMedia(cctx, j.Ref)
	switch {
	case errors.Is(err, provider.ErrMediaHost):
		return FetchResult{Status: StatusRejected, Reason: ReasonMediaHost}
	case errors.Is(err, provider.ErrNotFound):
		return FetchResult{Status: StatusExpired, Reason: ReasonGone}
	case err != nil:
		return retry
	}
	defer func() { _ = m.Body.Close() }()
	rules := InboxRules(w.Max)
	if j.Ref.Size > rules.Max {
		return FetchResult{Status: StatusRejected, Reason: ReasonTooLarge}
	}
	data, err := io.ReadAll(io.LimitReader(m.Body, rules.Max+1))
	if err != nil {
		return retry
	}
	declared := m.ContentType
	if declared == "" {
		declared = j.Ref.ContentType
	}
	in, refusal := Inspect(data, declared, rules)
	if refusal != nil {
		return FetchResult{Status: StatusRejected, Reason: refusal.Reason, Declared: baseType(declared)}
	}
	key := MessageKey(j.ChannelID, j.CreatedAt, j.MessageID, in.SHA256)
	if err := w.Objects.Put(ctx, key, bytes.NewReader(data), in.Size, in.ContentType); err != nil {
		if w.Log != nil {
			w.Log.Warn("messaging media not stored", "media", j.ID, "error", err)
		}
		return retry
	}
	return FetchResult{Status: StatusReady, Key: key, Inspected: in, Declared: baseType(declared)}
}

// CollectOnce removes up to limit objects that are no longer kept, checking
// each is gone before its row is dropped or marked expired, and returns how
// many it settled. An object that could not be removed is tried again on the
// next pass.
func (w *Worker) CollectOnce(ctx context.Context, limit int) (int, error) {
	gs, err := w.Store.MediaGarbage(ctx, limit)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, g := range gs {
		if g.Key != "" {
			if err := RemoveVerified(ctx, w.Objects, g.Key); err != nil {
				if w.Log != nil {
					w.Log.Warn("messaging media not removed", "media", g.ID, "error", err)
				}
				continue
			}
		}
		if err := w.Store.DropMedia(ctx, g); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ErrStillThere is an object that was removed but is still found.
var ErrStillThere = errors.New("media: object still present after removal")

// RemoveVerified removes an object and checks it is gone (spec M-20: an
// erase verifies each deletion).
func RemoveVerified(ctx context.Context, objs Objects, key string) error {
	if err := objs.Remove(ctx, key); err != nil {
		return err
	}
	ok, err := objs.Exists(ctx, key)
	if err != nil {
		return err
	}
	if ok {
		return ErrStillThere
	}
	return nil
}
