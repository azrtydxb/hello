package chat

import (
	"context"
	"crypto/sha256"
	"strconv"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/store"
)

// fakeBackend is an in-memory Backend: sites by key, visitors by token and a
// log of events per visitor.
type fakeBackend struct {
	mu       sync.Mutex
	sites    map[string]store.ChatSite
	visitors map[string]*store.ChatVisitor // by token
	blocked  map[int64]bool
	events   map[int64][]store.ChatEvent
	nextEv   int64
	nextVis  int64
	online   bool
	sent     []store.ChatSendInput
	sessions []store.ChatSessionInput
	typed    int
	ended    int
	lines    map[int64][]store.ChatLine
	secret   string
	email    string
}

func newFake() *fakeBackend {
	return &fakeBackend{sites: map[string]store.ChatSite{}, visitors: map[string]*store.ChatVisitor{}, blocked: map[int64]bool{},
		events: map[int64][]store.ChatEvent{}, online: true, lines: map[int64][]store.ChatLine{}}
}

func (f *fakeBackend) addSite(key string, mod func(*store.ChatSite)) store.ChatSite {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := store.ChatSite{ID: int64(len(f.sites) + 1), ChannelID: 10, AddressID: 20, Name: "Support", Enabled: true, Key: key,
		AllowedOrigins: []string{"https://intranet.lan"}, Accent: "#2563eb", Position: "right", Collect: "off", OfflineMode: "collect",
		Challenge: store.ChallengePoW, PowBits: 14, MaxOpenUnassigned: 200}
	if mod != nil {
		mod(&s)
	}
	f.sites[key] = s
	return s
}

func (f *fakeBackend) addVisitor(token string) *store.ChatVisitor {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextVis++
	v := &store.ChatVisitor{ID: f.nextVis, SiteID: 1}
	f.visitors[token] = v
	return v
}

func (f *fakeBackend) push(visitor int64, typ string, line *store.ChatLine) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextEv++
	f.events[visitor] = append(f.events[visitor], store.ChatEvent{ID: f.nextEv, Type: typ, Line: line})
	return f.nextEv
}

func (f *fakeBackend) ChatSiteByKey(_ context.Context, key string) (store.ChatSite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sites[key]
	if !ok {
		return store.ChatSite{}, store.ErrNotFound
	}
	return s, nil
}
func (f *fakeBackend) ChatTurnstileSecret(context.Context, int64) (string, error) {
	return f.secret, nil
}
func (f *fakeBackend) ChatRetentionDays(context.Context, store.ChatSite) (int, error) { return 90, nil }
func (f *fakeBackend) ChatIPHash(ip string, _ time.Time) ([]byte, error) {
	h := sha256.Sum256([]byte("day:" + ip))
	return h[:], nil
}
func (f *fakeBackend) ChatAvailability(context.Context, store.ChatSite, time.Time) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.online {
		return false, "no_agent", nil
	}
	return true, "", nil
}
func (f *fakeBackend) ChatSession(_ context.Context, in store.ChatSessionInput) (store.ChatSessionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, in)
	if v, ok := f.visitors[in.Token]; ok && in.Token != "" {
		if f.blocked[v.ID] {
			return store.ChatSessionResult{Closed: true}, nil
		}
		return store.ChatSessionResult{VisitorID: v.ID}, nil
	}
	f.nextVis++
	tok := "tok-" + strconv.FormatInt(f.nextVis, 10) + "-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	f.visitors[tok] = &store.ChatVisitor{ID: f.nextVis, SiteID: in.Site.ID}
	return store.ChatSessionResult{Token: tok, VisitorID: f.nextVis, Created: true}, nil
}
func (f *fakeBackend) ChatAuth(_ context.Context, siteID int64, token string, _ []byte) (store.ChatVisitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.visitors[token]
	if !ok || v.SiteID != siteID {
		return store.ChatVisitor{}, store.ErrNotFound
	}
	out := *v
	out.Blocked = f.blocked[v.ID]
	return out, nil
}
func (f *fakeBackend) ChatSend(_ context.Context, in store.ChatSendInput) (store.ChatSendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, in)
	return store.ChatSendResult{MessageID: int64(len(f.sent)), ConversationID: 5}, nil
}
func (f *fakeBackend) ChatHistory(_ context.Context, v, after int64, _ int) ([]store.ChatLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ChatLine{}
	for _, l := range f.lines[v] {
		if l.ID > after {
			out = append(out, l)
		}
	}
	return out, nil
}
func (f *fakeBackend) ChatEvents(_ context.Context, v, after int64, limit int) ([]store.ChatEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ChatEvent{}
	for _, e := range f.events[v] {
		if e.ID > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f *fakeBackend) ChatLatestEventID(_ context.Context, v int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var id int64
	for _, e := range f.events[v] {
		id = max(id, e.ID)
	}
	return id, nil
}
func (f *fakeBackend) ChatTyping(context.Context, store.ChatSite, int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typed++
	return nil
}
func (f *fakeBackend) ChatEnd(context.Context, store.ChatSite, int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended++
	return nil
}
func (f *fakeBackend) ChatTranscript(_ context.Context, v int64) (string, []store.ChatLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.email, f.lines[v], nil
}
