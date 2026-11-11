package sip

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/rtcpxr"
	"github.com/emiago/sipgo/sip"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeReports struct {
	mu   sync.Mutex
	got  []rtcpxr.Report
	devs []string
	down bool
}

func (f *fakeReports) RecordRTCPXR(_ context.Context, device, ext string, r rtcpxr.Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return context.DeadlineExceeded
	}
	f.got = append(f.got, r)
	f.devs = append(f.devs, device+"/"+ext)
	return nil
}

func (f *fakeReports) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func withReports(q QualityReports) pbxOpt {
	return func(_ *Config, d *Deps) { d.QualityReports = q }
}

const xrBody = "VQSessionReport: CallTerm\r\nLocalMetrics:\r\nCallID: leg-a@phone\r\n" +
	"PacketLoss: NLR=1.0 JDR=0.2\r\nDelay: RTD=30 IAJ=6\r\nQualityEst: MOSLQ=4.1 MOSCQ=3.9\r\n" +
	"DialogID: leg-a@phone;to-tag=t;from-tag=f\r\n"

func (p *phone) publishReq(event, ct, body string) *sip.Request {
	req := sip.NewRequest(sip.PUBLISH, sip.Uri{Scheme: "sip", User: "collector", Host: testDomain})
	req.AppendHeader(&sip.FromHeader{Address: p.aorURI(), Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}}})
	req.AppendHeader(&sip.ToHeader{Address: p.aorURI()})
	req.AppendHeader(sip.NewHeader("Event", event))
	if ct != "" {
		req.AppendHeader(sip.NewHeader("Content-Type", ct))
	}
	req.SetBody([]byte(body))
	req.SetDestination(p.pbx)
	return req
}

// TestRTCPXRCollector fails if a vq-rtcpxr PUBLISH from an authenticated
// phone is not stored (under the device, with the dialog's Call-ID and the
// phone's figures), if an unauthenticated one is stored, or if a malformed,
// wrongly typed or other-event one is stored or answered 200.
func TestRTCPXRCollector(t *testing.T) {
	q := &fakeReports{}
	pbx := startPBX(t, msgDevices(), withReports(q))
	a := newPhone(t, pbx, "a1", "pa")

	res := a.authDo(a.publishReq("vq-rtcpxr", rtcpxr.ContentType, xrBody), AlgSHA256, a.pass)
	if res.StatusCode != 200 {
		t.Fatalf("report: %d %s", res.StatusCode, res.Reason)
	}
	if q.count() != 1 {
		t.Fatalf("stored %d reports", q.count())
	}
	r := q.got[0]
	if q.devs[0] != "a1/100" || r.CallID != "leg-a@phone" || !r.Final || *r.MOSLQ != 4.1 || *r.MOSCQ != 3.9 || *r.JitterMs != 6 || *r.LossPct != 1.0 {
		t.Fatalf("stored %v %+v", q.devs, r)
	}
	if n := testutil.ToFloat64(pbx.m.RTCPXR.Reports.WithLabelValues("stored")); n != 1 {
		t.Fatalf("stored counter = %v", n)
	}

	for name, c := range map[string]struct {
		event, ct, body string
		want            int
	}{
		"malformed":  {"vq-rtcpxr", rtcpxr.ContentType, "not a report", 400},
		"no metrics": {"vq-rtcpxr", rtcpxr.ContentType, "VQSessionReport: CallTerm\r\nLocalMetrics:\r\nCallID: x\r\n", 400},
		"bad range":  {"vq-rtcpxr", rtcpxr.ContentType, strings.Replace(xrBody, "NLR=1.0", "NLR=400", 1), 400},
		"wrong type": {"vq-rtcpxr", "text/plain", xrBody, 415},
		"no type":    {"vq-rtcpxr", "", xrBody, 415},
		"other evt":  {"presence", rtcpxr.ContentType, xrBody, 403}, // routed to presence: not the own feature extension
	} {
		res := a.authDo(a.publishReq(c.event, c.ct, c.body), AlgSHA256, a.pass)
		if res.StatusCode != c.want {
			t.Errorf("%s: %d %s, want %d", name, res.StatusCode, res.Reason, c.want)
		}
	}
	// Wrong password: challenged, never stored.
	bad := a.authDo(a.publishReq("vq-rtcpxr", rtcpxr.ContentType, xrBody), AlgSHA256, "wrong")
	if bad.StatusCode == 200 {
		t.Fatal("a report with a wrong password was accepted")
	}
	if q.count() != 1 {
		t.Fatalf("stored %d reports, want only the first", q.count())
	}
}

// TestRTCPXRUnavailableAndRate fails if a store outage is answered 200, or
// a device can publish without bound.
func TestRTCPXRUnavailableAndRate(t *testing.T) {
	q := &fakeReports{down: true}
	pbx := startPBX(t, msgDevices(), withReports(q))
	a := newPhone(t, pbx, "a1", "pa")
	if res := a.authDo(a.publishReq("vq-rtcpxr", rtcpxr.ContentType, xrBody), AlgSHA256, a.pass); res.StatusCode != 503 {
		t.Fatalf("store down: %d", res.StatusCode)
	}
	var w reportWindow
	now := time.Now()
	for i := 0; i < rtcpxrPerMinute; i++ {
		if !w.allow("a1", now) {
			t.Fatalf("report %d refused within the limit", i)
		}
	}
	if w.allow("a1", now) {
		t.Fatal("report over the limit allowed")
	}
	if !w.allow("b1", now) {
		t.Fatal("limit shared between devices")
	}
	if !w.allow("a1", now.Add(time.Minute)) {
		t.Fatal("limit not reset after the window")
	}
}
