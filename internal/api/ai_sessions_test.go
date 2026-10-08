package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/ai/assistant"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
)

// lateAssistant lets the test build the assistant on the env's store after
// the handler exists.
type lateAssistant struct{ *assistant.Assistant }

// dbTasks records tasks in ai_tasks as ai.Tasks does and runs them in a
// goroutine; busy makes Start refuse as a full service would.
type dbTasks struct {
	db   *sql.DB
	busy bool
	wg   sync.WaitGroup
}

func (d *dbTasks) Start(ctx context.Context, kind string, userID int64, sessionID string, fn ai.TaskFunc) (string, error) {
	if d.busy {
		return "", &ai.Error{Code: ai.CodeBusy, Message: "no model slot within 5 seconds"}
	}
	var id string
	if err := d.db.QueryRowContext(ctx, `INSERT INTO ai_tasks (id, kind, requested_by, session_id, replica, status, started_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 'test', 'running', now()) RETURNING id::text`, kind, userID, sessionID).Scan(&id); err != nil {
		return "", err
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ctx := context.Background()
		res, err := fn(ctx, id)
		status, code := "succeeded", ""
		if err != nil {
			status, code = "failed", ai.CodeOf(err)
		}
		b, _ := json.Marshal(res)
		_, _ = d.db.ExecContext(ctx, `UPDATE ai_tasks SET status = $2, error_code = NULLIF($3, ''), result = $4::jsonb, finished_at = now() WHERE id = $1`,
			id, status, code, string(b))
	}()
	return id, nil
}

