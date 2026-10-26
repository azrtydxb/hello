package main

import (
	"context"
	"time"

	"github.com/azrtydxb/hello/internal/sip"
	"github.com/azrtydxb/hello/internal/store"
)

// timeConditions applies the day/night feature code through the
// control-plane store, so the override is audited, bumps the configuration
// revision and reaches every node as a snapshot.
type timeConditions struct{ st *store.Store }

var _ sip.TimeConditionSettings = timeConditions{}

// Toggle changes the override of the time condition with that toggle number.
func (t timeConditions) Toggle(ctx context.Context, actor, number, mode string, now time.Time) (string, string, error) {
	tc, err := t.st.ApplyTimeConditionToggle(ctx, actor, number, mode, now)
	if err != nil {
		return "", "", err
	}
	return tc.Name, tc.Override, nil
}
