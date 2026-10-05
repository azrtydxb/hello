// The Phase 4 collaborators hello-sip wires at startup: the voicemail store
// adapter, the feature-code settings sink, and the voicemail audio store.
// They are the only parts of hello-sip that touch PostgreSQL as a writer and
// MinIO directly.
package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/sip"
	"github.com/azrtydxb/hello/internal/store"
)

// voicemails adapts the control-plane store to sip.VoicemailStore. Every
// method runs on voicemail media or MWI goroutines, never on the SIP
// transaction path, and is bounded by the caller's context.
type voicemails struct {
	st *store.Store
}

var _ sip.VoicemailStore = voicemails{}

// Box returns the box of the dialled extension number.
func (v voicemails) Box(ctx context.Context, extension string) (sip.VoicemailBox, bool, error) {
	b, ok, err := v.st.VoicemailBoxByNumber(ctx, extension)
	if err != nil {
		return sip.VoicemailBox{}, false, err
	}
	if !ok {
		return sip.VoicemailBox{}, false, nil
	}
	return sip.VoicemailBox{
		ID:                b.ID,
		Extension:         extension,
		Email:             b.Email,
		GreetingObject:    b.GreetingObject,
		UnreachableObject: b.UnreachableObject,
	}, true, nil
}

// PasswordOK compares a retrieval password against the box's bcrypt hash.
func (v voicemails) PasswordOK(ctx context.Context, boxID int64, password string) (bool, error) {
	return v.st.VerifyVoicemailPassword(ctx, boxID, password)
}

// InsertMessage stores one message row after its audio reached MinIO.
func (v voicemails) InsertMessage(ctx context.Context, boxID int64, object, caller string, durationMs int64) (int64, error) {
	return v.st.InsertVoicemailMessage(ctx, boxID, object, caller, durationMs)
}

// MessageCounts is the box's MWI summary (unheard, total).
func (v voicemails) MessageCounts(ctx context.Context, boxID int64) (int, int, error) {
	un, total, err := v.st.VoicemailCounts(ctx, boxID)
	return int(un), int(total), err
}

// ListMessages returns the box's messages, newest first.
func (v voicemails) ListMessages(ctx context.Context, boxID int64) ([]sip.VoicemailMessage, error) {
	msgs, err := v.st.ListVoicemailMessages(ctx, boxID, false)
	if err != nil {
		return nil, err
	}
	out := make([]sip.VoicemailMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, sip.VoicemailMessage{
			ID:          m.ID,
			BoxID:       m.BoxID,
			MinioObject: m.Object,
			Caller:      m.Caller,
			DurationMs:  m.DurationMs,
			Heard:       m.Heard,
			CreatedAt:   m.CreatedAt,
		})
	}
	return out, nil
}

// MarkHeard marks a message heard after the phone played it.
func (v voicemails) MarkHeard(ctx context.Context, id int64) error {
	return v.st.SetVoicemailMessageHeard(ctx, id, true)
}

// settings applies feature-code updates through the control-plane store:
// Apply queues, Run applies off the SIP transaction path.
type settings struct {
	st  *store.Store
	ch  chan sip.ExtUpdate
	log *slog.Logger
}

var _ sip.SettingsSink = (*settings)(nil)

// settingsQueueDepth bounds the updates waiting while one write is in
// flight; a full queue reports the change as not accepted, and the phone
// hears 503 instead of the change being lost silently.
const settingsQueueDepth = 64

func newSettings(st *store.Store, log *slog.Logger) *settings {
	return &settings{st: st, ch: make(chan sip.ExtUpdate, settingsQueueDepth), log: log}
}

// Apply queues one extension-feature change and reports whether it was
// accepted. It runs on the SIP transaction path, so it only enqueues: the
// worker goroutine (Run) writes PostgreSQL.
func (q *settings) Apply(u sip.ExtUpdate) bool {
	if u.Extension == "" {
		return false
	}
	select {
	case q.ch <- u:
		return true
	default:
		q.log.Warn("feature-code update dropped: queue full", "extension", u.Extension)
		return false
	}
}

// Run applies queued updates until ctx ends, one bounded transaction each.
func (q *settings) Run(ctx context.Context) {
	for {
		select {
		case u := <-q.ch:
			q.apply(u)
		case <-ctx.Done():
			return
		}
	}
}

// apply writes one update with a generous timeout: the advisory lock every
// configuration change takes can wait on a busy control plane, and a queued
// feature-code change the phone was already told about cannot be replayed.
func (q *settings) apply(u sip.ExtUpdate) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := q.st.ApplyFeatureUpdate(ctx, "hello-sip", store.FeatureUpdate{
		Number:          u.Extension,
		DND:             u.DND,
		ForwardAlways:   u.ForwardAlways,
		ForwardBusy:     u.ForwardBusy,
		ForwardNoAnswer: u.ForwardNoAnswer,
	})
	if err != nil {
		q.log.Warn("feature-code update failed", "extension", u.Extension, "error", err)
	}
}

// lazyObjects builds the MinIO voicemail store on first use and keeps it. A
// MinIO that is down at startup must not keep the SIP node from serving
// calls: the node starts without voicemail audio and the first operation
// after MinIO returns builds the client (and the bucket) then.
type lazyObjects struct {
	endpoint, access, secret string
	secure                   bool
	log                      *slog.Logger

	mu    sync.Mutex
	inner media.Objects
}

var _ sip.ObjectStore = (*lazyObjects)(nil)

// store builds the client once. Concurrent first users wait; the loser of
// the race keeps whichever client won.
func (l *lazyObjects) store() (media.Objects, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inner != nil {
		return l.inner, nil
	}
	o, err := media.NewMinioObjects(l.endpoint, l.access, l.secret, l.secure, l.log)
	if err != nil {
		return nil, err
	}
	l.inner = o
	return o, nil
}

func (l *lazyObjects) Put(ctx context.Context, key string, data []byte) error {
	o, err := l.store()
	if err != nil {
		return err
	}
	return o.Put(ctx, key, data)
}

func (l *lazyObjects) Get(ctx context.Context, key string) ([]byte, error) {
	o, err := l.store()
	if err != nil {
		return nil, err
	}
	return o.Get(ctx, key)
}

func (l *lazyObjects) PutRecording(ctx context.Context, key string, data []byte) error {
	o, err := l.store()
	if err != nil {
		return err
	}
	return o.PutRecording(ctx, key, data)
}

func (l *lazyObjects) GetAnnouncement(ctx context.Context, key string) ([]byte, error) {
	o, err := l.store()
	if err != nil {
		return nil, err
	}
	return o.GetAnnouncement(ctx, key)
}
