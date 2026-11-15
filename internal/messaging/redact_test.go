package messaging

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

type redactFake struct {
	mu       sync.Mutex
	jobs     []RedactJob
	results  map[int64]RedactResult
	claimed  []string
	maintain int
}

func (s *redactFake) ClaimRedactions(_ context.Context, batch string, limit int) ([]RedactJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed = append(s.claimed, batch)
	out := s.jobs
	if len(out) > limit {
		out = out[:limit]
	}
	s.jobs = s.jobs[len(out):]
	return out, nil
}

func (s *redactFake) ChannelProviderConfig(_ context.Context, id int64) (provider.Config, error) {
	if id == 99 {
		return provider.Config{}, errors.New("sealed with another key")
	}
	return provider.Config{AccountSID: "AC1"}, nil
}

func (s *redactFake) FinishRedaction(_ context.Context, j RedactJob, r RedactResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[j.ID] = r
	return nil
}

func (s *redactFake) MaintainRedactions(context.Context) error {
	s.mu.Lock()
	s.maintain++
	s.mu.Unlock()
	return nil
}

// redactCarrier answers Redact by message id.
type redactCarrier struct {
	provider.SMSProvider
	errs map[string]error
}

func (f redactCarrier) Redact(_ context.Context, id string) (provider.Redaction, error) {
	if err := f.errs[id]; err != nil {
		return provider.Redaction{}, err
	}
	return provider.Redaction{Deleted: true}, nil
}

type redactPlain struct{ provider.SMSProvider }

// TestRedactor fails if a deletion's outcome is recorded wrongly: done for a
// deleted or vanished message, unsupported for a carrier without the
// operation, a retry with back-off for an outage, the credentials or a rate
// limit, failed for a refusal or after the last try.
func TestRedactor(t *testing.T) {
	st := &redactFake{results: map[int64]RedactResult{}}
	st.jobs = []RedactJob{
		{ID: 1, ChannelID: 1, Provider: "twilio", ProviderID: "ok", Attempts: 1},
		{ID: 2, ChannelID: 1, Provider: "twilio", ProviderID: "gone", Attempts: 1},
		{ID: 3, ChannelID: 1, Provider: "twilio", ProviderID: "down", Attempts: 2},
		{ID: 4, ChannelID: 1, Provider: "twilio", ProviderID: "auth", Attempts: 1},
		{ID: 5, ChannelID: 1, Provider: "twilio", ProviderID: "refused", Attempts: 1},
		{ID: 6, ChannelID: 1, Provider: "twilio", ProviderID: "down", Attempts: MaxRedactAttempts},
		{ID: 7, ChannelID: 1, Provider: "plain", ProviderID: "x", Attempts: 1},
		{ID: 8, ChannelID: 1, Provider: "telnyx", ProviderID: "x", Attempts: 1},
		{ID: 9, ChannelID: 99, Provider: "twilio", ProviderID: "ok", Attempts: 1},
		{ID: 10, ChannelID: 1, Provider: "twilio", ProviderID: "slow", Attempts: 1},
	}
	carrier := redactCarrier{errs: map[string]error{
		"gone": provider.ErrNotFound, "down": provider.ErrAmbiguous, "auth": provider.ErrAuth,
		"refused": &provider.Rejected{Code: "20009"}, "slow": &provider.Rejected{Code: provider.CodeRateLimited},
	}}
	reg := prometheus.NewRegistry()
	r := &Redactor{Store: st, Metrics: NewRedactMetrics(reg), New: func(kind string, _ provider.Config) (provider.SMSProvider, error) {
		switch kind {
		case "plain":
			return redactPlain{}, nil
		case "telnyx":
			return redactCarrier{errs: map[string]error{"x": provider.ErrRedactUnsupported}}, nil
		}
		return carrier, nil
	}}
	r.RunBatch(context.Background(), "erase-1", 5*time.Second)
	want := map[int64]string{1: RedactDone, 2: RedactDone, 3: RedactPending, 4: RedactPending, 5: RedactFailed, 6: RedactFailed,
		7: RedactUnsupported, 8: RedactUnsupported, 9: RedactPending, 10: RedactPending}
	for id, state := range want {
		if got := st.results[id]; got.State != state {
			t.Errorf("job %d: %+v, want %s", id, got, state)
		}
	}
	if st.results[3].RetryIn != 2*time.Minute || st.results[4].RetryIn != time.Minute {
		t.Errorf("back-off %v %v", st.results[3].RetryIn, st.results[4].RetryIn)
	}
	if st.claimed[0] != "erase-1" {
		t.Errorf("claimed batch %q", st.claimed[0])
	}
	if n := testutil.ToFloat64(r.Metrics.Total.WithLabelValues("twilio", RedactDone)); n != 2 {
		t.Errorf("done metric = %v", n)
	}
	if RedactBackoff(1) != time.Minute || RedactBackoff(20) != 6*time.Hour {
		t.Errorf("RedactBackoff %v %v", RedactBackoff(1), RedactBackoff(20))
	}
	r.RunBatch(context.Background(), "", time.Second) // no batch: nothing
	if len(st.claimed) != 2 {
		t.Errorf("an empty batch claimed: %v", st.claimed)
	}
}
