package detect

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/livestate"
)

var _ ai.Agent = (*AIOps)(nil)

func wantIDs(t *testing.T, got []Candidate, want ...string) {
	t.Helper()
	g := ids(got)
	slices.Sort(g)
	slices.Sort(want)
	if !slices.Equal(g, want) {
		t.Fatalf("candidates = %v, want %v", g, want)
	}
}

// TestDetectors fails if any detector misses its candidate at its S-19
// threshold, raises one just below it, or loses its evidence. The pure
// decision of each detector has a table; the readers run against PostgreSQL
// (and Valkey for the register-attempt keys) in the DB subtests.
func TestDetectors(t *testing.T) {
	t.Run("reg_failures", testRegFailures)
	t.Run("auth_bruteforce", testBruteforce)
	t.Run("trunk_down", testTrunkDown)
	t.Run("trunk_asr_drop", testASRDrop)
	t.Run("node_health", testNodeHealth)
	t.Run("trunk_capacity", testCapacity)
	t.Run("call_quality", testCallQuality)
	t.Run("config_smells", testConfigSmells)
	t.Run("samples", testSamples)
}

func attempts(n int, age time.Duration, f func(*livestate.RegisterAttempt)) []livestate.RegisterAttempt {
	out := make([]livestate.RegisterAttempt, n)
	for i := range out {
		out[i] = livestate.RegisterAttempt{At: t0.Add(-age), Device: "desk", IP: "10.0.0.9", Credentials: true, Code: 403, Reason: "Forbidden", UserAgent: "Yealink"}
		if f != nil {
			f(&out[i])
		}
	}
	return out
}

func testRegFailures(t *testing.T) {
	enabled := map[string]bool{"desk": true}
	for _, tc := range []struct {
		name string
		att  []livestate.RegisterAttempt
		on   map[string]bool
		want []string
	}{
		{"below threshold: 9 rejected", attempts(9, time.Minute, nil), enabled, nil},
		{"at threshold: 10 rejected", attempts(10, time.Minute, nil), enabled, []string{"reg_failures:desk/warning"}},
		{"above threshold: 20 rejected", attempts(20, time.Minute, nil), enabled, []string{"reg_failures:desk/warning"}},
		{"10 minutes old still counts", attempts(10, 10*time.Minute, nil), enabled, []string{"reg_failures:desk/warning"}},
		{"just past 10 minutes does not", attempts(10, 10*time.Minute+time.Second, nil), enabled, nil},
		{"without credentials is the handshake", attempts(10, time.Minute, func(a *livestate.RegisterAttempt) { a.Credentials, a.Code = false, 401 }), enabled, nil},
		{"stale nonce is the handshake", attempts(10, time.Minute, func(a *livestate.RegisterAttempt) { a.Code, a.Stale = 401, true }), enabled, nil},
		{"accepted", attempts(10, time.Minute, func(a *livestate.RegisterAttempt) { a.Code = 200 }), enabled, nil},
		{"disabled device", attempts(10, time.Minute, nil), map[string]bool{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evalRegFailures(map[string][]livestate.RegisterAttempt{"desk": tc.att}, tc.on, t0)
			wantIDs(t, got, tc.want...)
			if len(got) == 1 && (got[0].Evidence["rejected"] != len(tc.att) || len(got[0].Evidence["samples"].([]map[string]any)) > maxSamples) {
				t.Fatalf("evidence = %v", got[0].Evidence)
			}
		})
	}

	t.Run("valkey and postgres", func(t *testing.T) {
		env, _ := testEnv(t)
		env.VK = valkeyClient(t)
		ext := addExtension(t, env.DB, "101")
		addDevice(t, env.DB, ext, "desk", true)
		addDevice(t, env.DB, ext, "off", false)
		ls := livestate.New(env.VK)
		for _, dev := range []string{"desk", "off"} {
			for _, a := range attempts(10, time.Minute, func(a *livestate.RegisterAttempt) { a.Device = dev }) {
				if err := ls.RecordRegisterAttempt(context.Background(), a); err != nil {
					t.Fatal(err)
				}
			}
		}
		got, err := regFailures(context.Background(), NewRun(env))
		if err != nil {
			t.Fatal(err)
		}
		wantIDs(t, got, "reg_failures:desk/warning")
	})
}

