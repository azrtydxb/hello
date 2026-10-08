package detect

import (
	"context"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
)

// trunk_down (spec S-19): an enabled trunk whose live state is down or
// unregistered for at least trunkDownFor.
const trunkDownFor = 2 * time.Minute

type trunkState struct {
	Down   bool   `json:"down"`
	Reason string `json:"reason,omitempty"`
}

// trunkIsDown reads a trunk's live state: a registration trunk is down
// unless registered, an IP trunk when every destination's last check
// failed. A trunk with no health data yet is not known to be down.
func trunkIsDown(mode string, st livestate.TrunkStatus) trunkState {
	if mode == "registration" {
		switch {
		case st.Registration == nil:
			return trunkState{Down: true, Reason: "unregistered"}
		case st.Registration.State != "registered":
			return trunkState{Down: true, Reason: st.Registration.State}
		}
		return trunkState{}
	}
	if len(st.Destinations) == 0 {
		return trunkState{}
	}
	for _, d := range st.Destinations {
		if d.Up {
			return trunkState{}
		}
	}
	return trunkState{Down: true, Reason: "all destinations down"}
}

func trunkDown(ctx context.Context, r *Run) ([]Candidate, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, name, mode FROM trunks WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	type tr struct {
		id         int64
		name, mode string
	}
	var trunks []tr
	for rows.Next() {
		var t tr
		if err := rows.Scan(&t.id, &t.name, &t.mode); err != nil {
			_ = rows.Close()
			return nil, err
		}
		trunks = append(trunks, t)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range trunks {
		st, err := r.Live.TrunkStatus(ctx, t.id)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrValkeyUnavailable, err)
		}
		if err := r.putSample(ctx, kindTrunkState, t.name, trunkIsDown(t.mode, st)); err != nil {
			return nil, err
		}
	}
	hist, err := history[trunkState](ctx, r.Env, kindTrunkState, r.Now.Add(-trunkDownFor-time.Hour))
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, t := range trunks {
		if c, ok := evalTrunkDown(t.name, hist[t.name], r.Now); ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// evalTrunkDown is the detector over a trunk's samples (oldest first).
func evalTrunkDown(name string, ss []Sample[trunkState], now time.Time) (Candidate, bool) {
	from, ok := since(ss, func(s trunkState) bool { return s.Down })
	if !ok || now.Sub(from) < trunkDownFor {
		return Candidate{}, false
	}
	reason := ss[len(ss)-1].V.Reason
	return candidate("trunk_down", name, Critical, fmt.Sprintf("Trunk %s is down (%s)", name, reason),
		map[string]any{"trunk": name, "state": reason, "downSince": from, "downSeconds": int(now.Sub(from).Seconds())}), true
}
