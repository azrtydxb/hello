package detect

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
)

func nodeSamples(f func(i int) nodeSample, n int) []Sample[nodeSample] {
	out := make([]Sample[nodeSample], n)
	for i := range out {
		out[i] = Sample[nodeSample]{At: t0.Add(-time.Duration(n-1-i) * time.Minute), V: f(i)}
	}
	return out
}

func testNodeHealth(t *testing.T) {
	ready := nodeSample{Ready: true, StartedAt: t0.Add(-time.Hour)}
	notReady := func(reason string) func(int) nodeSample {
		return func(int) nodeSample { return nodeSample{Reason: reason, StartedAt: ready.StartedAt} }
	}
	for _, tc := range []struct {
		name string
		ss   []Sample[nodeSample]
		want []string
	}{
		{"ready", nodeSamples(func(int) nodeSample { return ready }, 10), nil},
		{"not ready 1 minute", nodeSamples(notReady("starting"), 2), nil},
		{"not ready 2 minutes", nodeSamples(notReady("starting"), 3), []string{"node_health:sip-1:not_ready/warning"}},
		{"dependency 1 minute", nodeSamples(notReady("PostgreSQL unreachable"), 2), nil},
		{"dependency 2 minutes", nodeSamples(notReady("PostgreSQL unreachable"), 3), []string{"node_health:sip-1:not_ready/critical"}},
		{"valkey dependency", nodeSamples(notReady("valkey: timeout"), 3), []string{"node_health:sip-1:not_ready/critical"}},
		{"lag 4 minutes", nodeSamples(func(int) nodeSample { s := ready; s.Lag = true; return s }, 5), nil},
		{"lag 5 minutes", nodeSamples(func(int) nodeSample { s := ready; s.Lag = true; return s }, 6), []string{"node_health:sip-1:config_lag/warning"}},
		{"lag caught up", nodeSamples(func(i int) nodeSample { s := ready; s.Lag = i < 8; return s }, 10), nil},
		{"2 restarts in an hour", nodeSamples(func(i int) nodeSample {
			s := ready
			s.StartedAt = t0.Add(-time.Duration(60-i/10*20) * time.Minute)
			return s
		}, 30), nil},
		{"3 restarts in an hour", nodeSamples(func(i int) nodeSample {
			s := ready
			s.StartedAt = t0.Add(-time.Duration(i/10) * time.Minute)
			return s
		}, 40), []string{"node_health:sip-1:flapping/warning"}},
		{"2 tombstones", nodeSamples(func(i int) nodeSample {
			s := ready
			s.Offline, s.Ready = i == 3 || i == 6, i != 3 && i != 6
			return s
		}, 10), nil},
		{"3 tombstones", nodeSamples(func(i int) nodeSample {
			s := ready
			s.Offline = i%2 == 1 && i < 6
			s.Ready = !s.Offline
			return s
		}, 10), []string{"node_health:sip-1:flapping/warning"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantIDs(t, evalNode("sip-1", "sip", tc.ss, t0), tc.want...)
		})
	}
	t.Run("postgres samples and the cluster", func(t *testing.T) {
		env, _ := testEnv(t)
		env.Cluster = fakeMembers{
			{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Unhealthy, Reason: "PostgreSQL down", StartedAt: t0.Add(-time.Hour), ConfigRevision: 5},
			{ID: "ctl-1", Kind: cluster.KindControl, State: cluster.Ready, StartedAt: t0.Add(-time.Hour)},
		}
		exec(t, env.DB, `UPDATE schema_info SET config_revision = 5`)
		exec(t, env.DB, `INSERT INTO ai_samples (kind, subject, at, value) VALUES ('node', 'sip-1', $1, '{"ready":false,"reason":"PostgreSQL down","startedAt":"2026-10-08T11:00:00Z"}')`, t0.Add(-2*time.Minute))
		got, err := nodeHealth(context.Background(), NewRun(env))
		if err != nil {
			t.Fatal(err)
		}
		wantIDs(t, got, "node_health:sip-1:not_ready/critical")
	})
}

