package detect

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
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
	rows, err := r.DB.QueryContext(ctx, `SELECT trunk_name,
		count(*) FILTER (WHERE start_time >= $1),
		count(*) FILTER (WHERE start_time >= $1 AND answer_time IS NOT NULL),
		count(*) FILTER (WHERE start_time < $1),
		count(*) FILTER (WHERE start_time < $1 AND answer_time IS NOT NULL)
		FROM cdrs WHERE start_time >= $2 AND start_time <= $3 AND trunk_name <> '' AND direction = 'outbound'
		GROUP BY trunk_name`, cut, cut.Add(-asrBaseline), r.Now)
	if err != nil {
		return nil, err
	}
	var all []asrCounts
	for rows.Next() {
		var c asrCounts
		if err := rows.Scan(&c.Trunk, &c.Recent, &c.RecentAns, &c.Base, &c.BaseA); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if dropped(c) {
			all = append(all, c)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	rows, err := r.DB.QueryContext(ctx, `SELECT final_status, count(*) FROM cdrs
		WHERE start_time >= $1 AND start_time <= $3 AND trunk_name = $2 AND direction = 'outbound' AND answer_time IS NULL
		GROUP BY final_status ORDER BY count(*) DESC, final_status`, cut, trunk, r.Now)
	if err != nil {
		return failStatus{}, err
	}
	defer func() { _ = rows.Close() }()
	var top failStatus
	var total int64
	for rows.Next() {
		var f failStatus
		if err := rows.Scan(&f.Status, &f.N); err != nil {
			return failStatus{}, err
		}
		if top.N == 0 {
			top = f
		}
		total += f.N
	}
	if err := rows.Err(); err != nil || total == 0 || top.N*100 < asrStatusShare*total {
		return failStatus{}, err
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
