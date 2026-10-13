package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/jackc/pgx/v5"
)

// Router is a compiled routing table as hello-sip uses it; *routing.Table
// satisfies it (and stubRouter, for snapshots without a compiled table).
type Router interface {
	Decide(c routing.Call, usable routing.TrunkUsability) routing.Decision
	Trunk(id int64) (*routing.Trunk, bool)
	TrunkForSource(ip netip.Addr, did string) (*routing.Trunk, bool)
}

// RoutingState is the trunk and route part of a snapshot.
type RoutingState struct {
	Router Router
	// Config is what Router was compiled from; trunk passwords inside are
	// in clear and must never be logged.
	Config routing.Config
	// Misconfigured maps a trunk ID to why it cannot be used (its sealed
	// password does not open). Such a trunk is not registered and routing
	// skips it.
	Misconfigured map[int64]string
	// Resolved maps a destination (livestate.DestinationKey) to the
	// addresses ("ip:port") it resolved to; empty until DNS has run.
	Resolved map[string][]string
	// Errors are this revision's compile errors; when set, Router and
	// Config are the last good revision's.
	Errors []routing.FieldError
}

// Routing returns the snapshot's routing state. A snapshot built with New
// routes only to internal extensions.
func (s *Snapshot) Routing() *RoutingState {
	if s.routing == nil {
		return &RoutingState{Router: stubRouter{snap: s}}
	}
	return s.routing
}

// WithRouting attaches routing state (tests and Load); it returns s.
func (s *Snapshot) WithRouting(r *RoutingState) *Snapshot {
	s.routing = r
	return s
}

