package store

import (
	"context"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/rtcpxr"
)

func ptr(v float64) *float64 { return &v }

// TestRTCPXRStoredOnMatchingCDR fails if a report is not returned with the
// CDR whose caller leg or callee leg it names, if a report of another call
// is attached, or if an interval report replaces the call-end report.
// Needs HELLO_TEST_DATABASE_URL.
func TestRTCPXRStoredOnMatchingCDR(t *testing.T) {
	st, db, _ := provStore(t)
	ctx := context.Background()
	var mine, other int64
	for _, c := range []struct {
		id   *int64
		corr string
		call string
		peer string
	}{{&mine, "c1", "leg-a", "leg-b"}, {&other, "c2", "leg-x", "leg-y"}} {
		if err := db.QueryRowContext(ctx, `INSERT INTO cdrs (correlation_id, sip_call_id, peer_call_id, source, destination, start_time, end_time)
			VALUES ($1, $2, $3, '100', '200', now(), now()) RETURNING id`, c.corr, c.call, c.peer).Scan(c.id); err != nil {
			t.Fatal(err)
		}
	}
	// The callee's phone reports before the CDR could be read, and again
	// with an interval report that must not replace its call-end report.
	final := rtcpxr.Report{CallID: "leg-b", Final: true, MOSLQ: ptr(4.1), LossPct: ptr(0.5)}
	if err := st.RecordRTCPXR(ctx, "dev200", "200", final); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRTCPXR(ctx, "dev200", "200", rtcpxr.Report{CallID: "leg-b", MOSLQ: ptr(2.0)}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRTCPXR(ctx, "dev100", "100", rtcpxr.Report{CallID: "leg-a", Final: true, JitterMs: ptr(9)}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRTCPXR(ctx, "dev999", "999", rtcpxr.Report{CallID: "leg-unrelated", Final: true, MOSLQ: ptr(1)}); err != nil {
		t.Fatal(err)
	}
	c, _, err := st.GetCDR(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.RTCPXR) != 2 {
		t.Fatalf("reports on the cdr = %+v, want the two of its legs", c.RTCPXR)
	}
	byDev := map[string]RTCPXRQuality{}
	for _, q := range c.RTCPXR {
		byDev[q.Device] = q
	}
	if q := byDev["dev200"]; q.MOSLQ == nil || *q.MOSLQ != 4.1 || !q.Final || q.LossPct == nil || *q.LossPct != 0.5 || q.Extension != "200" {
		t.Fatalf("callee report = %+v", q)
	}
	if q := byDev["dev100"]; q.JitterMs == nil || *q.JitterMs != 9 || q.MOSLQ != nil {
		t.Fatalf("caller report = %+v", q)
	}
	o, _, err := st.GetCDR(ctx, other)
	if err != nil || len(o.RTCPXR) != 0 {
		t.Fatalf("other cdr reports = %+v %v", o.RTCPXR, err)
	}
	// Retention.
	if _, err := db.ExecContext(ctx, `UPDATE cdr_rtcpxr SET reported_at = $1`, time.Now().Add(-RTCPXRRetention-time.Hour)); err != nil {
		t.Fatal(err)
	}
}
