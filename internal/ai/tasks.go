package ai

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// CodeTaskRunning refuses a second task for a session (spec S-8, 409).
const CodeTaskRunning = "task_running"

// ErrTaskRunning is Tasks.Start's error when the session already has a
// queued or running task.
var ErrTaskRunning = &Error{Code: CodeTaskRunning, Message: "a task is already running for this session"}

// Tasks runs interactive work asynchronously (spec S-15): an ai_tasks row,
// a goroutine on this replica, and the status, error code or result the
// console polls.
type Tasks struct{ s *Service }

type taskKey struct{}

// taskIDFrom is the id of the task ctx runs under, for logs.
func taskIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(taskKey{}).(string)
	return id
}

// NewID returns a random (version 4) UUID string.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidID reports whether id is a UUID, so a malformed path id is a 404
// rather than a database error.
func ValidID(id string) bool { return uuidRE.MatchString(id) }

// Start records a queued task for userID (and sessionID, when not empty)
// and runs fn in a goroutine that outlives the request; it returns the
// task id at once. A session with a queued or running task gets
// ErrTaskRunning; with AI off, ai_disabled.
func (t *Tasks) Start(ctx context.Context, kind string, userID int64, sessionID string, fn TaskFunc) (string, error) {
	s := t.s
	if !s.enabled {
		return "", &Error{Code: CodeDisabled, Message: "the AI agent is off"}
	}
	task := Task{ID: NewID(), Kind: kind, RequestedBy: userID, SessionID: sessionID, Replica: s.replica, Status: TaskQueued, CreatedAt: s.now()}
	if err := s.st.CreateAITask(ctx, task); err != nil {
		return "", err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runTask(task.ID, fn)
	}()
	return task.ID, nil
}

// Get returns a task to its requester or an admin; anyone else gets
// ErrNotFound, as for a task that does not exist (spec S-15).
func (t *Tasks) Get(ctx context.Context, id string, userID int64, admin bool) (Task, error) {
	if !ValidID(id) {
		return Task{}, ErrNotFound
	}
	task, err := t.s.st.AITask(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if task.RequestedBy != userID && !admin {
		return Task{}, ErrNotFound
	}
	return task, nil
}

// storeCtx bounds a task's own row writes, which must land even when the
// task's context has ended.
func storeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (s *Service) runTask(id string, fn TaskFunc) {
	ctx := context.WithValue(s.base, taskKey{}, id)
	wctx, cancel := storeCtx()
	err := s.st.StartAITask(wctx, id, s.now())
	cancel()
	if err != nil {
		s.log.Error("ai: start task", "task_id", id, "error", err)
	}
	result, err := safeRun(ctx, id, fn)
	status, code, msg := TaskSucceeded, "", ""
	var raw json.RawMessage
	if err != nil {
		status, code, msg = TaskFailed, CodeOf(err), "the task failed"
		var e *Error
		if errors.As(err, &e) && e.Message != "" {
			msg = e.Message
		}
		if s.base.Err() != nil && code == "" {
			code, msg = CodeInstanceStopped, "the instance stopped"
		}
		if code == "" {
			code = CodeProviderError
		}
		s.log.Warn("ai: task failed", "task_id", id, "error_code", code)
	} else if result != nil {
		if raw, err = json.Marshal(result); err != nil {
			status, code, msg, raw = TaskFailed, CodeInvalidOutput, "the result does not encode", nil
		}
	}
	wctx, cancel = storeCtx()
	defer cancel()
	if err := s.st.FinishAITask(wctx, id, status, code, msg, raw, s.now()); err != nil {
		s.log.Error("ai: finish task", "task_id", id, "error", err)
	}
}

// safeRun runs fn, turning a panic into an error.
func safeRun(ctx context.Context, id string, fn TaskFunc) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("task panicked: %v", r)
		}
	}()
	return fn(ctx, id)
}
