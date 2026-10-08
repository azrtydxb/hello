package detect

// Samples (spec S-20): history the live state does not keep, one ai_samples
// row per subject per run. Detectors write the current value first, then
// read the trailing window, so a condition "for 2 minutes" means every
// sample of the last 2 minutes agrees.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Sample kinds.
const (
	kindTrunkState  = "trunk_state"  // {down, reason} per trunk name
	kindTrunkActive = "trunk_active" // {active, max} per trunk name
	kindNode        = "node"         // nodeSample per member id
	kindDeviceDay   = "device_day"   // {registered} per device, one row per UTC day
	kindExplain     = "explain"      // explainState, subject "aiops"
)

// Sample is one stored value at a time.
type Sample[T any] struct {
	At time.Time
	V  T
}

// putSample stores v for kind and subject at the run's instant; a second
// write at the same instant is ignored.
func (r *Run) putSample(ctx context.Context, kind, subject string, v any) error {
	return putSampleAt(ctx, r.Env, kind, subject, r.Now, v)
}

func putSampleAt(ctx context.Context, e *Env, kind, subject string, at time.Time, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = e.DB.ExecContext(ctx, `INSERT INTO ai_samples (kind, subject, at, value) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, kind, subject, at, b)
	if err != nil {
		return fmt.Errorf("write %s sample: %w", kind, err)
	}
	return nil
}

// history returns every subject's samples of kind since, oldest first.
func history[T any](ctx context.Context, e *Env, kind string, since time.Time) (map[string][]Sample[T], error) {
	rows, err := e.DB.QueryContext(ctx, `SELECT subject, at, value FROM ai_samples
		WHERE kind = $1 AND at >= $2 ORDER BY subject, at`, kind, since)
	if err != nil {
		return nil, fmt.Errorf("read %s samples: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]Sample[T]{}
	for rows.Next() {
		var subject string
		var s Sample[T]
		var raw []byte
		if err := rows.Scan(&subject, &s.At, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &s.V); err != nil {
			continue // a sample of another shape is not evidence
		}
		out[subject] = append(out[subject], s)
	}
	return out, rows.Err()
}

// since reports when the trailing run of samples satisfying pred began; ok
// is false when the latest sample does not satisfy it. The caller compares
// it to the run's instant for "for at least N minutes".
func since[T any](ss []Sample[T], pred func(T) bool) (time.Time, bool) {
	var start time.Time
	for i := len(ss) - 1; i >= 0; i-- {
		if !pred(ss[i].V) {
			break
		}
		start = ss[i].At
	}
	return start, !start.IsZero()
}

// last returns at most the n newest samples, oldest first.
func last[T any](ss []Sample[T], n int) []Sample[T] {
	if len(ss) > n {
		return ss[len(ss)-n:]
	}
	return ss
}