func testBruteforce(t *testing.T) {
	byUsers := func(n int) map[string][]livestate.RegisterAttempt {
		out := map[string][]livestate.RegisterAttempt{}
		for i := range n {
			d := fmt.Sprintf("user%d", i)
			out[d] = attempts(1, time.Minute, func(a *livestate.RegisterAttempt) { a.Device = d; a.UserAgent = "sipvicious" })
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		att   map[string][]livestate.RegisterAttempt
		fails []livestate.AuthFailure
		want  []string
	}{
		{"4 usernames", byUsers(4), nil, nil},
		{"5 usernames", byUsers(5), nil, []string{"auth_bruteforce:10.0.0.9/critical"}},
		{"counter below the limit", nil, []livestate.AuthFailure{{IP: "10.0.0.9", Failures: 9}}, nil},
		{"counter at the limit", nil, []livestate.AuthFailure{{IP: "10.0.0.9", Failures: 10}}, []string{"auth_bruteforce:10.0.0.9/critical"}},
		{"counter above the limit", nil, []livestate.AuthFailure{{IP: "10.0.0.9", Failures: 50}}, []string{"auth_bruteforce:10.0.0.9/critical"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evalBruteforce(tc.att, tc.fails, 10, t0)
			wantIDs(t, got, tc.want...)
		})
	}
	t.Run("usernames older than 10 minutes", func(t *testing.T) {
		old := map[string][]livestate.RegisterAttempt{}
		for i := range 5 {
			d := fmt.Sprintf("user%d", i)
			old[d] = attempts(1, 11*time.Minute, func(a *livestate.RegisterAttempt) { a.Device = d })
		}
		wantIDs(t, evalBruteforce(old, nil, 10, t0))
	})
	t.Run("evidence lists usernames and agents", func(t *testing.T) {
		got := evalBruteforce(byUsers(5), nil, 10, t0)
		ev := got[0].Evidence
		if len(ev["usernames"].([]string)) != 5 || ev["userAgents"].([]string)[0] != "sipvicious" {
			t.Fatalf("evidence = %v", ev)
		}
	})
	t.Run("valkey", func(t *testing.T) {
		env, live := testEnv(t)
		env.VK = valkeyClient(t)
		ls := livestate.New(env.VK)
		for i := range 5 {
			for _, a := range attempts(1, time.Minute, func(a *livestate.RegisterAttempt) { a.Device = fmt.Sprintf("u%d", i) }) {
				if err := ls.RecordRegisterAttempt(context.Background(), a); err != nil {
					t.Fatal(err)
				}
			}
		}
		live.fails = []livestate.AuthFailure{{IP: "10.0.0.77", Failures: 10}}
		got, err := authBruteforce(context.Background(), NewRun(env))
		if err != nil {
			t.Fatal(err)
		}
		wantIDs(t, got, "auth_bruteforce:10.0.0.9/critical", "auth_bruteforce:10.0.0.77/critical")
	})
}

func testTrunkDown(t *testing.T) {
	down := func(age ...time.Duration) []Sample[trunkState] {
		var out []Sample[trunkState]
		for _, a := range age {
			out = append(out, Sample[trunkState]{At: t0.Add(-a), V: trunkState{Down: true, Reason: "failed"}})
		}
		return out
	}
	recovered := append(down(3*time.Minute, 2*time.Minute), Sample[trunkState]{At: t0, V: trunkState{}})
	for _, tc := range []struct {
		name string
		ss   []Sample[trunkState]
		want bool
	}{
		{"down 1m59s", down(119*time.Second, 0), false},
		{"down 2m", down(2*time.Minute, time.Minute, 0), true},
		{"down 10m", down(10*time.Minute, 5*time.Minute, 0), true},
		{"recovered", recovered, false},
		{"no samples", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := evalTrunkDown("carrier", tc.ss, t0)
			if ok != tc.want {
				t.Fatalf("candidate = %v, want %v", ok, tc.want)
			}
		})
	}
	t.Run("states", func(t *testing.T) {
		reg := func(state string) livestate.TrunkStatus {
			return livestate.TrunkStatus{Registration: &livestate.TrunkRegistration{State: state}}
		}
		for _, tc := range []struct {
			mode string
			st   livestate.TrunkStatus
			down bool
		}{
			{"registration", reg("registered"), false},
			{"registration", reg("failed"), true},
			{"registration", reg("registering"), true},
			{"registration", livestate.TrunkStatus{}, true},
			{"ip", livestate.TrunkStatus{Destinations: []livestate.DestinationHealth{{Up: false}, {Up: true}}}, false},
			{"ip", livestate.TrunkStatus{Destinations: []livestate.DestinationHealth{{Up: false}}}, true},
			{"ip", livestate.TrunkStatus{}, false},
		} {
			if got := trunkIsDown(tc.mode, tc.st).Down; got != tc.down {
				t.Errorf("%s %+v: down = %v, want %v", tc.mode, tc.st, got, tc.down)
			}
		}
	})
	for _, tc := range []struct {
		name  string
		first time.Duration // the oldest down sample, before this run's
		want  []string
	}{
		{"postgres: down 1m59s", 119 * time.Second, nil},
		{"postgres: down 2m", 2 * time.Minute, []string{"trunk_down:carrier/critical"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, live := testEnv(t)
			id := addTrunk(t, env.DB, "carrier", "registration", 0, true)
			addTrunk(t, env.DB, "off", "registration", 0, false)
			live.trunks[id] = livestate.TrunkStatus{Registration: &livestate.TrunkRegistration{State: "failed"}}
			exec(t, env.DB, `INSERT INTO ai_samples (kind, subject, at, value) VALUES ('trunk_state', 'carrier', $1, '{"down":true}')`, t0.Add(-tc.first))
			got, err := trunkDown(context.Background(), NewRun(env))
			if err != nil {
				t.Fatal(err)
			}
			wantIDs(t, got, tc.want...)
		})
	}
}

