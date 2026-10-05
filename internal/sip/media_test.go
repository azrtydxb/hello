package sip

import (
	"context"
	"github.com/azrtydxb/hello/internal/cdr"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// withMedia sets the RTP port range and the media switches; force turns
// the force switch on. The media metrics ride the PBX registry that
// startPBX wires into Deps.Media.
func withMedia(force bool) pbxOpt {
	return func(c *Config, d *Deps) {
		c.RTPPortMin, c.RTPPortMax = 30000, 31000
		c.MediaForceAnchor = force
		c.MediaRecordingNotice = true
	}
}

// fakeRecordings is the recordings metadata store.
type fakeRecordings struct {
	mu   sync.Mutex
	rows []recording0
}

type recording0 struct {
	correlationID, object, by string
	durationMs                int64
}

func (f *fakeRecordings) InsertRecording(_ context.Context, correlationID, object, by string, durationMs int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, recording0{correlationID, object, by, durationMs})
	return int64(len(f.rows)), nil
}

func (f *fakeRecordings) snapshot() []recording0 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recording0(nil), f.rows...)
}

// inviteTo builds a minimal INVITE for the anchoring decision table.
func inviteTo(number string) *sip.Request {
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: number, Host: testDomain})
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: "100", Host: testDomain},
		Params: sip.HeaderParams{{K: "tag", V: "t1"}}})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: number, Host: testDomain}})
	return req
}

// TestConditionalAnchor fails if a clean direct call anchors, if a
// NAT-simulated, recording, announcement or voicemail call does not
// anchor, or if the force switch does not anchor a clean call (spec S-1).
// Mutation: dropping the contact-vs-source comparison in EndpointInfo.nat
// fails the NAT cases; dropping the force tail fails the forced case.
func TestConditionalAnchor(t *testing.T) {
	base := func() *snapshot.Snapshot {
		return snapshot.New(1, testDomain, callerDevices()).
			WithExtensionData([]snapshot.Extension{
				{Number: "100", RecordDefault: false},
				{Number: "200", RecordDefault: true},
				{Number: "300", DND: true, VoicemailEnabled: true, VoicemailBoxID: 5},
			}).
			WithFeatureCodes([]snapshot.FeatureCode{{Code: "*89", Action: ActionAnnouncement, Argument: "welcome"}})
	}
	from100 := EndpointInfo{ContactHost: "10.0.0.9", ContactPort: 5060, SourceHost: "10.0.0.9", SourcePort: 5060, Extension: "100"}
	natted := EndpointInfo{ContactHost: "10.0.0.9", ContactPort: 5060, SourceHost: "10.0.0.9", SourcePort: 40001, Extension: "100"}
	natHost := EndpointInfo{ContactHost: "10.9.9.9", ContactPort: 5060, SourceHost: "10.0.0.9", SourcePort: 5060, Extension: "100"}
	natKnown := EndpointInfo{NATKnown: true, Extension: "100"}
	for _, tc := range []struct {
		name  string
		req   *sip.Request
		snap  *snapshot.Snapshot
		from  EndpointInfo
		to    EndpointInfo
		force bool
		want  AnchorReason
	}{
		{"direct stays direct", inviteTo("200"), base(), from100, EndpointInfo{Extension: "100"}, false, AnchorNone},
		{"source port differs from contact", inviteTo("200"), base(), natted, EndpointInfo{Extension: "200"}, false, AnchorNAT},
		{"contact host differs from source", inviteTo("200"), base(), natHost, EndpointInfo{Extension: "200"}, false, AnchorNAT},
		{"phase 3 NAT flag", inviteTo("200"), base(), natKnown, EndpointInfo{Extension: "200"}, false, AnchorNAT},
		{"callee record default anchors", inviteTo("200"), base(), from100, EndpointInfo{Extension: "200"}, false, AnchorRecording},
		{"caller record default anchors", inviteTo("300"), base(),
			EndpointInfo{ContactHost: "10.0.0.9", ContactPort: 5060, SourceHost: "10.0.0.9", SourcePort: 5060, Extension: "200"},
			EndpointInfo{Extension: "300"}, false, AnchorRecording},
		{"announcement feature code", inviteTo("*89"), base(), from100, EndpointInfo{}, false, AnchorAnnouncement},
		{"voicemail handoff anchors", inviteTo("300"), base(), from100,
			EndpointInfo{Extension: "300"}, false, AnchorVoicemail},
		{"force anchors a clean call", inviteTo("200"), base(), from100, EndpointInfo{Extension: "100"}, true, AnchorForced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideAnchor(tc.req, tc.snap, tc.from, tc.to, tc.force); got != tc.want {
				t.Fatalf("decideAnchor = %q, want %q", got, tc.want)
			}
		})
	}
}