func testCapacity(t *testing.T) {
	load := func(active ...int) []Sample[trunkLoad] {
		var out []Sample[trunkLoad]
		for i, a := range active {
			out = append(out, Sample[trunkLoad]{At: t0.Add(-time.Duration(len(active)-i) * time.Minute), V: trunkLoad{Active: a, Max: 10}})
		}
		return out
	}
	for _, tc := range []struct {
		name string
		ss   []Sample[trunkLoad]
		want string
	}{
		{"2 of 5 at 80%", load(8, 8, 1, 1, 1), ""},
		{"3 of 5 at 80%", load(8, 8, 8, 1, 1), "warning"},
		{"3 of 5, one just below 80%", load(8, 8, 7, 8, 1), "warning"},
		{"2 at 80% and one at 79%", load(8, 8, 7, 1, 1), ""},
		{"older than the last 5 do not count", load(9, 9, 9, 1, 1, 1, 1, 1), ""},
		{"full without overcommit", load(10, 10, 10, 10, 10), "warning"},
		{"overcommitted and full", load(10, 10, 11, 10, 10), "critical"},
		{"overcommitted earlier, now below full", load(11, 11, 11, 9, 9), "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := evalCapacity("carrier", tc.ss)
			if (tc.want == "") == ok || (ok && c.Severity != tc.want) {
				t.Fatalf("got %v %+v, want %q", ok, c, tc.want)
			}
		})
	}
	t.Run("postgres", func(t *testing.T) {
		env, live := testEnv(t)
		id := addTrunk(t, env.DB, "carrier", "ip", 10, true)
		addTrunk(t, env.DB, "unlimited", "ip", 0, true)
		live.trunks[id] = livestate.TrunkStatus{ActiveCalls: 8}
		for i := 1; i <= 2; i++ {
			exec(t, env.DB, `INSERT INTO ai_samples (kind, subject, at, value) VALUES ('trunk_active', 'carrier', $1, '{"active":8,"max":10}')`, t0.Add(-time.Duration(i)*time.Minute))
		}
		got, err := trunkCapacity(context.Background(), NewRun(env))
		if err != nil {
			t.Fatal(err)
		}
		wantIDs(t, got, "trunk_capacity:carrier/warning")
	})
}

func testCallQuality(t *testing.T) {
	q := func(lost, packets int64, jitter float64) qualityCDR {
		return qualityCDR{Packets: packets, Lost: lost, Jitter: jitter}
	}
	good, badLoss, badJit := q(0, 1000, 5), q(10, 990, 5), q(0, 1000, 100)
	for _, tc := range []struct {
		name string
		cdrs []qualityCDR
		want bool
	}{
		{"3 of 5 bad by loss", []qualityCDR{badLoss, badLoss, badLoss, good, good}, true},
		{"3 of 5 bad by jitter", []qualityCDR{badJit, badJit, badJit, good, good}, true},
		{"mixed loss and jitter", []qualityCDR{badLoss, badJit, badLoss, good, good}, true},
		{"2 of 5 bad", []qualityCDR{badLoss, badLoss, good, good, good}, false},
		{"loss just below 1%", []qualityCDR{q(9, 991, 0), q(9, 991, 0), q(9, 991, 0), good, good}, false},
		{"jitter just below 100 ms", []qualityCDR{q(0, 1000, 99.9), q(0, 1000, 99.9), q(0, 1000, 99.9), good, good}, false},
		{"only 4 CDRs", []qualityCDR{badLoss, badLoss, badLoss, badLoss}, false},
		{"no packets at all", []qualityCDR{q(0, 0, 0), q(0, 0, 0), q(0, 0, 0), q(0, 0, 0), q(0, 0, 0)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := evalQuality("trunk", "carrier", tc.cdrs)
			if ok != tc.want || (ok && c.Severity != Warning) {
				t.Fatalf("got %v %+v, want %v", ok, c, tc.want)
			}
		})
	}
	t.Run("postgres: the last 5 per trunk and per node", func(t *testing.T) {
		env, _ := testEnv(t)
		n := 0
		add := func(trunk, node string, age time.Duration, lost int64, jitter float64) {
			n++
			addCDR(t, env.DB, cdr{id: n, trunk: trunk, node: node, start: t0.Add(-age - time.Minute), end: t0.Add(-age),
				answered: true, packets: ptr(int64(1000 - lost)), lost: ptr(lost), jitter: ptr(jitter)})
		}
		// carrier: 3 bad of its last 5 on node sip-1; an older bad one and a CDR without quality do not matter.
		for i, bad := range []bool{true, true, true, false, false} {
			lost := int64(0)
			if bad {
				lost = 10
			}
			add("carrier", "sip-1", time.Duration(i+1)*time.Minute, lost, 1)
		}
		add("carrier", "sip-1", 6*time.Minute, 50, 1) // the sixth is outside "last 5"
		addCDR(t, env.DB, cdr{id: 900, trunk: "carrier", node: "sip-1", start: t0.Add(-2 * time.Minute), answered: true})
		// other: only 2 bad of 5 and just below the threshold on the rest.
		for i := range 5 {
			if i < 2 {
				add("other", "sip-2", time.Duration(i+1)*time.Minute, 10, 1)
			} else {
				add("other", "sip-2", time.Duration(i+1)*time.Minute, 9, 99.9)
			}
		}
		// stale: 5 bad CDRs that ended over an hour ago.
		for i := range 5 {
			add("stale", "sip-3", 61*time.Minute+time.Duration(i)*time.Minute, 500, 1)
		}
		got, err := callQuality(context.Background(), NewRun(env))
		if err != nil {
			t.Fatal(err)
		}
		wantIDs(t, got, "call_quality:trunk:carrier/warning", "call_quality:node:sip-1/warning")
		if len(got[0].Evidence["cdrs"].([]map[string]any)) != 5 {
			t.Fatalf("evidence = %v", got[0].Evidence)
		}
	})
}

