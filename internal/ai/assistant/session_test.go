package assistant

import (
	"errors"
	"strings"
	"testing"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"

	"github.com/azrtydxb/hello/internal/ai"
)

// TestAssistantSessions (assistant half, spec S-8, S-9) fails if another
// non-admin user can read or change a session, a second message during a
// running task is accepted, more than 20 messages reach the model, history
// reaches it other than as data, an answer cites a tool call that was not
// made, a proposal in an answer is stored unvalidated, or an answer is not
// stored with its tool calls. The HTTP and PostgreSQL half is in
// internal/api.
func TestAssistantSessions(t *testing.T) {
	ctx := t.Context()

	t.Run("ownership", func(t *testing.T) {
		r := newRig(t)
		owner, other, admin := User{ID: 7}, User{ID: 8}, User{ID: 9, Admin: true}
		s, err := r.a.CreateSession(ctx, owner, "mine")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.a.GetSession(ctx, other, s.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("other user read: %v", err)
		}
		if _, err := r.a.RenameSession(ctx, other, s.ID, "x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("other user renamed: %v", err)
		}
		if err := r.a.DeleteSession(ctx, other, s.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("other user deleted: %v", err)
		}
		if _, _, err := r.a.PostMessage(ctx, other, s.ID, "hi"); !errors.Is(err, ErrNotFound) {
			t.Errorf("other user posted: %v", err)
		}
		if _, _, err := r.a.PostMessage(ctx, admin, s.ID, "hi"); !errors.Is(err, ErrNotFound) {
			t.Errorf("admin posted as the owner: %v", err)
		}
		if l, _ := r.a.ListSessions(ctx, other); len(l) != 0 {
			t.Errorf("other user lists %v", l)
		}
		if _, err := r.a.GetSession(ctx, admin, s.ID); err != nil {
			t.Errorf("admin view: %v", err)
		}
		if l, _ := r.a.ListSessions(ctx, admin); len(l) != 1 {
			t.Errorf("admin lists %v", l)
		}
		if _, err := r.a.RenameSession(ctx, owner, s.ID, strings.Repeat("t", MaxTitleLen+1)); !errors.Is(err, ErrInvalid) {
			t.Errorf("long title: %v", err)
		}
		if _, _, err := r.a.PostMessage(ctx, owner, s.ID, strings.Repeat("q", MaxMessageLen+1)); !errors.Is(err, ErrInvalid) {
			t.Errorf("long message: %v", err)
		}
	})

	t.Run("one task per session, title from the first message", func(t *testing.T) {
		r := newRig(t, textResp(Answer{Answer: "Hello.", Citations: []int{}}))
		r.tasks.hold = make(chan struct{})
		u := User{ID: 7}
		s, _ := r.a.CreateSession(ctx, u, "")
		_, task, err := r.a.PostMessage(ctx, u, s.ID, "Why did 201's call fail?\nDetails follow")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.a.PostMessage(ctx, u, s.ID, "and again"); !errors.Is(err, ErrTaskRunning) {
			t.Errorf("second message while running: %v", err)
		}
		if err := r.a.DeleteSession(ctx, u, s.ID); !errors.Is(err, ErrTaskRunning) {
			t.Errorf("delete while running: %v", err)
		}
		d, _ := r.a.GetSession(ctx, u, s.ID)
		if d.Task == nil || d.Task.ID != task {
			t.Errorf("running task not shown: %+v", d.Task)
		}
		if d.Session.Title != "Why did 201's call fail?" {
			t.Errorf("title %q", d.Session.Title)
		}
		close(r.tasks.hold)
		if o := r.tasks.outcome(t, task); o.err != nil {
			t.Fatal(o.err)
		}
		if _, _, err := r.a.PostMessage(ctx, u, s.ID, strings.Repeat("y", MaxMessageLen)); err != nil {
			t.Errorf("message after the task: %v", err)
		}
		r.tasks.wg.Wait()
	})

	t.Run("the model sees at most 20 messages, history as data", func(t *testing.T) {
		r := newRig(t, textResp(Answer{Answer: "Fine.", Citations: []int{}}))
		u := User{ID: 7}
		s, _ := r.a.CreateSession(ctx, u, "")
		r.store.addHistory(s.ID, 30)
		_, task, err := r.a.PostMessage(ctx, u, s.ID, "current question")
		if err != nil {
			t.Fatal(err)
		}
		if o := r.tasks.outcome(t, task); o.err != nil {
			t.Fatal(o.err)
		}
		c := r.calls[0]
		if c.Prompt != "current question" || c.System != systemPrompt || c.Feature != Feature {
			t.Errorf("prompt %q, feature %q", c.Prompt, c.Feature)
		}
		hist := c.Data.(map[string]any)["history"].([]historyEntry)
		if len(hist)+1 > HistoryWindow {
			t.Errorf("%d messages reached the model, limit %d", len(hist)+1, HistoryWindow)
		}
		if len(hist) != HistoryWindow-1 || hist[len(hist)-1].Content != "old message 29" || hist[0].Content != "old message 11" {
			t.Errorf("history %d entries, first %q", len(hist), hist[0].Content)
		}
		if strings.Contains(c.System, "old message") || strings.Contains(c.Prompt, "old message") {
			t.Error("history reached the instructions")
		}
	})

	t.Run("answers are stored with their tool calls and checked citations", func(t *testing.T) {
		r := newRig(t,
			toolResp(call("c1", "listCDRs", `{"limit":5}`)),
			textResp(Answer{Answer: "Two failed.", Citations: []int{0, 3}}),
			textResp(Answer{Answer: "Two calls failed with 503.", Citations: []int{0}}),
		)
		r.api.bodies["/api/v1/cdrs"] = `{"items":[{"id":1,"disposition":"failed"}]}`
		_, o := r.ask(t, "which calls failed?")
		if o.err != nil {
			t.Fatal(o.err)
		}
		msgs := r.store.assistantMessages()
		if len(msgs) != 1 {
			t.Fatalf("stored %d answers", len(msgs))
		}
		m := msgs[0]
		if m.Content != "Two calls failed with 503." || len(m.Citations) != 1 || m.Citations[0] != 0 {
			t.Errorf("stored %q citing %v", m.Content, m.Citations)
		}
		if len(m.ToolCalls) != 1 || m.ToolCalls[0].OperationID != "listCDRs" || m.ToolCalls[0].Status != 200 ||
			string(m.ToolCalls[0].Arguments) != `{"limit":5}` {
			t.Errorf("tool calls %+v", m.ToolCalls)
		}
		if q := r.api.seen()[0]; q.Path != "/api/v1/cdrs" || q.Agent.UserID != 7 || q.Agent.TaskID == "" {
			t.Errorf("read %+v", q)
		}
	})

	t.Run("a citation of a call never made is never stored", func(t *testing.T) {
		bad := textResp(Answer{Answer: "Made up.", Citations: []int{2}})
		r := newRig(t, bad, bad, bad)
		_, o := r.ask(t, "anything?")
		if ai.CodeOf(o.err) != ai.CodeInvalidOutput {
			t.Errorf("task error %v, want invalid_output", o.err)
		}
		if msgs := r.store.assistantMessages(); len(msgs) != 0 {
			t.Errorf("stored %+v", msgs)
		}
	})

	t.Run("proposals are validated before they are stored", func(t *testing.T) {
		valid := &ProposalOut{Title: "Fix route", Rationale: "It points at a dead trunk.", Actions: []ActionOut{
			{OperationID: "updateOutboundRoute", PathParams: map[string]string{"id": "3"}, Body: []byte(`{"trunkId":2}`)},
		}}
		invalid := &ProposalOut{Title: "Make me admin", Actions: []ActionOut{{OperationID: "updateUserRole", Body: []byte(`{"role":"admin"}`)}}}
		r := newRig(t,
			textResp(Answer{Answer: "Change it.", Citations: []int{}, Proposal: invalid}),
			textResp(Answer{Answer: "Change it.", Citations: []int{}, Proposal: valid}),
		)
		s, o := r.ask(t, "fix the route")
		if o.err != nil {
			t.Fatal(o.err)
		}
		stored := r.props.storedDrafts()
		if len(stored) != 1 || stored[0].Actions[0].OperationID != "updateOutboundRoute" {
			t.Fatalf("stored %+v", stored)
		}
		d := stored[0]
		if d.Source != "assistant" || d.SessionID == nil || *d.SessionID != s.ID || string(d.Actions[0].Before) == "" {
			t.Errorf("draft %+v", d)
		}
		if r.props.ident[0].UserID != 7 {
			t.Errorf("validated as %+v", r.props.ident[0])
		}
		msgs := r.store.assistantMessages()
		if len(msgs) != 1 || msgs[0].ProposalID == nil {
			t.Errorf("answer not linked to its proposal: %+v", msgs)
		}

		// Without a proposal validator nothing is stored: the model is told
		// to answer without one.
		r2 := newRig(t,
			textResp(Answer{Answer: "Change it.", Citations: []int{}, Proposal: valid}),
			textResp(Answer{Answer: "I cannot propose here.", Citations: []int{}}),
		)
		r2.a.cfg.Proposals = nil
		if _, o := r2.ask(t, "fix the route"); o.err != nil {
			t.Fatal(o.err)
		}
		if len(r2.props.storedDrafts()) != 0 {
			t.Error("a proposal was stored with no validator")
		}
	})

	t.Run("an endpoint that rejects tools fails tools_unsupported", func(t *testing.T) {
		r := newRig(t)
		r.model.Err = &aisdk.APICallError{StatusCode: 400, Message: `"tools" is not supported by this model`}
		_, o := r.ask(t, "anything?")
		if ai.CodeOf(o.err) != ai.CodeToolsUnsupported {
			t.Fatalf("task error %v, want tools_unsupported", o.err)
		}
		if !strings.Contains(o.err.Error(), "not supported") {
			t.Errorf("provider message lost: %v", o.err)
		}
		r.model.Err = &aisdk.APICallError{StatusCode: 500, Message: "overloaded"}
		_, o = r.ask(t, "again?")
		if ai.CodeOf(o.err) != ai.CodeProviderError {
			t.Errorf("a 500 became %v", o.err)
		}
	})

	t.Run("a deleted user's task stops at the first read", func(t *testing.T) {
		r := newRig(t,
			toolResp(call("c1", "listExtensions", `{}`)),
			textResp(Answer{Answer: "x", Citations: []int{}}),
		)
		r.api.status = 401
		_, o := r.ask(t, "list extensions")
		if !errors.Is(o.err, errUserGone) {
			t.Errorf("task error %v", o.err)
		}
		if len(r.store.assistantMessages()) != 0 {
			t.Error("stored an answer for a deleted user")
		}
	})
}
