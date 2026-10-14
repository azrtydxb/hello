package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/assistant/chat"
	"github.com/azrtydxb/hello/internal/auth"
)

// streamAssistant is an AIAssistant whose streamed messages are scripted.
type streamAssistant struct {
	AIAssistant // the other operations are not used here
	posted      chan string
	events      chan chat.Event // closed by the test to end the stream
	err         error
	ctx         chan context.Context // receives the stream's context
}

func (a *streamAssistant) PostMessage(_ context.Context, _ chat.User, _, content string) (int64, string, error) {
	a.posted <- "json:" + content
	return 5, "11111111-1111-4111-8111-111111111111", a.err
}

func (a *streamAssistant) PostMessageStream(ctx context.Context, _ chat.User, _, content string) (<-chan chat.Event, error) {
	if a.err != nil {
		return nil, a.err
	}
	a.posted <- "stream:" + content
	a.ctx <- ctx
	return a.events, nil
}

// sseServer serves postAIMessage as user 7 (operator) without a database.
func sseServer(t *testing.T, a *streamAssistant) *httptest.Server {
	t.Helper()
	s := &server{Config{Assistant: a}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/ai/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		s.postAIMessage(w, r.WithContext(auth.WithActor(r.Context(), auth.Actor{UserID: 7, Role: auth.RoleOperator})))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newStreamAssistant() *streamAssistant {
	return &streamAssistant{posted: make(chan string, 4), events: make(chan chat.Event, 8), ctx: make(chan context.Context, 1)}
}

// reply is a response whose body the helper closes when the test ends.
type reply struct {
	StatusCode int
	Header     http.Header
	Body       io.Reader
}

func postMessage(t *testing.T, ctx context.Context, srv *httptest.Server, accept string) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/v1/ai/sessions/s1/messages", strings.NewReader(`{"content":"Is A up?"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return reply{StatusCode: res.StatusCode, Header: res.Header, Body: res.Body}
}

// frames reads SSE frames: a named event with its data, or a comment.
type frame struct{ event, data, comment string }

func readFrame(t *testing.T, br *bufio.Reader) (frame, error) {
	t.Helper()
	var f frame
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return f, err
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if f != (frame{}) {
				return f, nil
			}
		case strings.HasPrefix(line, ": "):
			f.comment = strings.TrimPrefix(line, ": ")
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// TestPostAIMessageStream (spec ai-agent S-8) fails if Accept:
// text/event-stream does not stream flushed events in order, the JSON call
// stops answering 202 for any other Accept, an idle stream sends no
// keepalive, the event data differs from the payload types, a refusal
// before the task starts is not the usual JSON error, or a client that goes
// away does not cancel the work.
func TestPostAIMessageStream(t *testing.T) {
	ctx := t.Context()

	t.Run("events arrive one by one, in order, then the stream ends", func(t *testing.T) {
		a := newStreamAssistant()
		srv := sseServer(t, a)
		res := postMessage(t, ctx, srv, "text/event-stream")
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" ||
			res.Header.Get("X-Accel-Buffering") != "no" || !strings.Contains(res.Header.Get("Cache-Control"), "no-cache") {
			t.Fatalf("status %d, headers %v", res.StatusCode, res.Header)
		}
		if got := <-a.posted; got != "stream:Is A up?" {
			t.Errorf("posted %q", got)
		}
		br := bufio.NewReader(res.Body)
		send := func(name string, data any) frame {
			a.events <- chat.Event{Name: name, Data: data}
			// The event must reach the client before the next is sent:
			// each is flushed on its own.
			f, err := readFrame(t, br)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}
			return f
		}
		if f := send(chat.EventStarted, chat.Started{TaskID: "t1", MessageID: 5}); f.event != "started" || f.data != `{"taskId":"t1","messageId":5}` {
			t.Errorf("started frame %+v", f)
		}
		if f := send(chat.EventThinking, nil); f.event != "thinking" || f.data != `{}` {
			t.Errorf("thinking frame %+v", f)
		}
		if f := send(chat.EventTool, chat.ToolEvent{OperationID: "listTrunks", Label: "Looking up trunks…"}); f.event != "tool" || !strings.Contains(f.data, `"label":"Looking up trunks…"`) {
			t.Errorf("tool frame %+v", f)
		}
		if f := send(chat.EventDelta, chat.Delta{Text: "Trunk A"}); f.event != "delta" || f.data != `{"text":"Trunk A"}` {
			t.Errorf("delta frame %+v", f)
		}
		msg := chat.Message{ID: 6, Role: chat.RoleAssistant, Content: "Trunk A is up.", ToolCalls: []chat.ToolCall{}, Citations: []int{}}
		f := send(chat.EventDone, chat.Done{TaskID: "t1", MessageID: 5, Message: msg, Usage: chat.Usage{InputTokens: 10, Calls: 1}})
		var done chat.Done
		if f.event != "done" || json.Unmarshal([]byte(f.data), &done) != nil || done.Message.Content != "Trunk A is up." || done.Usage.InputTokens != 10 {
			t.Errorf("done frame %+v", f)
		}
		close(a.events)
		if _, err := readFrame(t, br); err == nil {
			t.Error("the stream did not end after the channel closed")
		}
	})

	t.Run("an idle stream sends keepalive comments", func(t *testing.T) {
		old := sseKeepalive
		sseKeepalive = 10 * time.Millisecond
		t.Cleanup(func() { sseKeepalive = old })
		a := newStreamAssistant()
		srv := sseServer(t, a)
		res := postMessage(t, ctx, srv, "text/event-stream")
		br := bufio.NewReader(res.Body)
		for range 2 {
			f, err := readFrame(t, br)
			if err != nil || f.comment != "keepalive" {
				t.Fatalf("frame %+v, %v", f, err)
			}
		}
		close(a.events)
	})

	t.Run("a client that goes away cancels the work", func(t *testing.T) {
		a := newStreamAssistant()
		srv := sseServer(t, a)
		cctx, cancel := context.WithCancel(ctx)
		res := postMessage(t, cctx, srv, "text/event-stream")
		work := <-a.ctx
		a.events <- chat.Event{Name: chat.EventStarted, Data: chat.Started{TaskID: "t1", MessageID: 5}}
		if _, err := readFrame(t, bufio.NewReader(res.Body)); err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case <-work.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the work's context outlived the client")
		}
	})

	t.Run("any other Accept keeps the JSON answer", func(t *testing.T) {
		for _, accept := range []string{"", "application/json", "*/*", "text/html"} {
			a := newStreamAssistant()
			srv := sseServer(t, a)
			res := postMessage(t, ctx, srv, accept)
			var body map[string]any
			if res.StatusCode != http.StatusAccepted || json.NewDecoder(res.Body).Decode(&body) != nil || body["messageId"] != float64(5) {
				t.Errorf("Accept %q: %d %v", accept, res.StatusCode, body)
			}
			if got := <-a.posted; got != "json:Is A up?" {
				t.Errorf("Accept %q posted %q", accept, got)
			}
		}
	})

	t.Run("a refusal before the task starts is the JSON error", func(t *testing.T) {
		for _, tc := range []struct {
			err    error
			status int
			code   string
		}{
			{chat.ErrTaskRunning, http.StatusConflict, "task_running"},
			{chat.ErrNotFound, http.StatusNotFound, "not_found"},
			{&ai.Error{Code: ai.CodeBusy, Message: "no slot"}, http.StatusTooManyRequests, ai.CodeBusy},
			{&ai.Error{Code: ai.CodeDisabled}, http.StatusServiceUnavailable, ai.CodeDisabled},
		} {
			a := newStreamAssistant()
			a.err = tc.err
			srv := sseServer(t, a)
			res := postMessage(t, ctx, srv, "text/event-stream")
			var body struct{ Error struct{ Code string } }
			_ = json.NewDecoder(res.Body).Decode(&body)
			if res.StatusCode != tc.status || body.Error.Code != tc.code || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
				t.Errorf("%v: %d %q %s", tc.err, res.StatusCode, body.Error.Code, res.Header.Get("Content-Type"))
			}
		}
	})

	t.Run("an Accept list naming event-stream streams", func(t *testing.T) {
		for accept, want := range map[string]bool{
			"text/event-stream":                         true,
			"text/event-stream; charset=utf-8":          true,
			"application/json, text/event-stream;q=0.9": true,
			"TEXT/EVENT-STREAM":                         true,
			"application/json":                          false,
			"text/*":                                    false,
			"":                                          false,
		} {
			r := httptest.NewRequest("POST", "/", nil)
			if accept != "" {
				r.Header.Set("Accept", accept)
			}
			if got := wantsEventStream(r); got != want {
				t.Errorf("Accept %q: %v", accept, got)
			}
		}
	})
}
