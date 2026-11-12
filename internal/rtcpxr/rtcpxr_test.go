package rtcpxr

import (
	"errors"
	"strings"
	"testing"
)

const callTerm = "VQSessionReport: CallTerm\r\n" +
	"LocalMetrics:\r\n" +
	"Timestamps: START=2026-11-09T10:00:00Z STOP=2026-11-09T10:02:00Z\r\n" +
	"SessionDesc: PT=0 PD=PCMU SR=8000 FD=20 FPP=1 PPS=50 PLC=3\r\n" +
	"CallID: leg-a@phone\r\n" +
	"PacketLoss: NLR=1.5 JDR=0.5\r\n" +
	"Delay: RTD=40 ESD=80 IAJ=7\r\n" +
	"QualityEst: RLQ=88 RCQ=85 MOSLQ=4.2 MOSCQ=4.0 QoEEstAlg=P.564\r\n" +
	"DialogID: dlg-1@phone;to-tag=t1;from-tag=f1\r\n" +
	"RemoteMetrics:\r\n" +
	"PacketLoss: NLR=99 JDR=99\r\n" +
	"QualityEst: MOSLQ=1.0 MOSCQ=1.0\r\n"

func TestParseCallTerm(t *testing.T) {
	r, err := Parse([]byte(callTerm))
	if err != nil {
		t.Fatal(err)
	}
	if r.CallID != "dlg-1@phone" || !r.Final {
		t.Fatalf("id/final: %+v", r)
	}
	for name, p := range map[string]struct {
		got  *float64
		want float64
	}{
		"moslq": {r.MOSLQ, 4.2}, "moscq": {r.MOSCQ, 4.0}, "jitter": {r.JitterMs, 7},
		"loss": {r.LossPct, 1.5}, "discard": {r.DiscardPct, 0.5}, "rtt": {r.RoundTripMs, 40},
	} {
		if p.got == nil || *p.got != p.want {
			t.Errorf("%s: got %v want %v (remote metrics must not leak in)", name, p.got, p.want)
		}
	}
}

func TestParseCallIDFallbackAndInterval(t *testing.T) {
	r, err := Parse([]byte("VQSessionReport\nLocalMetrics:\nCallID: only@phone\nQualityEst: MOSLQ=3.5\n"))
	if err != nil || r.CallID != "only@phone" || r.Final || r.MOSCQ != nil || *r.MOSLQ != 3.5 {
		t.Fatalf("%+v %v", r, err)
	}
}

// TestParseRejectsMalformed fails if a malformed or out-of-range report is
// accepted.
func TestParseRejectsMalformed(t *testing.T) {
	big := "VQSessionReport: CallTerm\nLocalMetrics:\nCallID: a\nQualityEst: MOSLQ=4\n" + strings.Repeat("X: y\n", MaxBody)
	for name, body := range map[string]string{
		"empty":          "",
		"no header":      "LocalMetrics:\nCallID: a\nQualityEst: MOSLQ=4\n",
		"no dialog":      "VQSessionReport: CallTerm\nLocalMetrics:\nQualityEst: MOSLQ=4\n",
		"no metrics":     "VQSessionReport: CallTerm\nLocalMetrics:\nCallID: a\n",
		"remote only":    "VQSessionReport: CallTerm\nLocalMetrics:\nCallID: a\nRemoteMetrics:\nQualityEst: MOSLQ=4\n",
		"mos too high":   "VQSessionReport: CallTerm\nLocalMetrics:\nCallID: a\nQualityEst: MOSLQ=9.9\n",
		"loss negative":  "VQSessionReport: CallTerm\nLocalMetrics:\nCallID: a\nPacketLoss: NLR=-1\n",
		"loss over 100":  "VQSessionReport: CallTerm\nLocalMetrics:\nCallID: a\nPacketLoss: NLR=101\n",
		"not a number":   "VQSessionReport: CallTerm\nLocalMetrics:\nCallID: a\nDelay: IAJ=NaN\n",
		"garbage":        "hello world",
		"id with spaces": "VQSessionReport: CallTerm\nLocalMetrics:\nCallID: a b\nQualityEst: MOSLQ=4\n",
		"too large":      big,
	} {
		if _, err := Parse([]byte(body)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
}