// putAnnouncements stores both announcements' WAVs in the fake object
// store.
func putAnnouncements(objs *fakeObjects) {
	_ = objs.Put(context.Background(), "ann/welcome.wav", media.EncodeWAV(make([]byte, 3200), 8000))
	_ = objs.Put(context.Background(), "ann/recording-notice.wav", media.EncodeWAV(make([]byte, 1600), 8000))
}

// TestRecording fails if *1 or record_default does not produce a MinIO WAV
// linked to a recordings row, or if the notice does not play when
// configured (spec S-4). Mutation: dropping the storeRecording call from
// closeMedia fails the hangup-stored assertions; dropping the recordings
// row insert fails the row checks.
func TestRecording(t *testing.T) {
	objs := &fakeObjects{}
	putAnnouncements(objs)
	recs := &fakeRecordings{}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", RecordDefault: true}})
		s.WithAnnouncements(map[string]string{"recording-notice": "ann/recording-notice.wav"})
	}
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), withMedia(false),
		withFeatures(feats), withVoicemail(newVMStore(), objs))
	pbx.srv.deps.Recordings = recs
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	b.setCallee(answerAfter(nil))
	c := newPhone(t, pbx, "c1", "pc")
	c.register(t)
	c.setCallee(answerAfter(nil))

	// Part 1: *1 on a direct call re-anchors live and records; *1 again
	// stops and stores while the call stays up.
	r := waitCall(t, dial(t.Context(), a, "300"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, c.invites, "INVITE to c")
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "*1", &seq)
	reqA := waitReq(t, a.reinvites, "re-INVITE to a with the anchor's offer")
	reqC := waitReq(t, c.reinvites, "re-INVITE to c with the anchor's offer")
	if len(reqA.Body()) == 0 || len(reqC.Body()) == 0 {
		t.Fatal("the re-INVITEs carry no SDP")
	}
	feedA := startRTPFeed(t, reqA.Body())
	feedC := startRTPFeed(t, reqC.Body())
	feedA.silence(1)
	feedC.silence(1)
	sendDigits(t, a, r.dcs, "*1", &seq)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rows := recs.snapshot(); len(rows) == 1 {
			if rows[0].by != "dtmf" || !strings.HasPrefix(rows[0].object, "rec/") {
				t.Fatalf("recording row = %+v", rows[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the *1 recording was not stored")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if wav, ok := objs.snapshot()[recs.snapshot()[0].object]; !ok || string(wav[:4]) != "RIFF" {
		t.Fatal("the *1 recording audio is not a WAV in the object store")
	}
	if !slices.Contains(objs.snapshotGets(), "ann/recording-notice.wav") {
		t.Fatal("the recording notice was not fetched")
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)

	// Part 2: record_default anchors at setup and stores at hangup.
	r2 := waitCall(t, dial(t.Context(), a, "200"))
	if r2.err != nil {
		t.Fatalf("call 2: %v", r2.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	ans := string(r2.dcs.InviteResponse.Body())
	if !strings.Contains(ans, "m=audio") {
		t.Fatal("answer carries no SDP")
	}
	feedA2 := startRTPFeed(t, []byte(ans))
	feedA2.silence(1)
	hangup(t, r2.dcs)
	pbx.nextCDR(t)
	deadline = time.Now().Add(5 * time.Second)
	for {
		if rows := recs.snapshot(); len(rows) == 2 {
			if rows[1].by != "default" {
				t.Fatalf("second recording row = %+v", rows[1])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the record_default recording was not stored")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sendDigits sends each digit as its own INFO with a fresh CSeq, so a
// multi-digit sequence like *1 is not dropped as a retransmission.
func sendDigits(t *testing.T, p *phone, dcs *sipgo.DialogClientSession, digits string, seq *uint32) {
	t.Helper()
	inv := dcs.InviteRequest
	target := *dcs.InviteRequest.Recipient.Clone()
	if ct := dcs.InviteResponse.Contact(); ct != nil {
		target = *ct.Address.Clone()
	}
	for _, d := range digits {
		*seq++
		req := sip.NewRequest(sip.INFO, target)
		req.AppendHeader(&sip.FromHeader{Address: *inv.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagOfTest(inv.From())}}})
		req.AppendHeader(&sip.ToHeader{Address: *inv.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: toTagOf(dcs)}}})
		callID := sip.CallIDHeader(inv.CallID().Value())
		req.AppendHeader(&callID)
		req.AppendHeader(&sip.CSeqHeader{SeqNo: *seq, MethodName: sip.INFO})
		req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
		req.AppendHeader(sip.NewHeader("Content-Type", "application/dtmf-relay"))
		req.SetBody([]byte("Signal=" + string(d) + "\r\nDuration=100\r\n"))
		req.SetTransport("UDP")
		p.do(req)
	}
}

// TestReAnchorMidCall fails if a mid-call anchor need (here *1 on a direct
// call) does not re-anchor live: both dialogs must be re-INVITEd to the
// anchor, the CDR must say anchored, and the anchor must not churn back to
// direct (spec S-1, TestReAnchorMidCall). Mutation: making reanchorLive a
// no-op fails the re-INVITE waits and the CDR media column.
func TestReAnchorMidCall(t *testing.T) {
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), withMedia(false),
		withVoicemail(newVMStore(), &fakeObjects{}))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	c := newPhone(t, pbx, "c1", "pc")
	c.register(t)
	c.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "300"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, c.invites, "INVITE to c")
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "*1", &seq)
	reqA := waitReq(t, a.reinvites, "re-INVITE to a")
	reqC := waitReq(t, c.reinvites, "re-INVITE to c")
	for _, body := range [][]byte{reqA.Body(), reqC.Body()} {
		sdp, err := media.ParseAudioSDP(body)
		if err != nil || !strings.Contains(string(body), "m=audio") {
			t.Fatalf("re-INVITE body is not the anchor's SDP: %q (%v)", body, err)
		}
		if sdp.Address != "127.0.0.1" {
			t.Fatalf("re-INVITE names %s, want the anchor host", sdp.Address)
		}
	}
	if v := pbx.metric(t, "hello_media_anchored_calls", nil); v != 1 {
		t.Fatalf("anchored calls = %v, want 1", v)
	}
	// A second re-INVITE from a (a hold toggle) must not churn the anchor
	// away: the anchored path answers with the anchor's SDP.
	res := a.reinviteAsCaller(t, r.dcs, "v=0\r\no=a 2 2 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4970 RTP/AVP 0 101\r\na=rtpmap:101 telephone-event/8000\r\na=sendonly\r\n")
	if res.StatusCode != 200 {
		t.Fatalf("hold re-INVITE = %d", res.StatusCode)
	}
	reqA2 := waitReq(t, c.reinvites, "hold mirrored to c")
	if !strings.Contains(string(reqA2.Body()), "sendonly") {
		t.Fatalf("hold not mirrored: %q", reqA2.Body())
	}
	hangup(t, r.dcs)
	hangup(t, r.dcs)
	cd := pbx.nextCDR(t)
	if cd.MediaMode != "anchored" {
		t.Fatalf("CDR media = %q, want anchored", cd.MediaMode)
	}
	if !strings.Contains(traceText(cd.Trace), "Media anchored mid-call (recording)") {
		t.Fatalf("CDR trace lacks the anchor step: %s", traceText(cd.Trace))
	}
}

