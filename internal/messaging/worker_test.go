package messaging

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

// fakeStore is the worker's store in memory: it hands each queued job out
// once, like the SQL claim.
type fakeStore struct {
	mu       sync.Mutex
	queued   []OutboundJob
	polls    []PollJob
	finished map[int64]OutboundResult
	polled   map[int64]PollResult
	cfgErr   error
}

func (f *fakeStore) ClaimOutbound(_ context.Context, limit int) ([]OutboundJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := min(limit, len(f.queued))
	out := f.queued[:n]
	f.queued = f.queued[n:]
	return out, nil
}
func (f *fakeStore) ChannelProviderConfig(context.Context, int64) (provider.Config, error) {
	return provider.Config{AccountSID: "AC", AuthToken: "t"}, f.cfgErr
}
func (f *fakeStore) FinishOutbound(_ context.Context, j OutboundJob, r OutboundResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finished == nil {
		f.finished = map[int64]OutboundResult{}
	}
	f.finished[j.MessageID] = r
	return nil
}
func (f *fakeStore) ClaimPolls(_ context.Context, limit int) ([]PollJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := min(limit, len(f.polls))
	out := f.polls[:n]
	f.polls = f.polls[n:]
	return out, nil
}
func (f *fakeStore) FinishPoll(_ context.Context, j PollJob, r PollResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.polled == nil {
		f.polled = map[int64]PollResult{}
	}
	f.polled[j.MessageID] = r
	return nil
}

// fakeCarrier is a provider whose answers a test scripts.
type fakeCarrier struct {
	mu        sync.Mutex
	sendErr   error
	sent      provider.Sent
	state     provider.State
	statusErr error
	sends     int
	statuses  int
	caps      provider.Caps
}

func (c *fakeCarrier) Kind() string { return "fake" }
func (c *fakeCarrier) Caps() provider.Caps {
	if c.caps.StatusWindow == 0 {
		return provider.Caps{MaxBody: 1600, StatusWindow: 48 * time.Hour}
	}
	return c.caps
}
func (c *fakeCarrier) Validate(context.Context) error { return nil }
func (c *fakeCarrier) Send(context.Context, provider.Outbound) (provider.Sent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sends++
	return c.sent, c.sendErr
}
func (c *fakeCarrier) Status(context.Context, string) (provider.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses++
	return c.state, c.statusErr
}
func (c *fakeCarrier) Numbers(context.Context) ([]provider.Number, error) { return nil, nil }

func worker(st *fakeStore, c *fakeCarrier) *Worker {
	return &Worker{Store: st, Timeout: time.Second, New: func(string, provider.Config) (provider.SMSProvider, error) { return c, nil }}
}

