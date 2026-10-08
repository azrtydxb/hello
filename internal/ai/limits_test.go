package ai_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"
)

// blockingModel holds every call until release closes, counting the calls
// in flight.
type blockingModel struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	started  chan struct{}
	release  chan struct{}
}

func newBlocking() *blockingModel {
	return &blockingModel{started: make(chan struct{}, 16), release: make(chan struct{})}
}

func (m *blockingModel) Generate(ctx context.Context, _ provider.Call) (*provider.Response, error) {
	m.mu.Lock()
	m.inFlight++
	m.peak = max(m.peak, m.inFlight)
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.inFlight--; m.mu.Unlock() }()
	m.started <- struct{}{}
	select {
	case <-m.release:
		return aifake.JSON(answer{Answer: "ok"}), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *blockingModel) Stream(context.Context, provider.Call) (provider.StreamResponse, error) {
	return nil, errors.New("no streams")
}
func (m *blockingModel) ModelID() string      { return "blocking" }
func (m *blockingModel) ProviderName() string { return "test" }
func (m *blockingModel) Capabilities() provider.Capabilities {
	return provider.Capabilities{NativeJSON: true}
}

func service(t *testing.T, cfg config.AIAgent, m provider.LanguageModel, st *aifake.Store, o ai.Options) *ai.Service {
	t.Helper()
	o.Model = m
	if o.Replica == "" {
		o.Replica = "a"
	}
	s, err := ai.NewWith(cfg, st, nil, prometheus.NewRegistry(), slog.New(slog.DiscardHandler), o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ask(ctx context.Context, s *ai.Service, background bool) error {
	_, _, err := ai.Generate(ctx, s, ai.Call[answer]{Feature: "t", Prompt: "q", Background: background})
	return err
}

// TestTasksAndLimits (spec S-15, S-16) fails if a third concurrent call
// starts, an interactive call waits more than 5 s before ai_busy, a
// background call waits at all, the rate limit or the timeout does not
// apply, background work runs past 80 % of the budget or interactive work
// past 100 %, another user reads a task, a session runs two tasks, or (with
// Valkey) a stopped replica's task is not failed instance_stopped.
func TestTasksAndLimits(t *testing.T) {
	ctx := context.Background()

	t.Run("concurrency", func(t *testing.T) {
		m := newBlocking()
		s := service(t, aifake.Config(), m, aifake.NewStore(), ai.Options{})
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); _ = ask(ctx, s, false) }()
		}
		<-m.started
		<-m.started
		start := time.Now()
		if err := ask(ctx, s, true); ai.CodeOf(err) != ai.CodeBusy {
			t.Errorf("background third call: %v", err)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("background call waited %v", d)
		}
		start = time.Now()
		err := ask(ctx, s, false)
		if d := time.Since(start); ai.CodeOf(err) != ai.CodeBusy || d < 4900*time.Millisecond || d > 6*time.Second {
			t.Errorf("interactive third call: %v after %v, want ai_busy after 5 s", err, d)
		}
		close(m.release)
		wg.Wait()
		if m.peak != 2 {
			t.Errorf("peak in flight = %d", m.peak)
		}
	})

	t.Run("rate", func(t *testing.T) {
		cfg := aifake.Config()
		cfg.RequestsPerMinute = 1
		s, _, _ := aifake.Service(t, cfg, aifake.JSON(answer{Answer: "a"}), aifake.JSON(answer{Answer: "b"}))
		if err := ask(ctx, s, true); err != nil {
			t.Fatal(err)
		}
		if err := ask(ctx, s, true); ai.CodeOf(err) != ai.CodeBusy {
			t.Errorf("second start within the minute: %v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		cfg := aifake.Config()
		cfg.Timeout = 50 * time.Millisecond
		s := service(t, cfg, newBlocking(), aifake.NewStore(), ai.Options{})
		if err := ask(ctx, s, false); ai.CodeOf(err) != ai.CodeTimeout {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("budget shares", func(t *testing.T) {
		cfg := aifake.Config()
		cfg.DailyTokenBudget = 1000
		now := time.Now()
		st := aifake.NewStore()
		m := &aitest.MockModel{Responses: []*provider.Response{aifake.JSON(answer{Answer: "a"}), aifake.JSON(answer{Answer: "b"})},
			Caps: provider.Capabilities{NativeJSON: true}}
		s := service(t, cfg, m, st, ai.Options{})
		st.SetUsage(now, ai.Usage{InputTokens: 799})
		if err := ask(ctx, s, true); err != nil {
			t.Fatalf("background below 80 %%: %v", err)
		}
		st.SetUsage(now, ai.Usage{InputTokens: 800})
		if err := ask(ctx, s, true); ai.CodeOf(err) != ai.CodeBudgetExhausted {
			t.Errorf("background at 80 %%: %v", err)
		}
		if err := ask(ctx, s, false); err != nil {
			t.Errorf("interactive at 80 %%: %v", err)
		}
		st.SetUsage(now, ai.Usage{InputTokens: 600, OutputTokens: 300, ReasoningTokens: 100})
		if err := ask(ctx, s, false); ai.CodeOf(err) != ai.CodeBudgetExhausted {
			t.Errorf("interactive at 100 %%: %v", err)
		}
	})

	t.Run("tasks", func(t *testing.T) {
		st := aifake.NewStore()
		s := service(t, aifake.Config(), newBlocking(), st, ai.Options{})
		release := make(chan struct{})
		id, err := s.Tasks.Start(ctx, "message", 7, "11111111-1111-4111-8111-111111111111", func(context.Context, string) (any, error) {
			<-release
			return map[string]string{"messageId": "1"}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Tasks.Start(ctx, "message", 7, "11111111-1111-4111-8111-111111111111", nil); !errors.Is(err, ai.ErrTaskRunning) {
			t.Errorf("second task for the session: %v", err)
		}
		if _, err := s.Tasks.Get(ctx, id, 8, false); !errors.Is(err, ai.ErrNotFound) {
			t.Errorf("another user read the task: %v", err)
		}
		if _, err := s.Tasks.Get(ctx, id, 8, true); err != nil {
			t.Errorf("an admin cannot read it: %v", err)
		}
		if _, err := s.Tasks.Get(ctx, "nope", 7, true); !errors.Is(err, ai.ErrNotFound) {
			t.Errorf("malformed id: %v", err)
		}
		close(release)
		waitTask(t, st, id, ai.TaskSucceeded)
		if got := string(st.Task(id).Result); got != `{"messageId":"1"}` {
			t.Errorf("result = %s", got)
		}
		failed, err := s.Tasks.Start(ctx, "message", 7, "", func(context.Context, string) (any, error) {
			return nil, &ai.Error{Code: ai.CodeInvalidOutput, Message: "no valid answer"}
		})
		if err != nil {
			t.Fatal(err)
		}
		waitTask(t, st, failed, ai.TaskFailed)
		if tk := st.Task(failed); tk.ErrorCode != ai.CodeInvalidOutput || tk.ErrorMessage != "no valid answer" {
			t.Errorf("failed task = %+v", tk)
		}
		panicked, _ := s.Tasks.Start(ctx, "message", 7, "", func(context.Context, string) (any, error) { panic("boom") })
		waitTask(t, st, panicked, ai.TaskFailed)
	})

	t.Run("stopped replica's tasks fail (Valkey)", func(t *testing.T) {
		addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
		if addr == "" {
			t.Skip("HELLO_TEST_VALKEY_ADDR not set")
		}
		vk, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, SelectDB: 15, DisableCache: true})
		if err != nil {
			t.Fatal(err)
		}
		defer vk.Close()
		if err := vk.Do(ctx, vk.B().Flushdb().Build()).Error(); err != nil {
			t.Fatal(err)
		}
		st := aifake.NewStore()
		short := ai.WithTiming(ai.Options{}, 100*time.Millisecond, time.Second, time.Hour, time.Hour)
		oa, ob := short, short
		oa.Replica, ob.Replica = "a", "b"
		a := service(t, aifake.Config(), newBlocking(), st, oa)
		b := service(t, aifake.Config(), newBlocking(), st, ob)
		a.SetValkey(vk)
		b.SetValkey(vk)
		block := make(chan struct{})
		defer close(block)
		id, err := a.Tasks.Start(ctx, "message", 7, "", func(ctx context.Context, _ string) (any, error) {
			select {
			case <-block:
			case <-ctx.Done():
			}
			return nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		a.Beat(ctx)
		time.Sleep(1100 * time.Millisecond) // older than the stale window
		a.Beat(ctx)
		b.FailStale(ctx)
		if got := st.Task(id).Status; got == ai.TaskFailed {
			t.Fatal("a task of a live replica was failed")
		}
		time.Sleep(1100 * time.Millisecond) // a's heartbeat expires
		b.FailStale(ctx)
		if tk := st.Task(id); tk.Status != ai.TaskFailed || tk.ErrorCode != ai.CodeInstanceStopped {
			t.Fatalf("stopped replica's task = %+v", tk)
		}
	})
}

func waitTask(t *testing.T, st *aifake.Store, id string, want ai.TaskStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st.Task(id).Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s = %+v, want %s", id, st.Task(id), want)
}
