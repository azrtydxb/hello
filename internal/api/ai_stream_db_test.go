package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/ai/assistant"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
)

// sseEvents parses a finished text/event-stream body into its named events;
// comment lines are skipped.
func sseEvents(t *testing.T, body []byte) (names []string, data []map[string]any) {
	t.Helper()
	for block := range strings.SplitSeq(strings.TrimSpace(string(body)), "\n\n") {
		var name, payload string
		for line := range strings.SplitSeq(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				payload = strings.TrimPrefix(line, "data: ")
			}
		}
		if name == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("event %s has data %q: %v", name, payload, err)
		}
		names, data = append(names, name), append(data, m)
	}
	return names, data
}

// TestAssistantMessageStream (HTTP and PostgreSQL half, spec S-8) fails if
// Accept: text/event-stream does not stream the answer from started to a
// final event equal to the stored message, the stored message and task row
// differ from the JSON flow's, a refusal before the task starts is an event
// instead of the JSON error, or the session is not free for the next message
// when the stream ends.
func TestAssistantMessageStream(t *testing.T) {
	late := &lateAssistant{}
	agent, model, _ := aifake.Service(t, aifake.Config())
	e := newEnvConfig(t, Config{Live: noLive{}, Assistant: late, AIAgent: agent}, nil)
	ctx := context.Background()
	tasks := &dbTasks{db: e.db}
	spec, err := apispec.Load(openAPI)
	if err != nil {
		t.Fatal(err)
	}
	a, err := assistant.New(assistant.Config{Store: e.st, Tasks: tasks,
		Generate: func(ctx context.Context, c ai.Call[assistant.Answer]) (assistant.Answer, ai.Usage, error) {
			return ai.Generate(ctx, agent, c)
		},
		DataBlock: func(v any) string { b, _ := json.Marshal(v); return "<data>" + string(b) + "</data>" },
		API:       e.srv.Config.Handler, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	late.Assistant = a
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateUser(ctx, "test", "stream-oscar", hash, auth.RoleOperator); err != nil {
		t.Fatal(err)
	}
	oscar := e.client()
	oscar.must(http.StatusNoContent, "POST", "/api/v1/auth/login", map[string]string{"username": "stream-oscar", "password": testPassword})
	sess := oscar.must(http.StatusCreated, "POST", "/api/v1/ai/sessions", map[string]any{}).json(t)
	sessionID := sess["id"].(string)
	base := "/api/v1/ai/sessions/" + sessionID

	answer := assistant.Answer{Answer: "No trunks are configured.\nNothing to check.", Citations: []int{0}}
	model.Streams = [][]provider.StreamPart{
		aifake.StreamReasoning("Look at the trunks.", aifake.StreamToolCall("c1", "listTrunks", map[string]any{})),
		aifake.StreamReasoning("Answer.", aifake.StreamJSON(answer, 6)),
		aifake.StreamJSON(assistant.Answer{Answer: "Still none.", Citations: []int{}}, 6),
	}
	oscar.header.Set("Accept", "text/event-stream")
	r := oscar.must(http.StatusOK, "POST", base+"/messages", map[string]any{"content": "Are my trunks up?"})
	if ct := r.header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type %q", ct)
	}
	names, data := sseEvents(t, r.body)
	if len(names) < 4 || names[0] != "started" || names[len(names)-1] != "done" {
		t.Fatalf("events %v", names)
	}
	var streamed strings.Builder
	tools := 0
	for i, n := range names {
		switch n {
		case "tool":
			tools++
			if data[i]["operationId"] != "listTrunks" || data[i]["label"] != "Looking up trunks…" || len(data[i]) != 2 {
				t.Errorf("tool event %v", data[i])
			}
		case "delta":
			streamed.WriteString(data[i]["text"].(string))
		case "error":
			t.Errorf("error event %v", data[i])
		}
	}
	if tools != 1 || streamed.String() != answer.Answer {
		t.Errorf("tools %d, streamed %q", tools, streamed.String())
	}

	// The final event is the stored message, as getAISession lists it.
	tasks.wg.Wait()
	oscar.header.Del("Accept")
	var d assistant.Detail
	if err := json.Unmarshal(oscar.must(http.StatusOK, "GET", base, nil).body, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Messages) != 2 || d.Task != nil {
		t.Fatalf("messages %d, task %+v", len(d.Messages), d.Task)
	}
	// Compare decoded values: the event's message is a map (sorted keys),
	// the stored one a struct (field order).
	wantJSON, _ := json.Marshal(d.Messages[1])
	var want any
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatal(err)
	}
	if got := data[len(data)-1]["message"]; !reflect.DeepEqual(got, want) {
		t.Errorf("final message %v\nstored        %v", got, want)
	}
	if d.Messages[1].Content != answer.Answer || len(d.Messages[1].ToolCalls) != 1 || d.Messages[1].ToolCalls[0].OperationID != "listTrunks" {
		t.Errorf("stored %+v", d.Messages[1])
	}
	if last := data[len(data)-1]; last["taskId"] != *d.Messages[1].TaskID || last["messageId"] != float64(d.Messages[0].ID) {
		t.Errorf("done = %v", last)
	}
	var status string
	if err := e.db.QueryRowContext(ctx, `SELECT status FROM ai_tasks WHERE id = $1`, *d.Messages[1].TaskID).Scan(&status); err != nil || status != "succeeded" {
		t.Errorf("task status %q, %v", status, err)
	}

	// The session is free again: a second streamed message is accepted.
	oscar.header.Set("Accept", "text/event-stream")
	r = oscar.must(http.StatusOK, "POST", base+"/messages", map[string]any{"content": "And now?"})
	if names, _ := sseEvents(t, r.body); names[len(names)-1] != "done" {
		t.Errorf("second message events %v", names)
	}
	tasks.wg.Wait()

	// Refusals before the task starts are the JSON errors, not events.
	tasks.busy = true
	if r := oscar.do("POST", base+"/messages", map[string]any{"content": "again"}); r.code != http.StatusTooManyRequests || !strings.Contains(string(r.body), "ai_busy") {
		t.Errorf("busy: %d %s", r.code, r.body)
	}
	tasks.busy = false
	oscar.must(http.StatusBadRequest, "POST", base+"/messages", map[string]any{"content": ""})
	oscar.must(http.StatusNotFound, "POST", "/api/v1/ai/sessions/00000000-0000-4000-8000-000000000001/messages", map[string]any{"content": "x"})
	var n int
	_ = e.db.QueryRowContext(ctx, `SELECT count(*) FROM ai_messages WHERE session_id = $1 AND role = 'assistant'`, sessionID).Scan(&n)
	if n != 2 {
		t.Errorf("%d assistant messages stored, want 2", n)
	}
}