func testASRDrop(t *testing.T) {
	// attempts adds n CDRs of a trunk, answered of them answered.
	add := func(db *sql.DB, trunk string, n, answered int, at time.Time, status int) {
		for i := range n {
			addCDR(t, db, cdr{id: i, trunk: trunk, start: at.Add(time.Duration(i) * time.Second), answered: i < answered, status: status})
		}
	}
	base := t0.Add(-72 * time.Hour)
	recent := t0.Add(-30 * time.Minute)
	for _, tc := range []struct {
		name       string
		recent     int
		answered   int
		baseN      int
		baseAns    int
		want       []string
		wantStatus any
	}{
		{"20 attempts, 4 of 20 answered vs 80%", 20, 4, 100, 80, []string{"trunk_asr_drop:carrier/warning"}, 503},
		{"just below half: 7 of 20 vs 80%", 20, 7, 100, 80, []string{"trunk_asr_drop:carrier/warning"}, 503},
		{"exactly half: 8 of 20 vs 80%", 20, 8, 100, 80, nil, nil},
		{"19 attempts", 19, 0, 100, 80, nil, nil},
		{"no baseline", 20, 0, 0, 0, nil, nil},
		{"baseline never answered", 20, 0, 50, 0, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := testEnv(t)
			add(env.DB, "carrier", tc.baseN, tc.baseAns, base, 200)
			// the failures are all 503 here; a dominant status is named
			add(env.DB, "carrier", tc.recent, tc.answered, recent, 503)
			got, err := trunkASRDrop(context.Background(), NewRun(env))
			if err != nil {
				t.Fatal(err)
			}
			wantIDs(t, got, tc.want...)
			if len(got) == 1 && got[0].Evidence["dominantFailureStatus"] != tc.wantStatus {
				t.Fatalf("evidence = %v", got[0].Evidence)
			}
		})
	}
	t.Run("a status under 30 percent is not named", func(t *testing.T) {
		env, _ := testEnv(t)
		add(env.DB, "carrier", 100, 80, base, 200)
		for i, st := range []int{480, 486, 503, 404} { // 4 distinct failure statuses, 25% each
			for j := range 4 {
				addCDR(t, env.DB, cdr{id: 1000 + i*10 + j, trunk: "carrier", start: recent.Add(time.Duration(i*10+j) * time.Second), status: st})
			}
		}
		for i := range 4 {
			addCDR(t, env.DB, cdr{id: 2000 + i, trunk: "carrier", start: recent.Add(time.Duration(100+i) * time.Second), answered: true})
		}
		got, err := trunkASRDrop(context.Background(), NewRun(env))
		if err != nil {
			t.Fatal(err)
		}
		wantIDs(t, got, "trunk_asr_drop:carrier/warning")
		if _, named := got[0].Evidence["dominantFailureStatus"]; named {
			t.Fatalf("evidence names a status: %v", got[0].Evidence)
		}
	})
	// the failure share named: 6 of 20 failures is 30%, 6 of 21 is not.
	for _, tc := range []struct {
		name  string
		other int
		named bool
	}{{"exactly 30 percent is named", 14, true}, {"just under 30 percent is not", 15, false}} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := testEnv(t)
			add(env.DB, "carrier", 100, 80, base, 200)
			for i := range 6 {
				addCDR(t, env.DB, cdr{id: 3000 + i, trunk: "carrier", start: recent.Add(time.Duration(i) * time.Second), status: 503})
			}
			for i := range tc.other { // distinct statuses so none out-counts the 503s
				addCDR(t, env.DB, cdr{id: 4000 + i, trunk: "carrier", start: recent.Add(time.Duration(10+i) * time.Second), status: 400 + i})
			}
			got, err := trunkASRDrop(context.Background(), NewRun(env))
			if err != nil || len(got) != 1 {
				t.Fatalf("got %v, %v", got, err)
			}
			if _, named := got[0].Evidence["dominantFailureStatus"]; named != tc.named {
				t.Fatalf("evidence = %v, want named %v", got[0].Evidence, tc.named)
			}
		})
	}
	t.Run("empty CDRs report nothing", func(t *testing.T) {
		env, _ := testEnv(t)
		got, err := trunkASRDrop(context.Background(), NewRun(env))
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
}
