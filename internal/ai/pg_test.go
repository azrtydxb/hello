package ai_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// scratch returns a migrated throwaway database and its store.
func scratch(t *testing.T) (*sql.DB, *store.Store) {
	t.Helper()
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_ai_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	scfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*scfg)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db, store.New(db)
}

// runsOf reads every ai_agent_runs row.
func runsOf(t *testing.T, st *store.Store, agents ...string) func() []ai.AgentRun {
	return func() []ai.AgentRun {
		var out []ai.AgentRun
		for _, a := range agents {
			r, err := st.LastAgentRun(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			if r != nil {
				out = append(out, *r)
			}
		}
		return out
	}
}

// TestSchedulerPostgres is TestScheduler's story on PostgreSQL, with the
// advisory locks on dedicated connections, plus the closing of a dead
// replica's unfinished run.
func TestSchedulerPostgres(t *testing.T) {
	db, st := scratch(t)
	schedulerCase(t, st, runsOf(t, st, "count", "failing", "panicky", "spender"))

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO ai_agent_runs (agent, replica, started_at) VALUES ('orphan', 'dead', now() - interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	s, err := ai.NewWith(aifake.Config(), st, nil, nil, nil, ai.Options{Replica: "r", Model: newBlocking()})
	if err != nil {
		t.Fatal(err)
	}
	s.Scheduler.Register(&countAgent{name: "orphan", interval: time.Minute})
	s.Scheduler.Tick(ctx)
	var failed int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ai_agent_runs WHERE agent = 'orphan' AND outcome = 'failed' AND error = 'instance_stopped'`).Scan(&failed); err != nil || failed != 1 {
		t.Errorf("orphan run closed %d (%v)", failed, err)
	}
}

// TestTasksPostgres is the store half of TestTasksAndLimits: one task per
// session, finishing, and failing a replica's tasks.
func TestTasksPostgres(t *testing.T) {
	db, st := scratch(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, "test", "op", "x", auth.RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	sid := ai.NewID()
	if _, err := db.ExecContext(ctx, `INSERT INTO ai_sessions (id, owner) VALUES ($1, $2)`, sid, uid); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	t1 := ai.Task{ID: ai.NewID(), Kind: "message", RequestedBy: uid, SessionID: sid, Replica: "a", CreatedAt: now.Add(-time.Minute)}
	if err := st.CreateAITask(ctx, t1); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAITask(ctx, ai.Task{ID: ai.NewID(), Kind: "message", RequestedBy: uid, SessionID: sid, Replica: "a", CreatedAt: now}); !errors.Is(err, ai.ErrTaskRunning) {
		t.Errorf("second task for the session: %v", err)
	}
	if err := st.StartAITask(ctx, t1.ID, now); err != nil {
		t.Fatal(err)
	}
	reps, err := st.AITaskReplicas(ctx, now.Add(-30*time.Second))
	if err != nil || len(reps) != 1 || reps[0] != "a" {
		t.Fatalf("replicas = %v, %v", reps, err)
	}
	if n, err := st.FailAITasks(ctx, "a", ai.CodeInstanceStopped, "stopped", now); err != nil || n != 1 {
		t.Fatalf("FailAITasks = %d, %v", n, err)
	}
	// The late finish of a task already failed does not resurrect it.
	if err := st.FinishAITask(ctx, t1.ID, ai.TaskSucceeded, "", "", []byte(`{"a":1}`), now); err != nil {
		t.Fatal(err)
	}
	got, err := st.AITask(ctx, t1.ID)
	if err != nil || got.Status != ai.TaskFailed || got.ErrorCode != ai.CodeInstanceStopped || got.SessionID != sid || got.StartedAt == nil {
		t.Fatalf("task = %+v, %v", got, err)
	}
	if _, err := st.AITask(ctx, ai.NewID()); !errors.Is(err, ai.ErrNotFound) {
		t.Errorf("missing task: %v", err)
	}
	d := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	for range 2 {
		if err := st.AddAIUsage(ctx, d, ai.Usage{InputTokens: 1, OutputTokens: 2, ReasoningTokens: 3, Calls: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if u, err := st.AIUsage(ctx, d); err != nil || u != (ai.Usage{InputTokens: 2, OutputTokens: 4, ReasoningTokens: 6, Calls: 2}) {
		t.Errorf("usage = %+v, %v", u, err)
	}
}

// TestPrune (spec S-26) fails if any table keeps rows past its retention,
// prunes one inside it, or two replicas prune at once.
func TestPrune(t *testing.T) {
	db, st := scratch(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, "test", "op", "x", auth.RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Each table gets one row just past its retention (old) and one just
	// inside it (new), tagged so the survivors can be named.
	const h = time.Hour
	ins := func(age time.Duration, table string) {
		at := time.Now().Add(-age)
		id := ai.NewID()
		switch table {
		case "ai_tasks":
			exec(`INSERT INTO ai_tasks (id, kind, requested_by, replica, status, created_at) VALUES ($1, 'k', $2, 'r', 'succeeded', $3)`, id, uid, at)
		case "ai_agent_runs":
			exec(`INSERT INTO ai_agent_runs (agent, replica, started_at, outcome) VALUES ($1, 'r', $2, 'ok')`, id, at)
		case "ai_samples":
			exec(`INSERT INTO ai_samples (kind, subject, at, value) VALUES ('k', $1, $2, '{}')`, id, at)
		case "ai_findings":
			exec(`INSERT INTO ai_findings (id, candidate_id, type, subject, severity, status, title, resolved_at, last_seen)
				VALUES ($1, $3, 't', 's', 'info', 'resolved', 't', $2, $2)`, id, at, id)
		case "ai_proposals":
			exec(`INSERT INTO ai_proposals (id, source, fingerprint, title, actions, config_revision, status, updated_at)
				VALUES ($1, 'assistant', $3, 't', '[]', 1, 'applied', $2)`, id, at, id)
		case "ai_sessions":
			exec(`INSERT INTO ai_sessions (id, owner, last_active_at) VALUES ($1, $2, $3)`, id, uid, at)
			exec(`INSERT INTO ai_messages (session_id, role, content) VALUES ($1, 'user', 'hi')`, id)
		case "ai_usage":
			exec(`INSERT INTO ai_usage (day) VALUES ($1::date)`, at.UTC().Format(time.DateOnly))
		}
	}
	retention := map[string]time.Duration{
		"ai_tasks": ai.RetainTasks, "ai_agent_runs": ai.RetainRuns, "ai_samples": ai.RetainSamples,
		"ai_findings": ai.RetainFindings, "ai_proposals": ai.RetainProposals, "ai_sessions": ai.RetainSessions,
		"ai_usage": ai.RetainUsage,
	}
	for table, keep := range retention {
		margin := h
		if table == "ai_usage" {
			margin = 48 * h // whole days
		}
		ins(keep+margin, table)
		ins(keep-margin, table)
	}
	// An open finding and an open proposal are never pruned, however old.
	exec(`INSERT INTO ai_findings (id, candidate_id, type, subject, severity, title, last_seen) VALUES ($1, 'c', 't', 's', 'info', 't', now() - interval '400 days')`, ai.NewID())
	exec(`INSERT INTO ai_proposals (id, source, fingerprint, title, actions, config_revision, updated_at) VALUES ($1, 'assistant', 'f', 't', '[]', 1, now() - interval '400 days')`, ai.NewID())

	s, err := ai.NewWith(aifake.Config(), st, nil, nil, nil, ai.Options{Replica: "r", Model: newBlocking()})
	if err != nil {
		t.Fatal(err)
	}
	unlock, ok, err := st.TryAILock(ctx, "hello:ai:prune")
	if err != nil || !ok {
		t.Fatal("take the prune lock", err)
	}
	if ran, err := s.Prune(ctx); ran || err != nil {
		t.Fatalf("pruned while another replica holds the lock (%v)", err)
	}
	unlock()
	if ran, err := s.Prune(ctx); !ran || err != nil {
		t.Fatalf("Prune = %v, %v", ran, err)
	}
	want := map[string]int{"ai_tasks": 1, "ai_agent_runs": 1, "ai_samples": 1, "ai_findings": 2, "ai_proposals": 2, "ai_sessions": 1, "ai_usage": 1, "ai_messages": 1}
	for table, n := range want {
		var got int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != n {
			t.Errorf("%s keeps %d rows, want %d", table, got, n)
		}
	}
}
