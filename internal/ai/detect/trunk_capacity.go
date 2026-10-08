package detect

import (
	"context"
	"fmt"
	"time"
)

// trunk_capacity (spec S-19): a trunk with max_calls > 0 whose active calls
// were at least capacityPercent of it in at least capacityHits of its last
// capacitySamples samples is a warning; critical when it is full and a
// sample in that window was over the limit (the slot counter was
// overcommitted, which hello-sip counts in hello_trunk_slot_overcommit_total
// and keeps in the same Valkey set the active count is read from).
const (
	capacityPercent = 80
	capacityHits    = 3
	capacitySamples = 5
)

type trunkLoad struct {
	Active int `json:"active"`
	Max    int `json:"max"`
}

func trunkCapacity(ctx context.Context, r *Run) ([]Candidate, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, name, max_calls FROM trunks WHERE enabled AND max_calls > 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	type tr struct {
		id       int64
		name     string
		maxCalls int
	}
	var trunks []tr
	for rows.Next() {
		var t tr
		if err := rows.Scan(&t.id, &t.name, &t.maxCalls); err != nil {
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
		if err := r.putSample(ctx, kindTrunkActive, t.name, trunkLoad{Active: st.ActiveCalls, Max: t.maxCalls}); err != nil {
			return nil, err
		}
	}
	hist, err := history[trunkLoad](ctx, r.Env, kindTrunkActive, r.Now.Add(-time.Hour))
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, t := range trunks {
		if c, ok := evalCapacity(t.name, hist[t.name]); ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// evalCapacity is the detector over a trunk's samples (oldest first).
func evalCapacity(name string, ss []Sample[trunkLoad]) (Candidate, bool) {
	ss = last(ss, capacitySamples)
	hits, over := 0, false
	for _, s := range ss {
		if s.V.Max > 0 && s.V.Active*100 >= s.V.Max*capacityPercent {
			hits++
		}
		over = over || s.V.Active > s.V.Max
	}
	if hits < capacityHits {
		return Candidate{}, false
	}
	cur := ss[len(ss)-1].V
	sev := Warning
	if over && cur.Active >= cur.Max {
		sev = Critical
	}
	rows := []map[string]any{}
	for _, s := range ss {
		rows = append(rows, map[string]any{"at": s.At, "active": s.V.Active})
	}
	return candidate("trunk_capacity", name, sev, fmt.Sprintf("Trunk %s is at %d of %d calls", name, cur.Active, cur.Max),
		map[string]any{"trunk": name, "maxCalls": cur.Max, "activeCalls": cur.Active, "samplesAtOrAbove80Percent": hits,
			"overcommitted": over, "samples": rows}), true
}
