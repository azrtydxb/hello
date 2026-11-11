package sip

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// TestVoicemailHangupSendsByeWithoutPanic fails if ending a voicemail call
// panics or loses its BYE. The 200 is written through the raw transaction,
// so the dialog session's InviteResponse is nil and sipgo's DialogServerSession.Bye
// dereferences it: contain() swallowed the panic and the caller was never sent
// a BYE.
func TestVoicemailHangupSendsByeWithoutPanic(t *testing.T) {
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
	}
	logs := &logCapture{}
	withLog := func(_ *Config, d *Deps) { d.Log = slog.New(logs) }
	pbx := startPBX(t, callerDevices(), shortRing(200*time.Millisecond), withFeatures(feats), withVoicemail(vm, &fakeObjects{}), withLog)
	anchored(pbx)
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	b.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("voicemail call: %v", r.err)
	}
	feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feed.silence(2.5)
	feed.digit('#')
	waitReq(t, a.byes, "BYE to the caller after the voicemail")
	for _, m := range logs.messages() {
		if strings.Contains(m, "recovered panic") {
			t.Fatalf("hangup panicked: %v", logs.messages())
		}
	}
}

// TestByeAWithoutRecordedAnswer fails if byeA panics on a dialog session that
// never recorded a response (sipgo's DialogServerSession.Bye dereferences
// InviteResponse). A call with no way to send the BYE must say so in the log.
func TestByeAWithoutRecordedAnswer(t *testing.T) {
	logs := &logCapture{}
	c := &call{s: &Server{log: slog.New(logs)}, id: "corr-1",
		dss: &sipgo.DialogServerSession{Dialog: sipgo.Dialog{InviteRequest: &sip.Request{}}}}
	c.byeA() // must not panic
	msgs := logs.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "no BYE sent to caller") {
		t.Fatalf("log = %v; want the lost BYE reported", msgs)
	}
}
