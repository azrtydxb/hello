package ai

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is a missing task, or one the caller may not see.
var ErrNotFound = errors.New("not found")

// Store is the persistence the AI core needs (migration 00009);
// *store.Store implements it against PostgreSQL and aifake.Store in memory.
type Store interface {
	// AddAIUsage adds u to day's row of ai_usage (UTC date), creating it.
	AddAIUsage(ctx context.Context, day time.Time, u Usage) error
	// AIUsage is day's usage summed across replicas.
	AIUsage(ctx context.Context, day time.Time) (Usage, error)

	// CreateAITask inserts t as queued; ErrTaskRunning when t has a session
	// that already has a queued or running task.
	CreateAITask(ctx context.Context, t Task) error
	// StartAITask marks a queued task running.
	StartAITask(ctx context.Context, id string, at time.Time) error
	// FinishAITask ends a task: succeeded with result, or failed with code
	// and message.
	FinishAITask(ctx context.Context, id string, status TaskStatus, code, message string, result json.RawMessage, at time.Time) error
	// AITask returns a task; ErrNotFound when it does not exist.
	AITask(ctx context.Context, id string) (Task, error)
	// AITaskReplicas lists the replicas that have queued or running tasks
	// created before since.
	AITaskReplicas(ctx context.Context, before time.Time) ([]string, error)
	// FailAITasks fails replica's queued and running tasks with code.
	FailAITasks(ctx context.Context, replica, code, message string, at time.Time) (int64, error)

	// TryAILock takes the session-level advisory lock
	// pg_try_advisory_lock(hashtext(key)) on a dedicated connection; ok is
	// false when another holds it. unlock releases it and the connection;
	// a dead replica's lock goes with its connection.
	TryAILock(ctx context.Context, key string) (unlock func(), ok bool, err error)
	// LastAgentRun is agent's most recent run, nil when it never ran.
	LastAgentRun(ctx context.Context, agent string) (*AgentRun, error)
	// AgentRunRequested reports whether a run-now request waits for agent.
	AgentRunRequested(ctx context.Context, agent string) (bool, error)
	// RequestAgentRun records a run-now request (one per agent).
	RequestAgentRun(ctx context.Context, agent string, userID int64, at time.Time) (time.Time, error)
	// TakeAgentRequest deletes agent's run-now request, reporting whether
	// there was one.
	TakeAgentRequest(ctx context.Context, agent string) (bool, error)
	// StartAgentRun inserts a run row, and CloseOrphanRuns fails agent's
	// rows that never finished (their replica died; the caller holds the
	// lock, so none is live).
	StartAgentRun(ctx context.Context, agent, replica string, at time.Time) (int64, error)
	FinishAgentRun(ctx context.Context, id int64, r AgentRun) error
	CloseOrphanRuns(ctx context.Context, agent string, at time.Time) (int64, error)

	// PruneAI deletes what is past its retention (spec S-26) relative to
	// now; the caller holds the prune lock.
	PruneAI(ctx context.Context, now time.Time) (map[string]int64, error)
	// AICounts are the open and acknowledged findings by severity, the
	// health score over open ones, and the open proposals (spec S-21, S-23).
	AICounts(ctx context.Context) (Counts, error)
}

// Task is an ai_tasks row (spec S-15).
type Task struct {
	ID           string
	Kind         string
	RequestedBy  int64
	SessionID    string
	Replica      string
	Status       TaskStatus
	ErrorCode    string
	ErrorMessage string
	Result       json.RawMessage
	CreatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// AgentRun is an ai_agent_runs row (spec S-17). An empty Outcome is a run
// in progress.
type AgentRun struct {
	ID         int64
	Agent      string
	Replica    string
	StartedAt  time.Time
	FinishedAt *time.Time
	Outcome    Outcome
	Tokens     int64
	Detail     map[string]any
	Error      string
}

// Counts summarise findings and proposals for the status page.
type Counts struct {
	Info, Warning, Critical int
	HealthScore             int
	OpenProposals           int
}
