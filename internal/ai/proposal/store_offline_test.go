package proposal

import (
	"context"
	"errors"
	"testing"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"
)

// TestStatementsBuild runs every statement of DBStore on momo's offline
// handle, which renders the statement and then refuses to run it. It fails
// if a statement does not build (an @name without its argument, an argument
// no @name uses): those are momo.ErrValidation, while a statement that built
// reaches the handle and gets momo.ErrUnsupported. It needs no database, so
// it runs on every pull request. Upsert, Dismiss and Apply stop at BEGIN
// offline, so their statements are called one by one; the DB-backed tests
// (env_test.go) prove what they do together, and the advisory lock is
// momo's advisory.XactLockString.
func TestStatementsBuild(t *testing.T) {
	ctx := context.Background()
	q := momo.NewOffline(dialect.Postgres)
	s := NewStore(q, nil)
	const id = "00000000-0000-0000-0000-000000000001"
	sess, by := "00000000-0000-0000-0000-000000000002", int64(1)
	d := Draft{Source: SourceAssistant, Title: "t", Rationale: "r", SessionID: &sess, CreatedBy: &by,
		Actions: []Action{{OperationID: "updateTrunk", PathParams: map[string]string{"id": "1"}}}}
	calls := map[string]func() error{
		"Get":           func() error { _, err := s.Get(ctx, id); return err },
		"List":          func() error { _, err := s.List(ctx, StatusOpen, "assistant", 10); return err },
		"Counts":        func() error { _, err := s.Counts(ctx); return err },
		"Upsert":        func() error { _, err := s.Upsert(ctx, d); return err },
		"Dismiss":       func() error { return s.Dismiss(ctx, id, 1, ReasonOther, "x") },
		"revision":      func() error { _, err := revision(ctx, q); return err },
		"suppressed":    func() error { _, err := suppressed(ctx, q, "fp"); return err },
		"refresh":       func() error { _, err := refresh(ctx, q, "fp", d, []byte(`[]`), 1); return err },
		"insert":        func() error { _, err := insert(ctx, q, "fp", d, []byte(`[]`), 1); return err },
		"supersede":     func() error { return supersede(ctx, q, id, d) },
		"lock":          func() error { _, err := lock(ctx, q, id); return err },
		"audit":         func() error { return audit(ctx, q, 1, "dismiss", id, "ai:s") },
		"markDismissed": func() error { return markDismissed(ctx, q, id, 1, ReasonOther, "x") },
		"recordOutcome": func() error {
			return recordOutcome(ctx, q, id, StatusApplied, []byte(`{}`), 1, "ai:s", "00000000-0000-0000-0000-000000000003")
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if errors.Is(err, momo.ErrValidation) || !errors.Is(err, momo.ErrUnsupported) {
				t.Fatalf("err = %v; want the offline handle's momo.ErrUnsupported (the statement must build)", err)
			}
		})
	}
}