// TestSendAtMostOnce fails if a worker sends a text again after the carrier
// may have accepted it, leaves an accepted text unsent, lets a refused one
// count as sent, or does not stop on bad credentials or an opt-out.
func TestSendAtMostOnce(t *testing.T) {
	job := OutboundJob{MessageID: 1, ChannelID: 1, Provider: "fake", From: "+3270123456", To: "+32475000001", Body: "hi", AttemptID: "a"}
	run := func(c *fakeCarrier) (OutboundResult, *fakeStore) {
		st := &fakeStore{queued: []OutboundJob{job}}
		w := worker(st, c)
		if n, err := w.SendOnce(context.Background(), 10); err != nil || n != 1 {
			t.Fatalf("SendOnce = %d, %v", n, err)
		}
		// A second pass (or a second replica) finds nothing to send.
		if n, _ := w.SendOnce(context.Background(), 10); n != 0 {
			t.Fatalf("a claimed text was handed out again")
		}
		if c.sends != 1 {
			t.Fatalf("the carrier was called %d times", c.sends)
		}
		return st.finished[1], st
	}

	res, _ := run(&fakeCarrier{sent: provider.Sent{ProviderID: "SM1", Status: provider.StatusQueued}})
	if res.Outcome != OutcomeSent || res.ProviderID != "SM1" {
		t.Errorf("accepted: %+v", res)
	}
	// A lost answer: unknown, no error code, counted against the channel; not retried.
	res, _ = run(&fakeCarrier{sendErr: provider.ErrAmbiguous})
	if res.Outcome != OutcomeUnknown || res.ProviderID != "" || !res.ChannelFailure {
		t.Errorf("ambiguous: %+v", res)
	}
	res, _ = run(&fakeCarrier{sendErr: &provider.Rejected{Code: "21211"}})
	if res.Outcome != OutcomeFailed || res.ErrorCode != "21211" || res.OptOut || res.ChannelFailure {
		t.Errorf("rejected: %+v", res)
	}
	res, _ = run(&fakeCarrier{sendErr: &provider.Rejected{Code: provider.CodeOptedOut}})
	if res.Outcome != OutcomeFailed || !res.OptOut {
		t.Errorf("opt-out: %+v", res)
	}
	res, _ = run(&fakeCarrier{sendErr: provider.ErrAuth})
	if res.Outcome != OutcomeFailed || !res.BadCredentials {
		t.Errorf("auth: %+v", res)
	}
	res, _ = run(&fakeCarrier{sendErr: &provider.Rejected{Code: provider.CodeRateLimited}})
	if res.Outcome != OutcomeFailed || !res.ChannelFailure {
		t.Errorf("rate limited: %+v", res)
	}

	// Unusable credentials: nothing is sent.
	st := &fakeStore{queued: []OutboundJob{job}, cfgErr: errors.New("cannot open")}
	c := &fakeCarrier{}
	if _, err := worker(st, c).SendOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if c.sends != 0 || st.finished[1].Outcome != OutcomeFailed {
		t.Errorf("sends = %d, result = %+v", c.sends, st.finished[1])
	}
}

// TestStatusPolling fails if a delivered or failed text is not settled, a
// settled one keeps being polled, a text older than the carrier's status
// window is polled at all, or a vanished or unauthorised read does not stop.
func TestStatusPolling(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	job := func(id int64, age time.Duration) PollJob {
		return PollJob{MessageID: id, ChannelID: 1, Provider: "fake", ProviderID: "SM", SentAt: now.Add(-age)}
	}
	poll := func(c *fakeCarrier, age time.Duration) (PollResult, *fakeCarrier) {
		st := &fakeStore{polls: []PollJob{job(1, age)}}
		w := worker(st, c)
		w.Now = func() time.Time { return now }
		if n, err := w.PollOnce(context.Background(), 10); err != nil || n != 1 {
			t.Fatalf("PollOnce = %d, %v", n, err)
		}
		return st.polled[1], c
	}

	r, _ := poll(&fakeCarrier{state: provider.State{Status: provider.StatusDelivered}}, time.Minute)
	if !r.Read || r.State.Status != provider.StatusDelivered || !r.State.Final() {
		t.Errorf("delivered: %+v", r)
	}
	r, _ = poll(&fakeCarrier{state: provider.State{Status: provider.StatusFailed, ErrorCode: "30003"}}, time.Minute)
	if !r.Read || r.State.ErrorCode != "30003" || !r.State.Final() {
		t.Errorf("failed: %+v", r)
	}
	r, _ = poll(&fakeCarrier{state: provider.State{Status: provider.StatusSent}}, time.Minute)
	if !r.Read || r.State.Final() || r.Stop {
		t.Errorf("sent: %+v", r)
	}
	// Older than 48 hours: stop without asking the carrier.
	r, c := poll(&fakeCarrier{state: provider.State{Status: provider.StatusSent}}, 49*time.Hour)
	if !r.Stop || r.Read || c.statuses != 0 {
		t.Errorf("too old: %+v, carrier asked %d times", r, c.statuses)
	}
	r, _ = poll(&fakeCarrier{statusErr: provider.ErrNotFound}, time.Minute)
	if !r.Stop {
		t.Errorf("not found: %+v", r)
	}
	r, _ = poll(&fakeCarrier{statusErr: provider.ErrAuth}, time.Minute)
	if !r.Stop || !r.BadCredentials {
		t.Errorf("auth: %+v", r)
	}
	// A transient failure is neither a read nor a stop: it is tried again.
	r, _ = poll(&fakeCarrier{statusErr: provider.ErrAmbiguous}, time.Minute)
	if r.Read || r.Stop {
		t.Errorf("transient: %+v", r)
	}
}
