package detect

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
)

// latencyLimit is how long any one detector may take (spec S-19).
const latencyLimit = 2 * time.Second

// TestDetectorLatency (HELLO_BENCH=1) fails if any detector takes 2 s or
// more over 1 000 000 CDRs and 10 000 devices. The detectors that read the
// register-attempt keys need HELLO_TEST_VALKEY_ADDR; without it they are
// not measured.
func TestDetectorLatency(t *testing.T) {
	if os.Getenv("HELLO_BENCH") != "1" {
		t.Skip("HELLO_BENCH=1 not set")
	}
	env, live := testEnv(t)
	db := env.DB
	exec(t, db, `INSERT INTO extensions (number, name) SELECT (1000 + g)::text, 'e' || g FROM generate_series(1, 10000) g`)
	exec(t, db, `INSERT INTO devices (extension_id, sip_username, realm, ha1_md5, ha1_sha256, enabled)
		SELECT e.id, 'dev' || e.number, 'hello.test', 'x', 'x', e.id % 20 <> 0 FROM extensions e`)
	exec(t, db, `INSERT INTO trunks (name, mode, max_calls) SELECT 'trunk' || g, 'ip', 30 FROM generate_series(1, 20) g`)
	exec(t, db, `INSERT INTO ring_groups (name, strategy) SELECT 'rg' || g, 'ring-all' FROM generate_series(1, 200) g`)
	exec(t, db, `INSERT INTO ring_group_members (group_id, extension_id, position)
		SELECT g.id, e.id, (row_number() OVER (PARTITION BY g.id ORDER BY e.id))::int FROM ring_groups g
		JOIN extensions e ON e.id % 200 = g.id % 200 AND e.id <= 4000`)
	exec(t, db, `INSERT INTO cdrs (correlation_id, sip_call_id, source, destination, start_time, answer_time, end_time,
		duration_ms, billable_ms, sip_node, final_status, termination_side, direction, trunk_name, rtp_packets, rtp_lost, rtp_jitter_ms)
		SELECT 'c' || g, 'c' || g, '1001', '0500', s.t, CASE WHEN g % 5 <> 0 THEN s.t + interval '2 seconds' END, s.t + interval '30 seconds',
		       30000, 28000, 'sip-' || (g % 5), CASE WHEN g % 5 <> 0 THEN 200 ELSE 503 END, 'caller', 'outbound', 'trunk' || (1 + g % 20),
		       CASE WHEN g % 3 <> 0 THEN 1000 + g % 50 END, CASE WHEN g % 3 <> 0 THEN g % 7 END, CASE WHEN g % 3 <> 0 THEN (g % 40) END
		FROM generate_series(1, 1000000) g, LATERAL (SELECT $1::timestamptz - (g * interval '0.6 seconds') AS t) s`, t0)
	exec(t, db, `ANALYZE`)
	for i := range 20 {
		live.trunks[int64(i+1)] = livestate.TrunkStatus{ActiveCalls: 3}
	}
	for i := range 10000 {
		live.bindings = append(live.bindings, livestate.Binding{Device: fmt.Sprintf("dev%d", 1001+i)})
	}
	if os.Getenv("HELLO_TEST_VALKEY_ADDR") != "" {
		env.VK = valkeyClient(t)
		ls := livestate.New(env.VK)
		for i := range 2000 {
			for _, a := range attempts(5, time.Minute, func(a *livestate.RegisterAttempt) { a.Device = fmt.Sprintf("dev%d", 1001+i) }) {
				if err := ls.RecordRegisterAttempt(context.Background(), a); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, d := range Detectors() {
		t.Run(d.Name, func(t *testing.T) {
			r := NewRun(env)
			start := time.Now()
			cs, err := d.Run(context.Background(), r)
			took := time.Since(start)
			if err != nil {
				if env.VK == nil && (d.Name == "reg_failures" || d.Name == "auth_bruteforce") {
					t.Skip("needs Valkey")
				}
				t.Fatal(err)
			}
			t.Logf("%s: %v, %d candidates", d.Name, took, len(cs))
			if took >= latencyLimit {
				t.Fatalf("%s took %v, the limit is %v", d.Name, took, latencyLimit)
			}
		})
	}
}
