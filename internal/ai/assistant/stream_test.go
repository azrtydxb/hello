package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/ai/assistant/chat"
)

// streamRig is an assistant over a given generate function, with the fake
// store, tasks and API.
type streamRig struct {
	a     *Assistant
	store *memStore
	tasks *fakeTasks
	api   *fakeAPI
}

func newStreamRig(t *testing.T, gen GenerateFunc) *streamRig {
	t.Helper()
	r := &streamRig{tasks: newFakeTasks(), api: &fakeAPI{bodies: map[string]string{}}}
	r.store = newMemStore(r.tasks)
	a, err := New(Config{Store: r.store, Tasks: r.tasks, Generate: gen, DataBlock: testDataBlock, API: r.api, Spec: loadSpec(t)})
	if err != nil {
		t.Fatal(err)
	}
	r.a = a
	return r
}

// realGenerate is ai.Generate over a service on the mock model.
func realGenerate(svc *ai.Service) GenerateFunc {
	return func(ctx context.Context, c ai.Call[Answer]) (Answer, ai.Usage, error) {
		return ai.Generate(ctx, svc, c)
	}
}

// post starts a streamed message in a new session of user 7.
func (r *streamRig) post(t *testing.T, ctx context.Context, content string) (Session, <-chan chat.Event) {
	t.Helper()
	u := User{ID: 7}
	s, err := r.a.CreateSession(ctx, u, "")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := r.a.PostMessageStream(ctx, u, s.ID, content)
	if err != nil {
		t.Fatal(err)
	}
	return s, ch
}

// collect reads events until the channel closes.
func collect(t *testing.T, ch <-chan chat.Event) []chat.Event {
	t.Helper()
	var out []chat.Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("the stream did not close; events so far: %v", names(out))
		}
	}
}

func names(evs []chat.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Name
	}
	return out
}