// TestAssistantSessions (HTTP and store half, spec S-8, S-9) fails if a
// viewer can post a message, another non-admin user can read a session, a
// second message during a running task is accepted, more than 20 messages
// reach the model, or an answer is not stored with its task.
func TestAssistantSessions(t *testing.T) {
	late := &lateAssistant{}
	agent, _, _ := aifake.Service(t, aifake.Config())
	e := newEnvConfig(t, Config{Live: noLive{}, Assistant: late, AIAgent: agent}, nil)
	ctx := context.Background()
	tasks := &dbTasks{db: e.db}
	release := make(chan struct{})
	var mu sync.Mutex
	var seen []ai.Call[assistant.Answer]
	generate := func(ctx context.Context, c ai.Call[assistant.Answer]) (assistant.Answer, ai.Usage, error) {
		mu.Lock()
		seen = append(seen, c)
		mu.Unlock()
		<-release
		return assistant.Answer{Answer: "All trunks are up.", Citations: []int{}}, ai.Usage{}, nil
	}
	spec, err := apispec.Load(openAPI)
	if err != nil {
		t.Fatal(err)
	}
	a, err := assistant.New(assistant.Config{Store: e.st, Tasks: tasks, Generate: generate,
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
	for name, role := range map[string]auth.Role{"vera": auth.RoleViewer, "oscar": auth.RoleOperator, "olga": auth.RoleOperator} {
		if _, err := e.st.CreateUser(ctx, "test", name, hash, role); err != nil {
			t.Fatal(err)
		}
	}
	as := func(name string) *client {
		c := e.client()
		c.must(http.StatusNoContent, "POST", "/api/v1/auth/login", map[string]string{"username": name, "password": testPassword})
		return c
	}
	admin, vera, oscar, olga := e.login(), as("vera"), as("oscar"), as("olga")

	sess := oscar.must(http.StatusCreated, "POST", "/api/v1/ai/sessions", map[string]any{}).json(t)
	id := sess["id"].(string)
	base := "/api/v1/ai/sessions/" + id
	oscar.must(http.StatusBadRequest, "POST", "/api/v1/ai/sessions", map[string]any{"title": 5})
	oscar.must(http.StatusBadRequest, "PATCH", base, map[string]any{"title": ""})
	oscar.must(http.StatusBadRequest, "POST", base+"/messages", map[string]any{"content": strings.Repeat("q", assistant.MaxMessageLen+1)})

	// A viewer cannot post or create; other users see nothing.
	vera.must(http.StatusForbidden, "POST", base+"/messages", map[string]any{"content": "hi"})
	vera.must(http.StatusForbidden, "POST", "/api/v1/ai/sessions", map[string]any{})
	olga.must(http.StatusNotFound, "GET", base, nil)
	olga.must(http.StatusNotFound, "PATCH", base, map[string]any{"title": "mine now"})
	olga.must(http.StatusNotFound, "DELETE", base, nil)
	olga.must(http.StatusNotFound, "POST", base+"/messages", map[string]any{"content": "hi"})
	olga.must(http.StatusNotFound, "GET", "/api/v1/ai/sessions/not-a-uuid", nil)
	var list struct {
		Items []assistant.Session `json:"items"`
	}
	_ = json.Unmarshal(olga.must(http.StatusOK, "GET", "/api/v1/ai/sessions", nil).body, &list)
	if len(list.Items) != 0 {
		t.Errorf("olga lists %v", list.Items)
	}
	_ = json.Unmarshal(admin.must(http.StatusOK, "GET", "/api/v1/ai/sessions", nil).body, &list)
	if len(list.Items) != 1 {
		t.Errorf("admin lists %v", list.Items)
	}

	// 30 earlier messages; the model gets the last 20 with the new one.
	for i := range 30 {
		if _, err := e.db.ExecContext(ctx, `INSERT INTO ai_messages (session_id, role, content) VALUES ($1, 'user', $2)`, id, "old "+string(rune('a'+i%26))); err != nil {
			t.Fatal(err)
		}
	}
	acc := oscar.must(http.StatusAccepted, "POST", base+"/messages", map[string]any{"content": "Are my trunks up?"}).json(t)
	if acc["taskId"] == "" || acc["messageId"] == nil {
		t.Fatalf("accepted %v", acc)
	}
	r := oscar.must(http.StatusConflict, "POST", base+"/messages", map[string]any{"content": "and now?"})
	if !strings.Contains(string(r.body), "task_running") {
		t.Errorf("second message: %s", r.body)
	}
	oscar.must(http.StatusConflict, "DELETE", base, nil)
	detail := admin.must(http.StatusOK, "GET", base, nil).json(t)
	if task, _ := detail["task"].(map[string]any); task == nil || task["id"] != acc["taskId"] {
		t.Errorf("running task not shown: %v", detail["task"])
	}
	if s := detail["session"].(map[string]any); s["title"] != "Are my trunks up?" {
		t.Errorf("title %v", s["title"])
	}
	close(release)
	tasks.wg.Wait()

	mu.Lock()
	c := seen[0]
	mu.Unlock()
	hist := c.Data.(map[string]any)["history"]
	b, _ := json.Marshal(hist)
	var entries []map[string]any
	_ = json.Unmarshal(b, &entries)
	if len(entries)+1 != assistant.HistoryWindow || c.Prompt != "Are my trunks up?" {
		t.Errorf("%d messages reached the model, want %d", len(entries)+1, assistant.HistoryWindow)
	}

	var d assistant.Detail
	if err := json.Unmarshal(oscar.must(http.StatusOK, "GET", base, nil).body, &d); err != nil {
		t.Fatal(err)
	}
	last := d.Messages[len(d.Messages)-1]
	if last.Role != assistant.RoleAssistant || last.Content != "All trunks are up." || last.TaskID == nil || *last.TaskID != acc["taskId"] || d.Task != nil {
		t.Errorf("answer %+v, task %+v", last, d.Task)
	}

	tasks.busy = true
	if r := oscar.do("POST", base+"/messages", map[string]any{"content": "again"}); r.code != http.StatusTooManyRequests || !strings.Contains(string(r.body), "ai_busy") {
		t.Errorf("busy: %d %s", r.code, r.body)
	}
	tasks.busy = false

	got := oscar.must(http.StatusOK, "PATCH", base, map[string]any{"title": "Trunks"}).json(t)
	if got["title"] != "Trunks" {
		t.Errorf("renamed %v", got)
	}
	oscar.must(http.StatusNoContent, "DELETE", base, nil)
	oscar.must(http.StatusNotFound, "GET", base, nil)
	var n int
	_ = e.db.QueryRowContext(ctx, `SELECT count(*) FROM ai_messages WHERE session_id = $1`, id).Scan(&n)
	if n != 0 {
		t.Errorf("%d messages survived their session", n)
	}
}

// TestAssistantDisabled fails if a session route answers anything but 503
// ai_disabled while AI is off.
func TestAssistantDisabled(t *testing.T) {
	e := newEnv(t, noLive{})
	c := e.login()
	for _, rq := range [][2]string{{"GET", "/api/v1/ai/sessions"}, {"POST", "/api/v1/ai/sessions"},
		{"GET", "/api/v1/ai/sessions/00000000-0000-0000-0000-000000000001"}, {"POST", "/api/v1/ai/sessions/00000000-0000-0000-0000-000000000001/messages"}} {
		var body any
		if rq[0] == "POST" {
			body = map[string]any{"content": "x"}
		}
		r := c.do(rq[0], rq[1], body)
		if r.code != http.StatusServiceUnavailable || !strings.Contains(string(r.body), "ai_disabled") {
			t.Errorf("%s %s = %d %s", rq[0], rq[1], r.code, r.body)
		}
	}
}
