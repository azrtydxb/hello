package detect

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
)

// auth_bruteforce (spec S-19): a source IP whose failed-auth counter reached
// hello-sip's throttle limit, or whose rejected attempts name at least
// bruteUsernames distinct usernames in bruteWindow.
const (
	bruteWindow    = 10 * time.Minute
	bruteUsernames = 5
)

func authBruteforce(ctx context.Context, r *Run) ([]Candidate, error) {
	att, err := r.registerAttempts(ctx)
	if err != nil {
		return nil, err
	}
	fails, err := r.Live.AllAuthFailures(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrValkeyUnavailable, err)
	}
	return evalBruteforce(att, fails, r.authFailLimit(), r.Now), nil
}

// evalBruteforce is the detector over what it read. The usernames and
// User-Agents in the evidence come from the network: they are untrusted.
func evalBruteforce(att map[string][]livestate.RegisterAttempt, fails []livestate.AuthFailure, limit int64, now time.Time) []Candidate {
	type src struct {
		users, agents map[string]bool
		n             int64
		failures      int64
	}
	byIP := map[string]*src{}
	get := func(ip string) *src {
		if byIP[ip] == nil {
			byIP[ip] = &src{users: map[string]bool{}, agents: map[string]bool{}}
		}
		return byIP[ip]
	}
	for device, as := range att {
		for _, a := range as {
			if !rejected(a) || now.Sub(a.At) > bruteWindow || a.IP == "" {
				continue
			}
			s := get(a.IP)
			s.users[device] = true
			if a.UserAgent != "" {
				s.agents[a.UserAgent] = true
			}
			s.n++
		}
	}
	atLimit := map[string]bool{}
	for _, f := range fails {
		if f.Failures >= limit {
			atLimit[f.IP] = true
			get(f.IP).failures = f.Failures
		}
	}
	var out []Candidate
	for ip, s := range byIP {
		if !atLimit[ip] && len(s.users) < bruteUsernames {
			continue
		}
		users, agents := keys(s.users), keys(s.agents)
		out = append(out, candidate("auth_bruteforce", ip, Critical,
			fmt.Sprintf("Repeated failed authentication from %s", ip),
			map[string]any{"ip": ip, "failedAuthCount": s.failures, "throttleLimit": limit, "rejectedAttempts": s.n,
				"distinctUsernames": len(s.users), "usernames": users[:min(len(users), maxSamples)],
				"userAgents": agents[:min(len(agents), maxSamples)], "windowMinutes": int(bruteWindow.Minutes())}))
	}
	slices.SortFunc(out, func(a, b Candidate) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
