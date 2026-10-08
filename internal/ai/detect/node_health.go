package detect

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
)

// node_health (spec S-19).
const (
	nodeNotReadyFor = 2 * time.Minute // not READY this long: warning (critical on a dependency)
	nodeLagFor      = 5 * time.Minute // config revision behind this long: warning
	nodeFlapWindow  = time.Hour
	nodeFlapMin     = 3 // restarts or tombstones in the window
)

// nodeSample is what one run saw of one cluster member.
type nodeSample struct {
	Ready     bool      `json:"ready"`
	Offline   bool      `json:"offline"` // listed from a tombstone
	Reason    string    `json:"reason,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	Lag       bool      `json:"lag"`
}

// dependencyReason reports a not-ready reason that names PostgreSQL or Valkey.
func dependencyReason(reason string) bool {
	l := strings.ToLower(reason)
	return strings.Contains(l, "postgres") || strings.Contains(l, "valkey")
}

func nodeHealth(ctx context.Context, r *Run) ([]Candidate, error) {
	if r.Cluster == nil {
		return nil, ErrValkeyUnavailable
	}
	members, err := r.Cluster.Members(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrValkeyUnavailable, err)
	}
	rev, err := r.Store.ConfigRevision(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		s := nodeSample{
			Ready: m.State == cluster.Ready, Offline: m.State == cluster.Offline, Reason: m.Reason, StartedAt: m.StartedAt.UTC(),
			Lag: m.State != cluster.Offline && m.ConfigRevision > 0 && m.ConfigRevision < rev,
		}
		if err := r.putSample(ctx, kindNode, m.ID, s); err != nil {
			return nil, err
		}
	}
	hist, err := history[nodeSample](ctx, r.Env, kindNode, r.Now.Add(-nodeFlapWindow))
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, m := range members {
		out = append(out, evalNode(m.ID, string(m.Kind), hist[m.ID], r.Now)...)
	}
	slices.SortFunc(out, func(a, b Candidate) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// evalNode is the detector over one member's samples (oldest first).
func evalNode(id, kind string, ss []Sample[nodeSample], now time.Time) []Candidate {
	if len(ss) == 0 {
		return nil
	}
	var out []Candidate
	cur := ss[len(ss)-1].V
	base := func() map[string]any { return map[string]any{"node": id, "kind": kind} }
	if from, ok := since(ss, func(s nodeSample) bool { return !s.Ready }); ok && now.Sub(from) >= nodeNotReadyFor {
		sev := Warning
		if dependencyReason(cur.Reason) {
			sev = Critical
		}
		ev := base()
		ev["notReadySince"], ev["reason"] = from, cur.Reason
		out = append(out, candidate("node_health", id+":not_ready", sev, fmt.Sprintf("Node %s is not ready (%s)", id, cur.Reason), ev))
	}
	if from, ok := since(ss, func(s nodeSample) bool { return s.Lag }); ok && now.Sub(from) >= nodeLagFor {
		ev := base()
		ev["lagSince"] = from
		out = append(out, candidate("node_health", id+":config_lag", Warning, fmt.Sprintf("Node %s is behind on the configuration revision", id), ev))
	}
	starts := map[time.Time]bool{}
	var tombs int
	for i, s := range ss {
		starts[s.V.StartedAt] = true
		if s.V.Offline && (i == 0 || !ss[i-1].V.Offline) {
			tombs++
		}
	}
	if n := max(len(starts)-1, tombs); n >= nodeFlapMin {
		ev := base()
		ev["restarts"], ev["tombstones"], ev["windowMinutes"] = len(starts)-1, tombs, int(nodeFlapWindow.Minutes())
		out = append(out, candidate("node_health", id+":flapping", Warning, fmt.Sprintf("Node %s restarted %d times in an hour", id, n), ev))
	}
	return out
}