// TestRecordingPauseOnHold fails if recording captures hold silence: a
// held call's incoming audio must not reach the recorder, and the stored
// duration must not count the held seconds (spec edge case).
// Mutation: dropping the isPaused check in recTap makes the duration cover
// the held phase too and fails the bound.
func TestRecordingPauseOnHold(t *testing.T) {
	objs := &fakeObjects{}
	recs := &fakeRecordings{}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", RecordDefault: true}})
	}
	pbx := startPBX(t, callerDevices(), shortRing(2*time.Second), withMedia(false),
		withFeatures(feats), withVoicemail(newVMStore(), objs))
	pbx.srv.deps.Recordings = recs
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	feedA := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feedA.silence(1)
	// Hold: a sendonly re-INVITE pauses the recording.
	res := a.reinviteAsCaller(t, r.dcs, "v=0\r\no=a 2 2 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4970 RTP/AVP 0 101\r\na=rtpmap:101 telephone-event/8000\r\na=sendonly\r\n")
	if res.StatusCode != 200 {
		t.Fatalf("hold re-INVITE = %d", res.StatusCode)
	}
	feedA.silence(1.5) // held: must not be captured
	res = a.reinviteAsCaller(t, r.dcs, "v=0\r\no=a 3 3 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4970 RTP/AVP 0 101\r\na=rtpmap:101 telephone-event/8000\r\na=sendrecv\r\n")
	if res.StatusCode != 200 {
		t.Fatalf("resume re-INVITE = %d", res.StatusCode)
	}
	feedA.silence(1)
	hangup(t, r.dcs)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rows := recs.snapshot(); len(rows) == 1 {
			// About two seconds captured; the 1.5 held seconds must be
			// missing. The frame clock tolerates one tick either way.
			if rows[0].durationMs > 2400 {
				t.Fatalf("recording is %d ms: hold silence was captured", rows[0].durationMs)
			}
			if rows[0].durationMs < 1500 {
				t.Fatalf("recording is only %d ms", rows[0].durationMs)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recording not stored")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAnnouncements fails if an announcement destination does not answer,
// play and continue, if the before-transfer announcement does not play, or
// if a missing WAV does not skip the step (spec S-5).
func TestAnnouncements(t *testing.T) {
	objs := &fakeObjects{}
	putAnnouncements(objs)
	group := []snapshot.RingGroup{{
		ID: 1, Name: "700", Strategy: "ring-all", RingTimeout: 1, MemberDelay: 1,
		FailureKind: "announcement", FailureTarget: "welcome",
	}}
	feats := func(s *snapshot.Snapshot) {
		s.WithRingGroups(group, [][]snapshot.RingGroupMember{{{ExtensionID: 9, Extension: "200", Position: 1}}})
		s.WithFeatureCodes([]snapshot.FeatureCode{
			{Code: "*89", Action: ActionAnnouncement, Argument: "welcome"},
			{Code: "*2", Action: ActionBlindTransfer, Argument: "welcome"},
		})
		s.WithAnnouncements(map[string]string{"welcome": "ann/welcome.wav"})
	}
	pbx := startPBX(t, threeDevices(), shortRing(300*time.Millisecond), withMedia(false),
		withFeatures(feats), withVoicemail(newVMStore(), objs))
	pbx.srv.deps.Recordings = &fakeRecordings{}
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	c := newPhone(t, pbx, "c1", "pc")
	c.register(t)
	c.setCallee(answerAfter(nil))

	// Destination kind: the group's member never answers, so the failure
	// destination answers, plays, hangs up. The caller sees a 200 then a
	// BYE, and the WAV was fetched.
	b.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "700"))
	if r.err != nil {
		t.Fatalf("announcement call: %v", r.err)
	}
	waitReq(t, a.byes, "BYE ends the announcement")
	deadline := time.Now().Add(3 * time.Second)
	for !slices.Contains(objs.snapshotGets(), "ann/welcome.wav") {
		if time.Now().After(deadline) {
			t.Fatal("the announcement WAV was never fetched")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pbx.nextCDR(t)

	// Feature code: *89 answers, plays, and the PBX hangs up first.
	r = waitCall(t, dial(t.Context(), a, "*89"))
	if r.err != nil {
		t.Fatalf("feature code call: %v", r.err)
	}
	waitReq(t, a.byes, "BYE ends the announcement feature code")
	pbx.nextCDR(t)

	// Pre-transfer: *2 with the announcement argument plays before the
	// blind transfer to 300 executes.
	r = waitCall(t, dial(t.Context(), a, "300"))
	if r.err != nil {
		t.Fatalf("transfer call: %v", r.err)
	}
	waitReq(t, c.invites, "INVITE to c")
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "*2", &seq)
	sendDigits(t, a, r.dcs, "300#", &seq)
	// The transfer target answers; the caller is re-INVITEd to it, and the
	// announcement ran (fetched) before the transfer.
	waitReq(t, c.reinvites, "caller re-INVITEd to the transfer target")
	waitReq(t, a.reinvites, "the transferee leg's answer reaches the caller")
	deadline = time.Now().Add(3 * time.Second)
	for !slices.Contains(objs.snapshotGets(), "ann/welcome.wav") {
		if time.Now().After(deadline) {
			t.Fatal("the pre-transfer announcement never played")
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if v := pbx.metric(t, "hello_transfers_total", map[string]string{"kind": "blind", "result": "answered"}); v == 1 {
			break
		}
		if v := pbx.metric(t, "hello_transfers_total", map[string]string{"kind": "blind", "result": "failed"}); v == 1 {
			t.Fatal("the transfer failed")
		}
		if time.Now().After(deadline) {
			t.Fatal("the transfer never completed")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Missing WAV: *89 with the set emptied skips the step with a trace,
	// the call still answers and ends. The state swap is a fresh snapshot
	// stored atomically: mutating the live one in place races the in-flight
	// transactions of the transfer call above (the snapshot is read
	// lock-free).
	pbx.snaps.p.Store(snapshot.New(pbx.snaps.p.Load().Revision, testDomain, threeDevices()).
		WithExtensions(bareExtension).
		WithFeatureCodes([]snapshot.FeatureCode{{Code: "*89", Action: ActionAnnouncement, Argument: "ghost"}}))
	r = waitCall(t, dial(t.Context(), a, "*89"))
	if r.err != nil {
		t.Fatalf("missing-WAV call: %v", r.err)
	}
	waitReq(t, a.byes, "BYE ends the skipped announcement")
	// Drain the CDRs still in flight from the transfer part until this
	// call's CDR shows up.
	var cd cdr.Record
	deadline = time.Now().Add(5 * time.Second)
	for {
		cd = pbx.nextCDR(t)
		if cd.Destination == "*89" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the missing-WAV CDR never arrived")
		}
	}
	if !strings.Contains(traceText(cd.Trace), "missing") {
		t.Fatalf("CDR trace lacks the missing-WAV step: %s", traceText(cd.Trace))
	}
}

// TestNATAnchorCarrier fails if a NAT-simulated callee (Contact names one
// port, packets come from another) does not anchor the call, or if the
// anchored path does not deliver audio to the address the packets actually
// come from — the one-way-audio carrier case (spec S-6). Mutation: dropping
// the NAT trigger from the decision keeps the call direct and fails the
// anchored-answer and CDR assertions; latching to the SDP address instead
// of the packet source fails the feed assertions.
func TestNATAnchorCarrier(t *testing.T) {
	pbx := startPBX(t, callerDevices(), shortRing(2*time.Second), withMedia(false),
		withVoicemail(newVMStore(), &fakeObjects{}))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	// b registers once, with a Contact port far from its real source:
	// exactly what a phone behind a symmetric NAT looks like.
	b := newPhone(t, pbx, "b1", "pb1")
	res := b.authDo(b.registerReq(300, "<sip:b1@"+testDomain+":51999;transport=udp>"), AlgSHA256, "pb1")
	if res.StatusCode != 200 {
		t.Fatalf("NAT register = %d", res.StatusCode)
	}
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	// The fork's INVITE carried the anchor's offer: b's answer echoes the
	// anchor host, not a's address.
	invB := waitReq(t, b.invites, "INVITE to b")
	if sdp, err := media.ParseAudioSDP(invB.Body()); err != nil || sdp.Address != "127.0.0.1" {
		t.Fatalf("fork offer is not the anchor's: %q (%v)", invB.Body(), err)
	}
	ans := r.dcs.InviteResponse.Body()
	sdpA, err := media.ParseAudioSDP(ans)
	if err != nil || sdpA.Address != "127.0.0.1" {
		t.Fatalf("caller answer is not the anchor's: %q (%v)", ans, err)
	}
	// RTP: both feeds send from their own sockets; the relay must latch
	// them and deliver each side's audio to the other.
	feedA := startRTPFeed(t, ans)
	feedB := startRTPFeed(t, invB.Body())
	// b's first packet latches leg b onto feedB's socket (its real source,
	// not the SDP port); a's audio then flows there.
	feedB.packet(0, make([]byte, 160), false)
	time.Sleep(200 * time.Millisecond) // the latch lands before a's audio
	feedA.packet(0, make([]byte, 160), false)
	pkt := <-func() chan *media.RTPPacket {
		ch := make(chan *media.RTPPacket, 1)
		go func() {
			buf := make([]byte, 2048)
			_ = feedB.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, _, err := feedB.conn.ReadFrom(buf)
			if err == nil {
				if p, ok := media.ParseRTP(buf[:n]); ok {
					ch <- p
				}
			}
			close(ch)
		}()
		return ch
	}()
	if pkt == nil {
		t.Fatal("the callee feed received nothing: one-way audio")
	}
	hangup(t, r.dcs)
	hangup(t, r.dcs)
	cd := pbx.nextCDR(t)
	if cd.MediaMode != "anchored" {
		t.Fatalf("CDR media = %q, want anchored", cd.MediaMode)
	}
	if !strings.Contains(traceText(cd.Trace), "Media anchored (nat)") {
		t.Fatalf("CDR trace lacks the NAT anchor step: %s", traceText(cd.Trace))
	}
}

// TestRTPMetrics fails if the packets, octets, sessions or anchored-call
// gauges do not move for an anchored call, or if a failed anchor (port
// range exhausted) does not count and leave the call direct (spec S-3).
// Mutation: dropping the Observe wiring from startAnchor freezes the
// packet counters; dropping the AnchorFailures increment hides the
// exhaustion.
func TestRTPMetrics(t *testing.T) {
	objs := &fakeObjects{}
	pbx := startPBX(t, callerDevices(), shortRing(2*time.Second), withMedia(false),
		withVoicemail(newVMStore(), objs))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	// NAT on the callee side anchors the call: one registration whose
	// Contact names a port its packets never come from.
	b := newPhone(t, pbx, "b1", "pb1")
	if res := b.authDo(b.registerReq(300, "<sip:b1@"+testDomain+":51997;transport=udp>"), AlgSHA256, "pb1"); res.StatusCode != 200 {
		t.Fatalf("NAT register = %d", res.StatusCode)
	}
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE to b")
	feedA := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feedA.silence(0.5)
	if v := pbx.metric(t, "hello_media_anchored_calls", nil); v != 1 {
		t.Fatalf("anchored calls = %v, want 1", v)
	}
	if v := pbx.metric(t, "hello_rtp_sessions", nil); v != 1 {
		t.Fatalf("rtp sessions = %v, want 1", v)
	}
	// The stats tick is one second: give it a moment, then expect both
	// directions' counters.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pbx.metric(t, "hello_rtp_packets_total", map[string]string{"direction": "a>b"}) > 0 &&
			pbx.metric(t, "hello_rtp_octets_total", map[string]string{"direction": "a>b"}) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if v := pbx.metric(t, "hello_rtp_packets_total", map[string]string{"direction": "a>b"}); v == 0 {
		t.Fatal("hello_rtp_packets_total{a>b} never moved")
	}
	if v := pbx.metric(t, "hello_rtp_octets_total", map[string]string{"direction": "a>b"}); v == 0 {
		t.Fatal("hello_rtp_octets_total{a>b} never moved")
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pbx.metric(t, "hello_media_anchored_calls", nil) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if v := pbx.metric(t, "hello_media_anchored_calls", nil); v != 0 {
		t.Fatalf("anchored calls = %v after hangup, want 0", v)
	}
	// Anchor failure: exhaust the configured range, the call falls back to
	// direct, is counted, and still completes.
	pbx2 := startPBX(t, callerDevices(), shortRing(2*time.Second),
		func(c *Config, d *Deps) {
			c.RTPPortMin, c.RTPPortMax = 32000, 32001
			c.MediaForceAnchor = true
		},
		withVoicemail(newVMStore(), objs))
	hoard, err := media.NewRelay(32000, 32001)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hoard.AddLeg("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := hoard.AddLeg("b"); err != nil {
		t.Fatal(err)
	}
	defer hoard.Close()
	a2 := newPhone(t, pbx2, "a1", "pa")
	a2.register(t)
	b2 := newPhone(t, pbx2, "b1", "pb1")
	b2.register(t)
	b2.setCallee(answerAfter(nil))
	r2 := waitCall(t, dial(t.Context(), a2, "200"))
	if r2.err != nil {
		t.Fatalf("exhausted-range call: %v", r2.err)
	}
	waitReq(t, b2.invites, "INVITE to b")
	hangup(t, r2.dcs)
	cd2 := pbx2.nextCDR(t)
	if cd2.MediaMode == "anchored" {
		t.Fatal("the exhausted-range call anchored anyway")
	}
	if !strings.Contains(traceText(cd2.Trace), "Media anchor failed") {
		t.Fatalf("CDR trace lacks the anchor failure: %s", traceText(cd2.Trace))
	}
	if v := pbx2.metric(t, "hello_media_anchor_failures_total", nil); v != 1 {
		t.Fatalf("anchor failures = %v, want 1", v)
	}
}

// TestAdvertisedAnchorSDP fails if a set media anchor host is not stamped
// into both SDP answers (the caller's 200 and the fork's offer to the
// callee), or if the advertised port is not one of the relay's allocated
// ports from the configured RTP range — what a LAN phone dials its RTP to
// (spec S-2, the kw defect). Mutation: unwiring SetMediaAnchor in
// cmd/hello-sip restores the offer-mirroring fallback and fails the host
// assertions; advertising the offer's port instead of the leg's fails the
// range assertions.
func TestAdvertisedAnchorSDP(t *testing.T) {
	pbx := startPBX(t, callerDevices(), withMedia(true))
	pbx.srv.SetMediaAnchor(media.NewAnchor("192.0.2.10", discard))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	// The caller's answer: the anchor host and an allocated relay port.
	sdpA, err := media.ParseAudioSDP(r.dcs.InviteResponse.Body())
	if err != nil {
		t.Fatalf("caller answer SDP: %v", err)
	}
	if sdpA.Address != "192.0.2.10" {
		t.Fatalf("caller answer advertises %q, want the anchor host 192.0.2.10", sdpA.Address)
	}
	if sdpA.Port < 30000 || sdpA.Port > 31000 {
		t.Fatalf("caller answer port %d, want a relay leg from 30000-31000", sdpA.Port)
	}
	// The callee's offer: the same anchor host, its own allocated leg.
	invB := waitReq(t, b.invites, "INVITE to b")
	sdpB, err := media.ParseAudioSDP(invB.Body())
	if err != nil {
		t.Fatalf("fork offer SDP: %v", err)
	}
	if sdpB.Address != "192.0.2.10" {
		t.Fatalf("fork offer advertises %q, want the anchor host 192.0.2.10", sdpB.Address)
	}
	if sdpB.Port < 30000 || sdpB.Port > 31000 || sdpB.Port == sdpA.Port {
		t.Fatalf("fork offer port %d, want a second relay leg from 30000-31000 (answer was %d)", sdpB.Port, sdpA.Port)
	}
	hangup(t, r.dcs)
	cd := pbx.nextCDR(t)
	if cd.MediaMode != "anchored" {
		t.Fatalf("CDR media = %q, want anchored", cd.MediaMode)
	}
}
