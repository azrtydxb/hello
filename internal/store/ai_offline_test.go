package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"

	"github.com/azrtydxb/hello/internal/ai"
)

// TestAIStatementsOffline builds the AI domain's statements that reach the
// seam through a helper (queryAIMessages, findingAction), where
// TestStatementsBuild cannot see them, with the names their callers bind.
func TestAIStatementsOffline(t *testing.T) {
	db := momo.NewOffline(dialect.Postgres)
	for _, c := range []struct {
		name, sql string
		binds     []string
	}{
		{"aiSessionMessages", aiSessionMessages, []string{"session_id"}},
		{"aiMessageHistory", aiMessageHistory, []string{"session_id", "window"}},
		{"aiFindingAcknowledge", aiFindingAcknowledge, []string{"id", "user_id"}},
		{"aiFindingDismiss", aiFindingDismiss, []string{"id", "user_id", "reason"}},
	} {
		args := make([]any, 0, len(c.binds))
		for _, b := range c.binds {
			args = append(args, momo.Named(b, nil))
		}
		if _, err := momo.Exec(context.Background(), db, c.sql, args...); !errors.Is(err, momo.ErrUnsupported) {
			t.Errorf("%s does not build: %v", c.name, err)
		}
	}
}

// TestPruneAISends checks that every PruneAI step builds with its @cutoff
// and reaches PostgreSQL with one positional argument.
func TestPruneAISends(t *testing.T) {
	d := &fakeDriver{affected: 2}
	s := &Store{db: &dbPool{dbConn: dbConn{momo.New(d)}}}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	out, err := s.PruneAI(context.Background(), now)
	if err != nil {
		t.Fatalf("PruneAI: %v", err)
	}
	if len(out) != 7 || len(d.sent) != 7 {
		t.Fatalf("PruneAI ran %d statements, counted %v; want 7", len(d.sent), out)
	}
	for _, st := range d.sent {
		if len(st.args) != 1 || !strings.Contains(st.sql, "$1") || strings.Contains(st.sql, "@") {
			t.Errorf("sent %q with %v; want one $1 bind", st.sql, st.args)
		}
	}
	want := now.Add(-ai.RetainUsage).Format(time.DateOnly)
	if got := d.sent[len(d.sent)-1].args[0]; got != want {
		t.Errorf("usage cutoff = %v, want %s", got, want)
	}
}
