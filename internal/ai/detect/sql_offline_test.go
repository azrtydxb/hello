package detect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/dialect"

	"github.com/azrtydxb/hello/internal/livestate"
)

// TestStatementsBuild runs every statement of the detectors on momo's
// offline handle, which renders the statement and then refuses to run it.
// It fails if a statement does not build (an @name without its argument, an
// argument no @name uses): those are momo.ErrValidation, while a statement
// that built reaches the handle and gets momo.ErrUnsupported. It needs no
// database, so it runs on every pull request; the DB-backed detector tests
// then prove what the statements do.
func TestStatementsBuild(t *testing.T) {
	ctx := context.Background()
	live := &fakeLive{bindings: []livestate.Binding{{Device: "d1"}}, trunks: map[int64]livestate.TrunkStatus{}}
	env := &Env{DB: momo.NewOffline(dialect.Postgres), Live: live, Cluster: fakeMembers{}, Now: func() time.Time { return t0 }}
	run := func() *Run {
		r := NewRun(env)
		r.attRead = true // regFailures reads no Valkey
		return r
	}
	calls := map[string]func() error{
		"callQuality":      func() error { _, err := callQuality(ctx, run()); return err },
		"ringGroupSmells":  func() error { _, err := ringGroupSmells(ctx, run()); return err },
		"extensionSmells":  func() error { _, err := extensionSmells(ctx, run()); return err },
		"routeSmells":      func() error { _, err := routeSmells(ctx, run()); return err },
		"phoneSmells":      func() error { _, err := phoneSmells(ctx, run()); return err },
		"deviceSmells":     func() error { _, err := deviceSmells(ctx, run()); return err },
		"neverRegistered":  func() error { _, err := neverRegistered(ctx, run(), t0); return err },
		"lastExplained":    func() error { _, _, err := lastExplained(ctx, run()); return err },
		"regFailures":      func() error { _, err := regFailures(ctx, run()); return err },
		"putSample":        func() error { return run().putSample(ctx, kindNode, "sip-1", map[string]bool{"ready": true}) },
		"history":          func() error { _, err := history[trunkLoad](ctx, env, kindTrunkActive, t0); return err },
		"trunkASRDrop":     func() error { _, err := trunkASRDrop(ctx, run()); return err },
		"dominantFailure":  func() error { _, err := dominantFailure(ctx, run(), "carrier", t0); return err },
		"trunkCapacity":    func() error { _, err := trunkCapacity(ctx, run()); return err },
		"trunkDown":        func() error { _, err := trunkDown(ctx, run()); return err },
		"names (no binds)": func() error { _, err := names(ctx, run(), `SELECT name FROM trunks`); return err },
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