func testConfigSmells(t *testing.T) {
	ctx := context.Background()
	t.Run("ring groups, extensions, routes and phones", func(t *testing.T) {
		env, _ := testEnv(t)
		db := env.DB
		e101, e102, e103 := addExtension(t, db, "101"), addExtension(t, db, "102"), addExtension(t, db, "103")
		d101 := addDevice(t, db, e101, "d101", true)
		addDevice(t, db, e102, "d102", false) // 102 has only a disabled device
		_ = e103                              // 103 has no device at all
		empty := queryID(t, db, `INSERT INTO ring_groups (name, strategy) VALUES ('empty', 'ring-all') RETURNING id`)
		dead := queryID(t, db, `INSERT INTO ring_groups (name, strategy) VALUES ('dead', 'ring-all') RETURNING id`)
		fine := queryID(t, db, `INSERT INTO ring_groups (name, strategy) VALUES ('fine', 'ring-all') RETURNING id`)
		_ = empty
		exec(t, db, `INSERT INTO ring_group_members (group_id, extension_id, position) VALUES ($1, $2, 1), ($1, $3, 2)`, dead, e102, e103)
		exec(t, db, `INSERT INTO ring_group_members (group_id, extension_id, position) VALUES ($1, $2, 1), ($1, $3, 2)`, fine, e101, e102)
		tOn := addTrunk(t, db, "on", "ip", 0, true)
		tOff := addTrunk(t, db, "off", "ip", 0, false)
		route := func(name string, pos int, enabled bool, trunks ...int64) {
			rid := queryID(t, db, `INSERT INTO outbound_routes (position, name, match_kind, match, enabled) VALUES ($1, $2, 'prefix', $3, $4) RETURNING id`, pos, name, fmt.Sprint(pos), enabled)
			for i, tr := range trunks {
				exec(t, db, `INSERT INTO outbound_route_trunks (route_id, trunk_id, position) VALUES ($1, $2, $3)`, rid, tr, i+1)
			}
		}
		route("all-off", 1, true, tOff)
		route("mixed", 2, true, tOff, tOn)
		route("disabled-route", 3, false, tOff)
		phone := func(mac string, created time.Time, fetched *time.Time, mismatch bool, dev int64) {
			exec(t, db, `INSERT INTO phones (mac, vendor, model, device_id, token_hash, token_enc, admin_password_enc, created_at, last_fetch_at, ua_mismatch)
				VALUES ($1, 'generic', 'm', $2, decode(md5($1) || md5($1), 'hex'), '\x01', '\x01', $3, $4, $5)`, mac, dev, created, fetched, mismatch)
		}
		d102 := queryID(t, db, `SELECT id FROM devices WHERE sip_username = 'd102'`)
		phone("aaaaaaaaaaaa", t0.Add(-25*time.Hour), nil, false, d101) // never fetched, over a day: smell
		phone("bbbbbbbbbbbb", t0.Add(-23*time.Hour), nil, false, d102) // under a day: fine
		d := addDevice(t, db, e101, "d-more", true)
		phone("cccccccccccc", t0.Add(-30*24*time.Hour), ptr(t0.Add(-7*24*time.Hour-time.Minute)), false, d)
		d2 := addDevice(t, db, e101, "d-more2", true)
		phone("dddddddddddd", t0.Add(-30*24*time.Hour), ptr(t0.Add(-7*24*time.Hour+time.Minute)), false, d2)
		d3 := addDevice(t, db, e101, "d-more3", true)
		phone("eeeeeeeeeeee", t0.Add(-30*24*time.Hour), ptr(t0.Add(-time.Hour)), true, d3)
		got, err := configSmells(ctx, NewRun(env))
		if err != nil {
			t.Fatal(err)
		}
		wantIDs(t, got,
			"config_smells:ring_group_empty:empty/warning",
			"config_smells:ring_group_unreachable:dead/warning",
			"config_smells:extension_no_device:103/info",
			"config_smells:route_trunks_disabled:all-off/warning",
			"config_smells:phone_never_fetched:aaaaaaaaaaaa/info",
			"config_smells:phone_stale:cccccccccccc/info",
			"config_smells:phone_ua_mismatch:eeeeeeeeeeee/info",
		)
	})

	t.Run("a device unregistered at every run for 7 days", func(t *testing.T) {
		env, live := testEnv(t)
		e := addExtension(t, env.DB, "101")
		for _, u := range []string{"never", "once", "short", "off"} {
			addDevice(t, env.DB, e, u, u != "off")
		}
		day := t0.Truncate(24 * time.Hour)
		for i := 1; i <= 6; i++ { // 6 earlier days, today is written by the run
			at := day.AddDate(0, 0, -i)
			for _, u := range []string{"never", "once", "off"} {
				exec(t, env.DB, `INSERT INTO ai_samples (kind, subject, at, value) VALUES ('device_day', $1, $2, $3)`,
					u, at, fmt.Sprintf(`{"registered": %v}`, u == "once" && i == 3))
			}
			if i <= 5 { // "short" has only 5 earlier days of history, 6 with today
				exec(t, env.DB, `INSERT INTO ai_samples (kind, subject, at, value) VALUES ('device_day', 'short', $1, '{"registered": false}')`, at)
			}
		}
		live.bindings = nil
		got, err := deviceSmells(ctx, NewRun(env))
		if err != nil {
			t.Fatal(err)
		}
		wantIDs(t, got, "config_smells:device_never_registered:never/info")
		// today it registers: the day's sample turns true and the smell goes.
		live.bindings = []livestate.Binding{{Device: "never"}}
		got, err = deviceSmells(ctx, NewRun(env))
		if err != nil {
			t.Fatal(err)
		}
		wantIDs(t, got)
	})

	t.Run("shadowed outbound routes", func(t *testing.T) {
		route := func(pos int, name, kind, match string, mut func(*routing.OutboundRoute)) routing.OutboundRoute {
			r := routing.OutboundRoute{ID: int64(pos), Position: pos, Name: name, MatchKind: kind, Match: match, Trunks: []int64{1}, Enabled: true}
			if mut != nil {
				mut(&r)
			}
			return r
		}
		cfg := func(rs ...routing.OutboundRoute) routing.Config {
			return routing.Config{
				Trunks:     []routing.Trunk{{ID: 1, Name: "t", Mode: "ip", Enabled: true, Destinations: []routing.Destination{{Host: "198.51.100.1", Weight: 1}}}},
				Outbound:   rs,
				Extensions: map[string]string{"101": ""},
			}
		}
		for _, tc := range []struct {
			name string
			rs   []routing.OutboundRoute
			want []string
		}{
			{"a prefix under a shorter prefix", []routing.OutboundRoute{route(1, "all-0", "prefix", "0", nil), route(2, "uk", "prefix", "044", nil)},
				[]string{"config_smells:route_shadowed:uk/info"}},
			{"the order the other way is fine", []routing.OutboundRoute{route(1, "uk", "prefix", "044", nil), route(2, "all-0", "prefix", "0", nil)}, nil},
			{"a regex under a prefix", []routing.OutboundRoute{route(1, "all-0", "prefix", "0", nil), route(2, "mobile", "regex", `^05[0-9]{8}$`, nil)},
				[]string{"config_smells:route_shadowed:mobile/info"}},
			{"a partial overlap is not shadowed", []routing.OutboundRoute{route(1, "mobile", "regex", `^05[0-9]{8}$`, nil), route(2, "all-0", "prefix", "0", nil)}, nil},
			{"an earlier scheduled route does not shadow", []routing.OutboundRoute{
				route(1, "all-0", "prefix", "0", func(r *routing.OutboundRoute) {
					r.Schedule = &routing.Schedule{TimeZone: "UTC", Windows: []routing.Window{{Days: []time.Weekday{time.Monday}, Start: "09:00", End: "17:00"}}}
				}), route(2, "uk", "prefix", "044", nil)}, nil},
			{"an earlier route for other extensions does not shadow", []routing.OutboundRoute{
				route(1, "all-0", "prefix", "0", func(r *routing.OutboundRoute) { r.SourceExtensions = []string{"101"} }), route(2, "uk", "prefix", "044", nil)}, nil},
			{"a disabled earlier route does not shadow", []routing.OutboundRoute{
				route(1, "all-0", "prefix", "0", func(r *routing.OutboundRoute) { r.Enabled = false }), route(2, "uk", "prefix", "044", nil)}, nil},
			{"a regex alternation matched by an earlier prefix", []routing.OutboundRoute{route(1, "all-9", "prefix", "9", nil), route(2, "alt", "regex", `^(900|9111)$`, nil)},
				[]string{"config_smells:route_shadowed:alt/info"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				wantIDs(t, evalShadowed(cfg(tc.rs...), t0), tc.want...)
			})
		}
	})
}