// TestPostMessageStream (spec S-6, S-8) fails if a streamed message does
// not announce itself first and end with exactly one terminal event, the
// answer text arrives other than as the answer property's deltas, a tool
// call leaks its arguments or lacks a label, the final event differs from
// the stored message, or the stream stays open after the task.
func TestPostMessageStream(t *testing.T) {
	ctx := t.Context()

	t.Run("started, thinking, tool, deltas, done", func(t *testing.T) {
		svc, m, _ := aifake.Service(t, aifake.Config())
		const secretArg = "ext-SECRET-ARGUMENT"
		answer := Answer{Answer: "Extension 201 is registered.\nSecond line.", Citations: []int{0}}
		m.Streams = [][]provider.StreamPart{
			// The tool phase: a call, then plain text that is never streamed.
			aifake.StreamReasoning("I should look.", aifake.StreamToolCall("c1", "getExtension", map[string]string{"id": secretArg})),
			aifake.StreamText("TOOL-PHASE-PROSE: 201 is registered."),
			// The answer phase.
			aifake.StreamReasoning("Now answer.", aifake.StreamJSON(answer, 7)),
			// The next message: no data needed, plain text, then its answer.
			aifake.StreamToolCall("n1", ai.NoDataTool, struct{}{}),
			aifake.StreamText("202 too."),
			aifake.StreamJSON(Answer{Answer: "And 202 too.", Citations: []int{}}, 7),
		}
		r := newStreamRig(t, realGenerate(svc))
		r.api.bodies["/api/v1/extensions/"+secretArg] = `{"id":"` + secretArg + `","number":"201"}`
		s, ch := r.post(t, ctx, "Is 201 registered?")
		evs := collect(t, ch)

		if evs[0].Name != chat.EventStarted {
			t.Fatalf("first event %q, events %v", evs[0].Name, names(evs))
		}
		last := evs[len(evs)-1]
		if last.Name != chat.EventDone {
			t.Fatalf("last event %q, events %v", last.Name, names(evs))
		}
		var thinking, tools int
		var text strings.Builder
		firstDelta, toolAt := -1, -1
		for i, e := range evs {
			switch e.Name {
			case chat.EventThinking:
				thinking++
			case chat.EventTool:
				tools++
				toolAt = i
				te := e.Data.(chat.ToolEvent)
				if te.OperationID != "getExtension" || te.Label != "Looking up an extension…" {
					t.Errorf("tool event %+v", te)
				}
				if b, _ := json.Marshal(e.Data); strings.Contains(string(b), secretArg) {
					t.Errorf("the tool event carries its arguments: %s", b)
				}
			case chat.EventDelta:
				if firstDelta < 0 {
					firstDelta = i
				}
				text.WriteString(e.Data.(chat.Delta).Text)
			case chat.EventReset:
				t.Errorf("reset at %d: the tool phase streamed text; %v", i, names(evs))
			case chat.EventDone, chat.EventError:
				if i != len(evs)-1 {
					t.Errorf("terminal event %q at %d of %d", e.Name, i, len(evs))
				}
			}
		}
		if thinking < 1 || tools != 1 || toolAt > firstDelta {
			t.Errorf("thinking %d, tools %d, tool at %d, first delta at %d; %v", thinking, tools, toolAt, firstDelta, names(evs))
		}
		if text.String() != answer.Answer {
			t.Errorf("streamed %q, want %q", text.String(), answer.Answer)
		}
		if started := evs[0].Data.(chat.Started); started.TaskID == "" || started.MessageID != 1 {
			t.Errorf("started = %+v", started)
		}

		// The final event is the stored message.
		done := last.Data.(chat.Done)
		d, err := r.a.GetSession(ctx, User{ID: 7}, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		var stored Message
		for _, msg := range d.Messages {
			if msg.Role == RoleAssistant {
				stored = msg
			}
		}
		if got, want := mustJSON(t, done.Message), mustJSON(t, stored); got != want {
			t.Errorf("final message %s\nstored        %s", got, want)
		}
		if done.Message.Content != answer.Answer || len(done.Message.Citations) != 1 || len(done.Message.ToolCalls) != 1 {
			t.Errorf("final message %+v", done.Message)
		}
		if done.TaskID != evs[0].Data.(chat.Started).TaskID || done.MessageID != 1 || done.Usage.Calls != 3 || done.Usage.InputTokens == 0 {
			t.Errorf("done = %+v", done)
		}
		if n := len(r.store.assistantMessages()); n != 1 {
			t.Errorf("%d assistant messages stored", n)
		}
		if d.Task != nil {
			t.Errorf("task still active after the stream closed: %+v", d.Task)
		}
		// A client reacting to the final event may post the next message.
		next, err := r.a.PostMessageStream(ctx, User{ID: 7}, s.ID, "and 202?")
		if err != nil {
			t.Fatalf("posting right after done: %v", err)
		}
		if evs := collect(t, next); evs[len(evs)-1].Name != chat.EventDone {
			t.Errorf("the next message ended with %v", names(evs))
		}
	})

	t.Run("a failing model ends with an error event and stores nothing", func(t *testing.T) {
		svc, m, _ := aifake.Service(t, aifake.Config())
		m.Err = errors.New("connection refused")
		r := newStreamRig(t, realGenerate(svc))
		_, ch := r.post(t, ctx, "hello?")
		evs := collect(t, ch)
		last := evs[len(evs)-1]
		if names(evs)[0] != chat.EventStarted || last.Name != chat.EventError {
			t.Fatalf("events %v", names(evs))
		}
		if e := last.Data.(chat.ErrorEvent); e.Code != ai.CodeProviderError || e.Message == "" || strings.Contains(e.Message, "refused") {
			t.Errorf("error event %+v", e)
		}
		if n := len(r.store.assistantMessages()); n != 0 {
			t.Errorf("%d assistant messages stored for a failed task", n)
		}
	})

	t.Run("an invalid answer asks again and voids the streamed text", func(t *testing.T) {
		svc, m, _ := aifake.Service(t, aifake.Config())
		m.Streams = [][]provider.StreamPart{
			aifake.StreamToolCall("n1", ai.NoDataTool, struct{}{}),
			aifake.StreamText("Hello there."),
			aifake.StreamJSON(Answer{Answer: "Cites a call that was never made.", Citations: []int{3}}, 9),
			aifake.StreamJSON(Answer{Answer: "Fixed.", Citations: []int{}}, 9),
		}
		r := newStreamRig(t, realGenerate(svc))
		_, ch := r.post(t, ctx, "hi")
		evs := collect(t, ch)
		var text strings.Builder
		resets := 0
		for _, e := range evs {
			switch e.Name {
			case chat.EventDelta:
				text.WriteString(e.Data.(chat.Delta).Text)
			case chat.EventReset:
				resets++
				text.Reset()
			}
		}
		if text.String() != "Fixed." || resets != 1 || evs[len(evs)-1].Name != chat.EventDone {
			t.Errorf("text %q, resets %d, events %v", text.String(), resets, names(evs))
		}
	})

	t.Run("refusals before the task starts are errors, not events", func(t *testing.T) {
		r := newStreamRig(t, func(ctx context.Context, c ai.Call[Answer]) (Answer, ai.Usage, error) {
			<-ctx.Done()
			return Answer{}, ai.Usage{}, ctx.Err()
		})
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		s, ch := r.post(t, cctx, "first")
		if _, err := r.a.PostMessageStream(ctx, User{ID: 7}, s.ID, "second"); !errors.Is(err, ErrTaskRunning) {
			t.Errorf("second message while running: %v", err)
		}
		if _, err := r.a.PostMessageStream(ctx, User{ID: 8}, s.ID, "stolen"); !errors.Is(err, ErrNotFound) {
			t.Errorf("another user: %v", err)
		}
		if _, err := r.a.PostMessageStream(ctx, User{ID: 7}, s.ID, "  "); !errors.Is(err, ErrInvalid) {
			t.Errorf("empty message: %v", err)
		}
		cancel()
		collect(t, ch)
	})
}

// TestPostMessageStreamDisconnect (spec S-8) fails if the work goes on
// after the reader is gone, a cancelled task stores a message or stays
// active, a stream dropped after the answer exists loses or repeats the
// stored message, or a slow reader makes the task stop.
func TestPostMessageStreamDisconnect(t *testing.T) {
	t.Run("cancelling stops the model call", func(t *testing.T) {
		stopped := make(chan error, 1)
		started := make(chan struct{})
		r := newStreamRig(t, func(ctx context.Context, c ai.Call[Answer]) (Answer, ai.Usage, error) {
			c.Stream.Text("Partial ")
			close(started)
			<-ctx.Done()
			stopped <- context.Cause(ctx)
			return Answer{}, ai.Usage{}, ctx.Err()
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s, ch := r.post(t, ctx, "a long question")
		<-started
		cancel()
		select {
		case cause := <-stopped:
			if !errors.Is(cause, errClientGone) {
				t.Errorf("cancelled with %v", cause)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the model call kept running after the reader went")
		}
		evs := collect(t, ch)
		for _, e := range evs {
			if e.Name == chat.EventDone {
				t.Errorf("a done event for a cancelled task: %v", names(evs))
			}
		}
		r.tasks.wg.Wait()
		if n := len(r.store.assistantMessages()); n != 0 {
			t.Errorf("%d assistant messages stored for a cancelled task", n)
		}
		out := r.tasks.results["10000000-0000-0000-0000-000000000001"]
		if ai.CodeOf(out.err) != ai.CodeCanceled {
			t.Errorf("task ended with %v (%q)", out.err, ai.CodeOf(out.err))
		}
		if d, _ := r.a.GetSession(t.Context(), User{ID: 7}, s.ID); d.Task != nil {
			t.Errorf("task still active: %+v", d.Task)
		}
	})

	t.Run("a stream dropped once the answer exists loses and repeats nothing", func(t *testing.T) {
		var cancel context.CancelFunc
		r := newStreamRig(t, func(ctx context.Context, c ai.Call[Answer]) (Answer, ai.Usage, error) {
			c.Stream.Text("The answer")
			cancel() // the client goes while the answer is complete
			// Wait until the cancellation reached this call, as it would
			// in production before the model call returns.
			<-ctx.Done()
			return Answer{Answer: "The answer", Citations: []int{}}, ai.Usage{Calls: 1}, nil
		})
		var ctx context.Context
		ctx, cancel = context.WithCancel(t.Context())
		defer cancel()
		s, ch := r.post(t, ctx, "question")
		collect(t, ch)
		r.tasks.wg.Wait()
		msgs := r.store.assistantMessages()
		if len(msgs) != 1 || msgs[0].Content != "The answer" {
			t.Fatalf("stored %+v", msgs)
		}
		d, _ := r.a.GetSession(t.Context(), User{ID: 7}, s.ID)
		var assistantMsgs int
		for _, m := range d.Messages {
			if m.Role == RoleAssistant {
				assistantMsgs++
			}
		}
		if assistantMsgs != 1 || d.Task != nil {
			t.Errorf("%d assistant messages, task %+v", assistantMsgs, d.Task)
		}
	})

	t.Run("a reader that never reads does not wedge the task", func(t *testing.T) {
		r := newStreamRig(t, func(ctx context.Context, c ai.Call[Answer]) (Answer, ai.Usage, error) {
			for range 10 * eventBuffer {
				c.Stream.Text("x")
			}
			return Answer{Answer: "done", Citations: []int{}}, ai.Usage{}, nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		s, _ := r.post(t, ctx, "question")
		time.Sleep(50 * time.Millisecond)
		cancel() // the reader gives up without ever reading
		r.tasks.wg.Wait()
		if d, _ := r.a.GetSession(t.Context(), User{ID: 7}, s.ID); d.Task != nil {
			t.Errorf("task wedged: %+v", d.Task)
		}
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
