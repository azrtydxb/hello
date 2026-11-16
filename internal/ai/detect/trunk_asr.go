package detect

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/azrtydxb/momo"
)

// trunk_asr_drop (spec S-19): per trunk, at least asrMinAttempts outbound
// attempts in the last hour and an answer-seizure ratio below half of the
// trunk's ratio over the 7 days before it. One SIP final status that
// accounts for at least asrStatusShare of the failures is named.
const (
	asrWindow      = time.Hour
	asrBaseline    = 7 * 24 * time.Hour
	asrMinAttempts = 20
	asrStatusShare = 30 // percent
)

type asrCounts struct {
	Trunk                          string
	Recent, RecentAns, Base, BaseA int64
}

func trunkASRDrop(ctx context.Context, r *Run) ([]Candidate, error) {
	cut := r.Now.Add(-asrWindow)
	rows, err := momo.Query[momo.Row5[string, int64, int64, int64, int64]](ctx, r.DB, `SELECT trunk_name,
		count(*) FILTER (WHERE start_time >= @cut),
		count(*) FILTER (WHERE start_time >= @cut AND answer_time IS NOT NULL),
		count(*) FILTER (WHERE start_time < @cut),
		count(*) FILTER (WHERE start_time < @cut AND answer_time IS NOT NULL)
		FROM cdrs WHERE start_time >= @from AND start_time <= @now AND trunk_name <> '' AND direction = 'outbound'
		GROUP BY trunk_name`, momo.Named("cut", cut), momo.Named("from", cut.Add(-asrBaseline)), momo.Named("now", r.Now))
	if err != nil {
		return nil, err
	}
	var all []asrCounts
	for _, row := range rows {
		c := asrCounts{Trunk: row.C1, Recent: row.C2, RecentAns: row.C3, Base: row.C4, BaseA: row.C5}
		if dropped(c) {
			all = append(all, c)
		}
	}
	var out []Candidate
	for _, c := range all {
		status, err := dominantFailure(ctx, r, c.Trunk, cut)
		if err != nil {
			return nil, err
		}
		out = append(out, asrCandidate(c, status))
	}
	slices.SortFunc(out, func(a, b Candidate) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// dropped reports whether the recent ratio is below half the baseline's:
// RecentAns/Recent < (BaseA/Base)/2, in integers.
func dropped(c asrCounts) bool {
	return c.Recent >= asrMinAttempts && c.Base > 0 && c.BaseA > 0 && 2*c.RecentAns*c.Base < c.BaseA*c.Recent
}

type failStatus struct {
	Status int
	N      int64
}

// dominantFailure returns the SIP final status with at least asrStatusShare
// percent of the trunk's failed attempts since cut, or the zero value.
func dominantFailure(ctx context.Context, r *Run, trunk string, cut time.Time) (failStatus, error) {
	rows, err := momo.Query[momo.Row2[int, int64]](ctx, r.DB, `SELECT final_status, count(*) FROM cdrs
		WHERE start_time >= @cut AND start_time <= @now AND trunk_name = @trunk AND direction = 'outbound' AND answer_time IS NULL
		GROUP BY final_status ORDER BY count(*) DESC, final_status`, momo.Named("cut", cut), momo.Named("trunk", trunk), momo.Named("now", r.Now))
	if err != nil {
		return failStatus{}, err
	}
	var top failStatus
	var total int64
	for _, row := range rows {
		f := failStatus{Status: row.C1, N: row.C2}
		if top.N == 0 {
			top = f
		}
		total += f.N
	}
	if total == 0 || top.N*100 < asrStatusShare*total {
		return failStatus{}, nil
	}
	return top, nil
}

func asrCandidate(c asrCounts, top failStatus) Candidate {
	ev := map[string]any{
		"trunk": c.Trunk, "attempts": c.Recent, "answered": c.RecentAns,
		"asrPercent":         pct(c.RecentAns, c.Recent),
		"baselineAttempts":   c.Base,
		"baselineAsrPercent": pct(c.BaseA, c.Base),
		"windowMinutes":      int(asrWindow.Minutes()), "baselineDays": int(asrBaseline.Hours() / 24),
	}
	if top.N > 0 {
		ev["dominantFailureStatus"] = top.Status
		ev["dominantFailureCount"] = top.N
	}
	return candidate("trunk_asr_drop", c.Trunk, Warning,
		fmt.Sprintf("Trunk %s answer-seizure ratio fell to %.0f%% (baseline %.0f%%)", c.Trunk, pct(c.RecentAns, c.Recent), pct(c.BaseA, c.Base)), ev)
}

func pct(n, d int64) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) * 100 / float64(d)
}