func testSamples(t *testing.T) {
	ss := func(down ...bool) []Sample[trunkState] {
		var out []Sample[trunkState]
		for i, d := range down {
			out = append(out, Sample[trunkState]{At: t0.Add(time.Duration(i) * time.Minute), V: trunkState{Down: d}})
		}
		return out
	}
	pred := func(s trunkState) bool { return s.Down }
	if from, ok := since(ss(true, false, true, true), pred); !ok || !from.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("since = %v %v, want the start of the trailing run", from, ok)
	}
	if _, ok := since(ss(true, true, false), pred); ok {
		t.Fatal("a run that ended is not a trailing run")
	}
	if got := last(ss(true, false, true), 2); len(got) != 2 || got[0].V.Down {
		t.Fatalf("last = %v", got)
	}
	t.Run("history reads kinds apart and keeps order", func(t *testing.T) {
		env, _ := testEnv(t)
		r := NewRun(env)
		for i := 3; i >= 0; i-- {
			exec(t, env.DB, `INSERT INTO ai_samples (kind, subject, at, value) VALUES ('trunk_state', 'a', $1, $2)`, t0.Add(-time.Duration(i)*time.Minute), fmt.Sprintf(`{"down": %v}`, i%2 == 0))
		}
		exec(t, env.DB, `INSERT INTO ai_samples (kind, subject, at, value) VALUES ('node', 'a', $1, '{}')`, t0)
		if err := r.putSample(context.Background(), kindTrunkState, "a", trunkState{Down: true}); err != nil {
			t.Fatal(err)
		}
		if err := r.putSample(context.Background(), kindTrunkState, "a", trunkState{Down: false}); err != nil { // the same instant is kept once
			t.Fatal(err)
		}
		h, err := history[trunkState](context.Background(), env, kindTrunkState, t0.Add(-2*time.Minute))
		if err != nil || len(h["a"]) != 3 || !h["a"][2].V.Down {
			t.Fatalf("history = %+v, %v", h, err)
		}
	})
}
