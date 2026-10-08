package detect

import (
	"context"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
)

// config_smells (spec S-19): configuration that is probably a mistake. Info
// unless noted. The type is config_smells and the subject starts with the
// smell, so each finding is its own candidate.
const (
	deviceNeverRegisteredDays = 7
	phoneNeverFetchedAfter    = 24 * time.Hour
	phoneStaleAfter           = 7 * 24 * time.Hour
	maxExampleDigits          = 15 // E.164
)

func configSmells(ctx context.Context, r *Run) ([]Candidate, error) {
	var out []Candidate
	for _, q := range []func(context.Context, *Run) ([]Candidate, error){
		ringGroupSmells, extensionSmells, routeSmells, shadowedRoutes, phoneSmells, deviceSmells,
	} {
		cs, err := q(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	slices.SortFunc(out, func(a, b Candidate) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// names runs a query returning one text column per row.
func names(ctx context.Context, r *Run, q string, args ...any) ([]string, error) {
	rows, err := r.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func smell(kind, name, severity, title string, ev map[string]any) Candidate {
	if ev == nil {
		ev = map[string]any{}
	}
	ev["smell"], ev["name"] = kind, name
	return candidate("config_smells", kind+":"+name, severity, title, ev)
}

func ringGroupSmells(ctx context.Context, r *Run) ([]Candidate, error) {
	empty, err := names(ctx, r, `SELECT g.name FROM ring_groups g
		WHERE NOT EXISTS (SELECT 1 FROM ring_group_members m WHERE m.group_id = g.id) ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	unreachable, err := names(ctx, r, `SELECT g.name FROM ring_groups g
		WHERE EXISTS (SELECT 1 FROM ring_group_members m WHERE m.group_id = g.id)
		AND NOT EXISTS (SELECT 1 FROM ring_group_members m JOIN devices d ON d.extension_id = m.extension_id AND d.enabled
		                WHERE m.group_id = g.id) ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, n := range empty {
		out = append(out, smell("ring_group_empty", n, Warning, fmt.Sprintf("Ring group %s has no members", n), nil))
	}
	for _, n := range unreachable {
		out = append(out, smell("ring_group_unreachable", n, Warning, fmt.Sprintf("No member of ring group %s has an enabled device", n), nil))
	}
	return out, nil
}

func extensionSmells(ctx context.Context, r *Run) ([]Candidate, error) {
	ns, err := names(ctx, r, `SELECT e.number FROM extensions e
		WHERE NOT EXISTS (SELECT 1 FROM devices d WHERE d.extension_id = e.id) ORDER BY e.number`)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, n := range ns {
		out = append(out, smell("extension_no_device", n, Info, fmt.Sprintf("Extension %s has no device", n), nil))
	}
	return out, nil
}

func routeSmells(ctx context.Context, r *Run) ([]Candidate, error) {
	ns, err := names(ctx, r, `SELECT o.name FROM outbound_routes o
		WHERE o.enabled AND EXISTS (SELECT 1 FROM outbound_route_trunks x WHERE x.route_id = o.id)
		AND NOT EXISTS (SELECT 1 FROM outbound_route_trunks x JOIN trunks t ON t.id = x.trunk_id AND t.enabled
		                WHERE x.route_id = o.id) ORDER BY o.position`)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, n := range ns {
		out = append(out, smell("route_trunks_disabled", n, Warning, fmt.Sprintf("Every trunk of outbound route %s is disabled", n), nil))
	}
	return out, nil
}

func phoneSmells(ctx context.Context, r *Run) ([]Candidate, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT mac, label, created_at, last_fetch_at, ua_mismatch FROM phones
		WHERE enabled AND ((last_fetch_at IS NULL AND created_at < $1) OR last_fetch_at < $2 OR (last_fetch_at IS NOT NULL AND ua_mismatch))
		ORDER BY mac`, r.Now.Add(-phoneNeverFetchedAfter), r.Now.Add(-phoneStaleAfter))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Candidate
	for rows.Next() {
		var mac, label string
		var created time.Time
		var fetched *time.Time
		var mismatch bool
		if err := rows.Scan(&mac, &label, &created, &fetched, &mismatch); err != nil {
			return nil, err
		}
		ev := map[string]any{"mac": mac, "label": label, "createdAt": created, "lastFetchAt": fetched}
		switch {
		case fetched == nil:
			out = append(out, smell("phone_never_fetched", mac, Info, fmt.Sprintf("Phone %s never fetched its configuration", mac), ev))
		case mismatch:
			out = append(out, smell("phone_ua_mismatch", mac, Info, fmt.Sprintf("Phone %s's last fetch did not match its model", mac), ev))
		default:
			out = append(out, smell("phone_stale", mac, Info, fmt.Sprintf("Phone %s has not fetched its configuration for 7 days", mac), ev))
		}
	}
	return out, rows.Err()
}

// deviceSmells reports enabled devices never registered on any of the last
// deviceNeverRegisteredDays days, from the per-day samples this run also
// updates (one row per device per UTC day, true once it was seen registered).
func deviceSmells(ctx context.Context, r *Run) ([]Candidate, error) {
	if r.Live == nil {
		return nil, ErrValkeyUnavailable
	}
	bs, err := r.Live.AllBindings(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrValkeyUnavailable, err)
	}
	registered := make([]string, 0, len(bs))
	for _, b := range bs {
		registered = append(registered, b.Device)
	}
	day := r.Now.Truncate(24 * time.Hour)
	// One row per enabled device for today: registered stays true once true.
	if _, err := r.DB.ExecContext(ctx, `INSERT INTO ai_samples (kind, subject, at, value)
		SELECT $1, d.sip_username, $2::timestamptz, jsonb_build_object('registered', d.sip_username = ANY($3))
		FROM devices d WHERE d.enabled
		ON CONFLICT (kind, subject, at) DO UPDATE
		SET value = jsonb_build_object('registered', (ai_samples.value->>'registered')::boolean OR (EXCLUDED.value->>'registered')::boolean)`,
		kindDeviceDay, day, registered); err != nil {
		return nil, fmt.Errorf("write device samples: %w", err)
	}
	ns, err := names(ctx, r, `SELECT d.sip_username FROM devices d
		JOIN ai_samples s ON s.kind = $1 AND s.subject = d.sip_username AND s.at >= $2 AND s.at <= $3
		WHERE d.enabled GROUP BY d.sip_username
		HAVING count(*) >= $4 AND bool_and(NOT (s.value->>'registered')::boolean) ORDER BY d.sip_username`,
		kindDeviceDay, day.AddDate(0, 0, 1-deviceNeverRegisteredDays), day, deviceNeverRegisteredDays)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, n := range ns {
		out = append(out, smell("device_never_registered", n, Info,
			fmt.Sprintf("Device %s was not registered on any of the last %d days", n, deviceNeverRegisteredDays),
			map[string]any{"days": deviceNeverRegisteredDays}))
	}
	return out, nil
}

// shadowedRoutes finds enabled outbound routes that can never match because
// an earlier, always-open route matches every number they would: the
// routing package's own matcher runs over the shortest and longest example
// number of the route's pattern, from each of its source extensions.
func shadowedRoutes(ctx context.Context, r *Run) ([]Candidate, error) {
	snap, err := r.Store.RoutingConfig(ctx)
	if err != nil {
		return nil, err
	}
	return evalShadowed(snap.Config, r.Now), nil
}

func evalShadowed(cfg routing.Config, now time.Time) []Candidate {
	table, errs := routing.Compile(cfg)
	if len(errs) > 0 {
		return nil // an invalid configuration is the API's to report
	}
	byName := map[string]routing.OutboundRoute{}
	for _, o := range cfg.Outbound {
		byName[o.Name] = o
	}
	var out []Candidate
	for _, o := range cfg.Outbound {
		if !o.Enabled {
			continue
		}
		by, ex := "", exampleNumbers(o)
		if len(ex) == 0 {
			continue
		}
		sources := o.SourceExtensions
		if len(sources) == 0 {
			sources = []string{"0000000000"} // not an extension, so only unrestricted routes match it
		}
		shadowed := true
		for _, src := range sources[:min(len(sources), 10)] {
			for _, n := range ex {
				d := table.Decide(routing.Call{FromExtension: src, Number: n, At: now}, func(int64, bool) (bool, string) { return true, "" })
				e, ok := byName[d.Route]
				if d.Route == "" || d.Route == o.Name || !ok || e.Position >= o.Position || e.Schedule != nil {
					shadowed = false
				}
				by = d.Route
			}
		}
		if shadowed {
			out = append(out, smell("route_shadowed", o.Name, Info, fmt.Sprintf("Outbound route %s can never match: %s comes first", o.Name, by),
				map[string]any{"route": o.Name, "shadowedBy": by, "exampleNumbers": ex}))
		}
	}
	return out
}

// exampleNumbers returns the shortest and longest number a route's pattern
// matches, as far as they can be built from the pattern; none for a
// pattern this cannot build a verified example of.
func exampleNumbers(o routing.OutboundRoute) []string {
	var cands []string
	var re *regexp.Regexp
	if o.MatchKind == "prefix" {
		cands = []string{o.Match, o.Match + strings.Repeat("0", max(0, maxExampleDigits-len(o.Match)))}
	} else {
		parsed, err := syntax.Parse(o.Match, syntax.Perl)
		if err != nil {
			return nil
		}
		if re, err = regexp.Compile(o.Match); err != nil {
			return nil
		}
		cands = []string{buildExample(parsed, false), buildExample(parsed, true)}
	}
	var out []string
	for _, c := range cands {
		ok := c != "" && (re == nil && strings.HasPrefix(c, o.Match) || re != nil && re.MatchString(c))
		if ok && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// buildExample builds a string the regexp tree matches: the shortest
// alternatives and fewest repeats, or the longest within maxExampleDigits.
func buildExample(re *syntax.Regexp, long bool) string {
	switch re.Op {
	case syntax.OpLiteral:
		return string(re.Rune)
	case syntax.OpCharClass:
		if len(re.Rune) == 0 {
			return ""
		}
		for i := 0; i+1 < len(re.Rune); i += 2 { // prefer a digit
			if re.Rune[i] <= '0' && '0' <= re.Rune[i+1] {
				return "0"
			}
		}
		return string(re.Rune[0])
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return "0"
	case syntax.OpCapture:
		return buildExample(re.Sub[0], long)
	case syntax.OpConcat:
		var b strings.Builder
		for _, s := range re.Sub {
			b.WriteString(buildExample(s, long))
		}
		return b.String()
	case syntax.OpAlternate:
		best := buildExample(re.Sub[0], long)
		for _, s := range re.Sub[1:] {
			if c := buildExample(s, long); (long && len(c) > len(best)) || (!long && len(c) < len(best)) {
				best = c
			}
		}
		return best
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		lo, hi := 0, 1
		switch re.Op {
		case syntax.OpPlus:
			lo, hi = 1, maxExampleDigits
		case syntax.OpStar:
			lo, hi = 0, maxExampleDigits
		case syntax.OpRepeat:
			lo, hi = re.Min, re.Max
			if hi < 0 {
				hi = maxExampleDigits
			}
		}
		n := lo
		if long {
			n = min(hi, maxExampleDigits)
		}
		return strings.Repeat(buildExample(re.Sub[0], long), n)
	}
	return "" // anchors, empty matches
}
