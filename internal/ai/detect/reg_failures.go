package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/valkey-io/valkey-go"
)

// reg_failures (spec S-19): an enabled device with at least regFailMin
// rejected REGISTERs carrying credentials in regFailWindow.
const (
	regFailWindow = 10 * time.Minute
	regFailMin    = 10
)

const regAttemptsPrefix = "hello:regattempts:"

// rejected reports a REGISTER that carried credentials and was refused; a
// stale-nonce challenge is the normal handshake, not a failure.
func rejected(a livestate.RegisterAttempt) bool {
	return a.Credentials && a.Code >= 400 && !a.Stale
}

// registerAttempts reads every device's recorded attempts once per run
// (the keys exist only for devices that tried within the last hour).
func (r *Run) registerAttempts(ctx context.Context) (map[string][]livestate.RegisterAttempt, error) {
	if r.attRead {
		return r.attempts, r.attErr
	}
	r.attRead = true
	r.attempts, r.attErr = scanAttempts(ctx, r.valkeyClient())
	return r.attempts, r.attErr
}

func scanAttempts(ctx context.Context, c valkey.Client) (map[string][]livestate.RegisterAttempt, error) {
	if c == nil {
		return nil, ErrValkeyUnavailable
	}
	out := map[string][]livestate.RegisterAttempt{}
	var cursor uint64
	seenKeys := map[string]struct{}{}
	for {
		e, err := c.Do(ctx, c.B().Scan().Cursor(cursor).Match(regAttemptsPrefix+"*").Count(500).Build()).AsScanEntry()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrValkeyUnavailable, err)
		}
		// SCAN may return a key twice across iterations.
		var fresh []string
		for _, k := range e.Elements {
			if _, dup := seenKeys[k]; !dup {
				seenKeys[k] = struct{}{}
				fresh = append(fresh, k)
			}
		}
		e.Elements = fresh
		if len(e.Elements) > 0 {
			cmds := make([]valkey.Completed, len(e.Elements))
			for i, k := range e.Elements {
				cmds[i] = c.B().Lrange().Key(k).Start(0).Stop(livestate.RegisterAttemptsCap - 1).Build()
			}
			for i, res := range c.DoMulti(ctx, cmds...) {
				vs, err := res.AsStrSlice()
				if err != nil {
					if valkey.IsValkeyNil(err) {
						continue
					}
					return nil, fmt.Errorf("%w: %w", ErrValkeyUnavailable, err)
				}
				device := strings.TrimPrefix(e.Elements[i], regAttemptsPrefix)
				for _, v := range vs {
					var a livestate.RegisterAttempt
					if json.Unmarshal([]byte(v), &a) == nil {
						out[device] = append(out[device], a)
					}
				}
			}
		}
		if cursor = e.Cursor; cursor == 0 {
			return out, nil
		}
	}
}

func regFailures(ctx context.Context, r *Run) ([]Candidate, error) {
	att, err := r.registerAttempts(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT sip_username FROM devices WHERE enabled`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	enabled := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		enabled[u] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return evalRegFailures(att, enabled, r.Now), nil
}

// evalRegFailures is the detector over what it read.
func evalRegFailures(att map[string][]livestate.RegisterAttempt, enabled map[string]bool, now time.Time) []Candidate {
	var out []Candidate
	for device, as := range att {
		if !enabled[device] {
			continue
		}
		var bad []livestate.RegisterAttempt
		for _, a := range as {
			if rejected(a) && now.Sub(a.At) <= regFailWindow {
				bad = append(bad, a)
			}
		}
		if len(bad) < regFailMin {
			continue
		}
		slices.SortFunc(bad, func(a, b livestate.RegisterAttempt) int { return b.At.Compare(a.At) })
		rows := []map[string]any{}
		for _, a := range bad[:min(len(bad), maxSamples)] {
			rows = append(rows, map[string]any{"at": a.At, "ip": a.IP, "code": a.Code, "reason": a.Reason, "userAgent": a.UserAgent})
		}
		out = append(out, candidate("reg_failures", device, Warning,
			fmt.Sprintf("Device %s: %d rejected REGISTERs in %d minutes", device, len(bad), int(regFailWindow.Minutes())),
			map[string]any{"device": device, "rejected": len(bad), "windowMinutes": int(regFailWindow.Minutes()), "samples": rows}))
	}
	slices.SortFunc(out, func(a, b Candidate) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (e *Env) valkeyClient() valkey.Client {
	if e.VK == nil && e.VKFunc != nil {
		return e.VKFunc()
	}
	return e.VK
}
