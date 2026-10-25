package sip

import (
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// TestRecordInfoParse fails if the Record INFO of a phone's Record key is
// not read in the forms the vendors send: Snom "Record: on" / "Record: off"
// (recording_mechanism SIP), Fanvil "Record:on" / "Record:off" (SIP1 Record
// Start / Stop), as a Record header or a body line in any content type, and
// if a DTMF INFO or other text is taken for one.
func TestRecordInfoParse(t *testing.T) {
	info := func(header, ctype, body string) *sip.Request {
		req := sip.NewRequest(sip.INFO, sip.Uri{User: "100", Host: "hello.test"})
		if header != "" {
			req.AppendHeader(sip.NewHeader("Record", header))
		}
		if ctype != "" {
			req.AppendHeader(sip.NewHeader("Content-Type", ctype))
		}
		req.SetBody([]byte(body))
		return req
	}
	for _, tc := range []struct {
		name         string
		req          *sip.Request
		wantOn, want bool
	}{
		{"snom body on", info("", "text/plain", "Record: on\r\n"), true, true},
		{"snom body off", info("", "text/plain", "Record: off"), false, true},
		{"snom header on", info("on", "", ""), true, true},
		{"snom header off", info("off", "", ""), false, true},
		{"fanvil body on", info("", "application/x-fanvil", "Record:on"), true, true},
		{"fanvil body off", info("", "", "Record:off\r\n"), false, true},
		{"case and space", info("", "text/plain", "  RECORD :  ON  "), true, true},
		{"after another line", info("", "text/plain", "Foo: bar\r\nRecord: on\r\n"), true, true},
		{"unknown word", info("", "text/plain", "Record: pause"), false, false},
		{"dtmf relay", info("", "application/dtmf-relay", "Signal=1\r\nDuration=100\r\n"), false, false},
		{"empty", info("", "", ""), false, false},
		{"a longer word", info("", "text/plain", "Recorder: on"), false, false},
	} {
		on, ok := recordInfo(tc.req)
		if ok != tc.want || (ok && on != tc.wantOn) {
			t.Errorf("%s: recordInfo = %v, %v; want %v, %v", tc.name, on, ok, tc.wantOn, tc.want)
		}
	}
}

// sendInfo sends one in-dialog INFO from the caller with a fresh CSeq.
func sendInfo(t *testing.T, p *phone, dcs *sipgo.DialogClientSession, seq *uint32, header, ctype, body string) {
	t.Helper()
	inv := dcs.InviteRequest
	target := *dcs.InviteRequest.Recipient.Clone()
	if ct := dcs.InviteResponse.Contact(); ct != nil {
		target = *ct.Address.Clone()
	}
	*seq++
	req := sip.NewRequest(sip.INFO, target)
	req.AppendHeader(&sip.FromHeader{Address: *inv.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagOfTest(inv.From())}}})
	req.AppendHeader(&sip.ToHeader{Address: *inv.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: toTagOf(dcs)}}})
	callID := sip.CallIDHeader(inv.CallID().Value())
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: *seq, MethodName: sip.INFO})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	if header != "" {
		req.AppendHeader(sip.NewHeader("Record", header))
	}
	if ctype != "" {
		req.AppendHeader(sip.NewHeader("Content-Type", ctype))
	}
	req.SetBody([]byte(body))
	req.SetTransport("UDP")
	p.do(req)
}

// TestRecordInfoTogglesRecording fails if the Record key's INFO does not
// record through the same path as *1: "on" starts a recording (re-anchoring
// when needed), a repeated "on" and an "off" with nothing running change
// nothing, "off" stops and stores it while the call stays up. Mutation:
// dropping the recordInfo branch from handleInfo leaves no recording row.
func TestRecordInfoTogglesRecording(t *testing.T) {
	objs := &fakeObjects{}
	putAnnouncements(objs)
	recs := &fakeRecordings{}
	feats := func(s *snapshot.Snapshot) {
		s.WithAnnouncements(map[string]string{"recording-notice": "ann/recording-notice.wav"})
	}
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), withMedia(false),
		withFeatures(feats), withVoicemail(newVMStore(), objs))
	pbx.srv.deps.Recordings = recs
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	c := newPhone(t, pbx, "c1", "pc")
	c.register(t)
	c.setCallee(answerAfter(nil))

	r := waitCall(t, dial(t.Context(), a, "300"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	invC := waitReq(t, c.invites, "INVITE to c")
	waitReq(t, c.acks, "ACK to c")
	feedA := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feedC := startRTPFeed(t, invC.Body())
	seq := r.dcs.InviteRequest.CSeq().SeqNo + 10

	// "off" with nothing recording stores nothing.
	sendInfo(t, a, r.dcs, &seq, "", "text/plain", "Record:off")
	feedA.silence(0.5)
	if rows := recs.snapshot(); len(rows) != 0 {
		t.Fatalf("an off with no recording stored %+v", rows)
	}
	// Snom's form starts it; a second "on" does not stop it again.
	sendInfo(t, a, r.dcs, &seq, "", "text/plain", "Record: on\r\n")
	feedA.silence(1)
	feedC.silence(1)
	sendInfo(t, a, r.dcs, &seq, "on", "", "")
	feedA.silence(0.5)
	if rows := recs.snapshot(); len(rows) != 0 {
		t.Fatalf("a repeated on stopped the recording: %+v", rows)
	}
	// Fanvil's form stops and stores it.
	sendInfo(t, a, r.dcs, &seq, "", "text/plain", "Record:off")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rows := recs.snapshot(); len(rows) == 1 {
			if rows[0].by != "dtmf" || !strings.HasPrefix(rows[0].object, "rec/") {
				t.Fatalf("recording row = %+v", rows[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the Record INFO recording was not stored")
		}
		time.Sleep(20 * time.Millisecond)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}
