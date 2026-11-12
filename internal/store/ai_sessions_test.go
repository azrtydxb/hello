package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/assistant/chat"
	"github.com/azrtydxb/hello/internal/auth"
)

// TestPostAIMessageCreatesItsTask fails if posting a message hangs because
// the task it starts waits for the session lock the post holds (the
// assistant answered nothing and the request timed out), or if a second post
// while the first task is queued is not refused as task_running.
func TestPostAIMessageCreatesItsTask(t *testing.T) {
	s := scratchStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	uid, err := s.CreateUser(ctx, "test", "alice", "x", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.CreateAISession(ctx, uid, "")
	if err != nil {
		t.Fatal(err)
	}
	start := func(ctx context.Context, _ int64, _ []chat.Message) (string, error) {
		task := ai.Task{ID: ai.NewID(), Kind: "message", RequestedBy: uid, SessionID: sess.ID, Replica: "r", Status: ai.TaskQueued, CreatedAt: time.Now()}
		return task.ID, s.CreateAITask(ctx, task)
	}
	if _, id, err := s.PostAIMessage(ctx, sess.ID, "hello", nil, "hello", 10, start); err != nil || id == "" {
		t.Fatalf("PostAIMessage = %q, %v", id, err)
	}
	if _, _, err := s.PostAIMessage(ctx, sess.ID, "again", nil, "", 10, start); !errors.Is(err, chat.ErrTaskRunning) && !errors.Is(err, ai.ErrTaskRunning) {
		t.Fatalf("second post while a task is queued = %v, want task_running", err)
	}
}

// TestAIMessageContext (spec flow-canvas S-19) fails if a question's canvas
// context is not stored with the message, does not reach the task's
// history, or is invented for a message posted without one.
func TestAIMessageContext(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	uid, err := s.CreateUser(ctx, "test", "bob", "x", auth.RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.CreateAISession(ctx, uid, "")
	if err != nil {
		t.Fatal(err)
	}
	canvas := int64(4)
	mc := &chat.Context{CanvasID: &canvas, Nodes: []string{"ivr:1"}, Trace: &chat.TraceContext{From: "trunk:2", Number: "0201", Digits: "1"}}
	var seen []chat.Message
	start := func(ctx context.Context, _ int64, h []chat.Message) (string, error) {
		seen = h
		task := ai.Task{ID: ai.NewID(), Kind: "message", RequestedBy: uid, SessionID: sess.ID, Replica: "r", Status: ai.TaskQueued, CreatedAt: time.Now()}
		return task.ID, s.CreateAITask(ctx, task)
	}
	if _, _, err := s.PostAIMessage(ctx, sess.ID, "why?", mc, "why?", 10, start); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Context == nil || *seen[0].Context.CanvasID != 4 || seen[0].Context.Trace.Digits != "1" {
		t.Fatalf("history = %+v, want the message with its context", seen)
	}
	msgs, err := s.AIMessages(ctx, sess.ID)
	if err != nil || len(msgs) != 1 || msgs[0].Context == nil || msgs[0].Context.Nodes[0] != "ivr:1" {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	if _, err := s.AddAIMessage(ctx, chat.Message{SessionID: sess.ID, Role: chat.RoleAssistant, Content: "because"}); err != nil {
		t.Fatal(err)
	}
	msgs, _ = s.AIMessages(ctx, sess.ID)
	if len(msgs) != 2 || msgs[1].Context != nil {
		t.Fatalf("an answer has a context: %+v", msgs)
	}
}