// loadRouting reads trunks, destinations, routes and external numbers. A
// password that does not open marks its trunk misconfigured (logged); it is
// never dropped silently and never logged itself.
func loadRouting(ctx context.Context, db Querier, box *secret.Box, log *slog.Logger) (routing.Config, map[int64]string, error) {
	cfg := routing.Config{Extensions: map[string]string{}, ResolvedIPs: map[int64][]netip.Addr{}}
	bad := map[int64]string{}

	rows, err := db.Query(ctx, `SELECT id, name, mode, username, password_enc, realm, from_domain, register_expires,
		options_interval, source_cidrs, max_calls, default_caller_id, enabled FROM trunks ORDER BY id`)
	if err != nil {
		return cfg, nil, fmt.Errorf("snapshot: trunks: %w", err)
	}
	for rows.Next() {
		var (
			t         routing.Trunk
			sealed    []byte
			reg, opts int32
			maxCalls  int32
		)
		if err := rows.Scan(&t.ID, &t.Name, &t.Mode, &t.Username, &sealed, &t.Realm, &t.FromDomain, &reg,
			&opts, &t.SourceCIDRs, &maxCalls, &t.DefaultCallerID, &t.Enabled); err != nil {
			rows.Close()
			return cfg, nil, fmt.Errorf("snapshot: scan trunk: %w", err)
		}
		t.RegisterExpires = time.Duration(reg) * time.Second
		t.OptionsInterval = time.Duration(opts) * time.Second
		t.MaxCalls = int(maxCalls)
		if sealed != nil {
			if box == nil {
				bad[t.ID] = "no secret key to open the trunk password"
			} else if pw, err := box.Open(sealed, "trunk:"+strconv.FormatInt(t.ID, 10)); err != nil {
				bad[t.ID] = "trunk password does not open with HELLO_SECRET_KEY"
			} else {
				t.Password = pw
			}
		}
		cfg.Trunks = append(cfg.Trunks, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return cfg, nil, fmt.Errorf("snapshot: trunks: %w", err)
	}
	for id, why := range bad {
		if log != nil {
			log.Error("trunk misconfigured: it will not be registered or used", "trunk_id", id, "reason", why)
		}
	}

	byID := map[int64]*routing.Trunk{}
	for i := range cfg.Trunks {
		byID[cfg.Trunks[i].ID] = &cfg.Trunks[i]
	}
	rows, err = db.Query(ctx, `SELECT trunk_id, host, port, priority, weight FROM trunk_destinations ORDER BY trunk_id, priority, id`)
	if err != nil {
		return cfg, nil, fmt.Errorf("snapshot: destinations: %w", err)
	}
	for rows.Next() {
		var (
			id                   int64
			d                    routing.Destination
			port, prio, weight32 int32
		)
		if err := rows.Scan(&id, &d.Host, &port, &prio, &weight32); err != nil {
			rows.Close()
			return cfg, nil, fmt.Errorf("snapshot: scan destination: %w", err)
		}
		d.Port, d.Priority, d.Weight = int(port), int(prio), int(weight32)
		if t := byID[id]; t != nil {
			t.Destinations = append(t.Destinations, d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return cfg, nil, fmt.Errorf("snapshot: destinations: %w", err)
	}

	if err := loadOutbound(ctx, db, &cfg); err != nil {
		return cfg, nil, err
	}
	if err := loadInbound(ctx, db, &cfg); err != nil {
		return cfg, nil, err
	}
	rows, err = db.Query(ctx, `SELECT number, external_number FROM extensions`)
	if err != nil {
		return cfg, nil, fmt.Errorf("snapshot: external numbers: %w", err)
	}
	for rows.Next() {
		var n, ext string
		if err := rows.Scan(&n, &ext); err != nil {
			rows.Close()
			return cfg, nil, fmt.Errorf("snapshot: scan extension: %w", err)
		}
		cfg.Extensions[n] = ext
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return cfg, nil, fmt.Errorf("snapshot: external numbers: %w", err)
	}
	return cfg, bad, nil
}

func loadOutbound(ctx context.Context, db Querier, cfg *routing.Config) error {
	rows, err := db.Query(ctx, `SELECT id, position, name, match_kind, match, source_extensions, schedule,
		number_transform, callerid_transform, failover_codes, emergency, enabled FROM outbound_routes ORDER BY position`)
	if err != nil {
		return fmt.Errorf("snapshot: outbound routes: %w", err)
	}
	idx := map[int64]int{}
	for rows.Next() {
		var (
			r                   routing.OutboundRoute
			pos                 int32
			sched, num, callerT []byte
			codes               []int32
		)
		if err := rows.Scan(&r.ID, &pos, &r.Name, &r.MatchKind, &r.Match, &r.SourceExtensions, &sched,
			&num, &callerT, &codes, &r.Emergency, &r.Enabled); err != nil {
			rows.Close()
			return fmt.Errorf("snapshot: scan outbound route: %w", err)
		}
		r.Position = int(pos)
		if err := decodeJSON(sched, &r.Schedule, num, &r.Number, callerT, &r.CallerID); err != nil {
			rows.Close()
			return fmt.Errorf("snapshot: outbound route %d: %w", r.ID, err)
		}
		for _, c := range codes {
			r.FailoverCodes = append(r.FailoverCodes, int(c))
		}
		idx[r.ID] = len(cfg.Outbound)
		cfg.Outbound = append(cfg.Outbound, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("snapshot: outbound routes: %w", err)
	}
	rows, err = db.Query(ctx, `SELECT route_id, trunk_id FROM outbound_route_trunks ORDER BY route_id, position`)
	if err != nil {
		return fmt.Errorf("snapshot: route trunks: %w", err)
	}
	for rows.Next() {
		var route, trunk int64
		if err := rows.Scan(&route, &trunk); err != nil {
			rows.Close()
			return fmt.Errorf("snapshot: scan route trunk: %w", err)
		}
		if i, ok := idx[route]; ok {
			cfg.Outbound[i].Trunks = append(cfg.Outbound[i].Trunks, trunk)
		}
	}
	rows.Close()
	return rows.Err()
}

func loadInbound(ctx context.Context, db Querier, cfg *routing.Config) error {
	rows, err := db.Query(ctx, `SELECT id, position, name, did_kind, did, coalesce(trunk_id, 0), sip_domain, header_name,
		header_regex, schedule, callerid_transform, destination_kind, destination, enabled FROM inbound_routes ORDER BY position`)
	if err != nil {
		return fmt.Errorf("snapshot: inbound routes: %w", err)
	}
	for rows.Next() {
		var (
			r              routing.InboundRoute
			pos            int32
			sched, callerT []byte
		)
		if err := rows.Scan(&r.ID, &pos, &r.Name, &r.DIDKind, &r.DID, &r.TrunkID, &r.SIPDomain, &r.HeaderName,
			&r.HeaderRegex, &sched, &callerT, &r.DestinationKind, &r.Destination, &r.Enabled); err != nil {
			rows.Close()
			return fmt.Errorf("snapshot: scan inbound route: %w", err)
		}
		r.Position = int(pos)
		if err := decodeJSON(sched, &r.Schedule, nil, nil, callerT, &r.CallerID); err != nil {
			rows.Close()
			return fmt.Errorf("snapshot: inbound route %d: %w", r.ID, err)
		}
		cfg.Inbound = append(cfg.Inbound, r)
	}
	rows.Close()
	return rows.Err()
}

// decodeJSON decodes the nullable schedule and the two transforms.
func decodeJSON(sched []byte, s **routing.Schedule, num []byte, n *routing.Transform, cid []byte, c *routing.Transform) error {
	if len(sched) > 0 && string(sched) != "null" {
		var v routing.Schedule
		if err := json.Unmarshal(sched, &v); err != nil {
			return fmt.Errorf("schedule: %w", err)
		}
		*s = &v
	}
	if n != nil && len(num) > 0 {
		if err := json.Unmarshal(num, n); err != nil {
			return fmt.Errorf("number transform: %w", err)
		}
	}
	if len(cid) > 0 {
		if err := json.Unmarshal(cid, c); err != nil {
			return fmt.Errorf("caller-ID transform: %w", err)
		}
	}
	return nil
}

// buildRouting compiles cfg with the resolution results.
func buildRouting(cfg routing.Config, bad map[int64]string, resolved map[string][]string) *RoutingState {
	cfg.ResolvedIPs = resolvedIPs(cfg.Trunks, resolved)
	rs := &RoutingState{Config: cfg, Misconfigured: bad, Resolved: resolved}
	rs.Router, rs.Errors = compile(cfg)
	if rs.Router == nil {
		// Never nil: the watcher swaps in the last good table on install.
		rs.Router = stubRouter{cfg: cfg}
	}
	return rs
}

// compile is compileRouting; tests replace it to simulate compile errors,
// which the stub never produces.
var compile = compileRouting

// resolvedIPs collects, per trunk, every IP its destinations resolved to,
// including literal-IP destinations, for source validation.
func resolvedIPs(trunks []routing.Trunk, resolved map[string][]string) map[int64][]netip.Addr {
	out := map[int64][]netip.Addr{}
	for _, t := range trunks {
		var ips []netip.Addr
		for _, d := range t.Destinations {
			if ip, err := netip.ParseAddr(d.Host); err == nil {
				ips = append(ips, ip.Unmap())
			}
			for _, a := range resolved[livestate.DestinationKey(d)] {
				if ap, err := netip.ParseAddrPort(a); err == nil && !slices.Contains(ips, ap.Addr().Unmap()) {
					ips = append(ips, ap.Addr().Unmap())
				}
			}
		}
		if len(ips) > 0 {
			out[t.ID] = ips
		}
	}
	return out
}

// Resolver is the DNS lookup the watcher uses; *net.Resolver satisfies it.
type Resolver interface {
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// resolveDestination returns the "ip:port" addresses of a destination:
// a literal IP as is; a hostname with port 0 by SRV (_sip._udp), else
// A/AAAA on 5060; a hostname with a port by A/AAAA.
func resolveDestination(ctx context.Context, r Resolver, d routing.Destination) ([]string, error) {
	port := d.Port
	if ip, err := netip.ParseAddr(d.Host); err == nil {
		if port == 0 {
			port = 5060
		}
		return []string{netip.AddrPortFrom(ip, uint16(port)).String()}, nil //nolint:gosec // port is 0..65535 by schema.
	}
	type target struct {
		host string
		port int
	}
	var targets []target
	if port == 0 {
		if _, srvs, err := r.LookupSRV(ctx, "sip", "udp", d.Host); err == nil {
			for _, s := range srvs {
				targets = append(targets, target{s.Target, int(s.Port)})
			}
		}
		if len(targets) == 0 {
			targets = []target{{d.Host, 5060}}
		}
	} else {
		targets = []target{{d.Host, port}}
	}
	var (
		out     []string
		lastErr error
	)
	for _, t := range targets {
		addrs, err := r.LookupHost(ctx, t.host)
		if err != nil {
			lastErr = err
			continue
		}
		for _, a := range addrs {
			if ip, err := netip.ParseAddr(a); err == nil {
				out = append(out, netip.AddrPortFrom(ip.Unmap(), uint16(t.port)).String()) //nolint:gosec // SRV/config port.
			}
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// resolveAll resolves every destination of every trunk.
// resolveAll resolves every destination of every trunk. A lookup that
// fails keeps that destination's previous result (a DNS hiccup must not
// wipe a carrier's source addresses and 403 its calls); failed lists the
// destinations that failed.
func resolveAll(ctx context.Context, r Resolver, trunks []routing.Trunk, prev map[string][]string) (out map[string][]string, failed []string) {
	out = map[string][]string{}
	for _, t := range trunks {
		for _, d := range t.Destinations {
			k := livestate.DestinationKey(d)
			if _, done := out[k]; done {
				continue
			}
			dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			addrs, err := resolveDestination(dctx, r, d)
			cancel()
			if err != nil {
				failed = append(failed, k)
				addrs = prev[k]
			}
			out[k] = addrs
		}
	}
	return out, failed
}

// beginner is a connection that can open the read-only, repeatable-read
// transaction the whole snapshot is read in, so devices and routes come from
// one revision.
type beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}
