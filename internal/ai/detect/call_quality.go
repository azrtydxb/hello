package detect

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// call_quality (spec S-19, S-5.1): per trunk (trunk_name) and per node
// (sip_node), the last qualityCDRs CDRs with non-null quality that ended in
// the last qualityWindow; when qualityBad of them have loss of at least
// qualityLossPercent or jitter of at least qualityJitterMs, a warning. Fewer
// than qualityCDRs such CDRs raise nothing.
const (
	qualityWindow      = time.Hour
	qualityCDRs        = 5
	qualityBad         = 3
	qualityLossPercent = 1
	qualityJitterMs    = 100
)

type qualityCDR struct {
	ID      int64
	Packets int64
	Lost    int64
	Jitter  float64
}

// lossPercent is lost / (packets + lost) x 100, as the console shows it.
func (q qualityCDR) lossPercent() float64 { return pct(q.Lost, q.Packets+q.Lost) }

func (q qualityCDR) bad() bool {
	total := q.Packets + q.Lost
	return (total > 0 && q.Lost*100 >= total*qualityLossPercent) || q.Jitter >= qualityJitterMs
}

// The last qualityCDRs CDRs with quality per trunk or node, newest first.
const (
	qualityRankHead = `SELECT k, id, rtp_packets, rtp_lost, rtp_jitter_ms FROM (SELECT `
	qualityRankTail = ` AS rn FROM cdrs WHERE end_time >= $1 AND end_time <= $2 AND rtp_packets IS NOT NULL`
	qualityTrunkSQL = qualityRankHead + `trunk_name AS k, id, rtp_packets, rtp_lost, rtp_jitter_ms,
		row_number() OVER (PARTITION BY trunk_name ORDER BY end_time DESC, id DESC)` + qualityRankTail +
		` AND trunk_name <> '') t WHERE rn <= $3 ORDER BY k, rn`
	qualityNodeSQL = qualityRankHead + `sip_node AS k, id, rtp_packets, rtp_lost, rtp_jitter_ms,
		row_number() OVER (PARTITION BY sip_node ORDER BY end_time DESC, id DESC)` + qualityRankTail +
		`) t WHERE rn <= $3 ORDER BY k, rn`
)

func callQuality(ctx context.Context, r *Run) ([]Candidate, error) {
	var out []Candidate
	for _, dim := range []struct{ kind, query string }{{"trunk", qualityTrunkSQL}, {"node", qualityNodeSQL}} {
		rows, err := r.DB.QueryContext(ctx, dim.query, r.Now.Add(-qualityWindow), r.Now, qualityCDRs)
		if err != nil {
			return nil, err
		}
		by := map[string][]qualityCDR{}
		for rows.Next() {
			var k string
			var q qualityCDR
			var lost *int64
			var jitter *float64
			if err := rows.Scan(&k, &q.ID, &q.Packets, &lost, &jitter); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if lost != nil {
				q.Lost = *lost
			}
			if jitter != nil {
				q.Jitter = *jitter
			}
			by[k] = append(by[k], q)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for k, cdrs := range by {
			if c, ok := evalQuality(dim.kind, k, cdrs); ok {
				out = append(out, c)
			}
		}
	}
	slices.SortFunc(out, func(a, b Candidate) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// evalQuality is the detector over one trunk's or node's newest CDRs.
func evalQuality(kind, name string, cdrs []qualityCDR) (Candidate, bool) {
	if len(cdrs) < qualityCDRs {
		return Candidate{}, false
	}
	bad := 0
	rows := []map[string]any{}
	for _, q := range cdrs {
		if q.bad() {
			bad++
		}
		rows = append(rows, map[string]any{"cdrId": q.ID, "lossPercent": q.lossPercent(), "jitterMs": q.Jitter})
	}
	if bad < qualityBad {
		return Candidate{}, false
	}
	return candidate("call_quality", kind+":"+name, Warning,
		fmt.Sprintf("Poor call quality on %s %s: %d of the last %d calls", kind, name, bad, len(cdrs)),
		map[string]any{kind: name, "badCalls": bad, "checkedCalls": len(cdrs), "lossThresholdPercent": qualityLossPercent,
			"jitterThresholdMs": qualityJitterMs, "cdrs": rows}), true
}
