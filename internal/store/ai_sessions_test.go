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
	if _, id, err := s.PostAIMessage(ctx, sess.ID, "hello", "hello", 10, start); err != nil || id == "" {
		t.Fatalf("PostAIMessage = %q, %v", id, err)
	}
	if _, _, err := s.PostAIMessage(ctx, sess.ID, "again", "", 10, start); !errors.Is(err, chat.ErrTaskRunning) && !errors.Is(err, ai.ErrTaskRunning) {
		t.Fatalf("second post while a task is queued = %v, want task_running", err)
	}
}
