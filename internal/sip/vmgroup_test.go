package sip

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// groupFeatures is a snapshot with the broadcast code, the extensions 100 and
// 200 with boxes (3 and 5) and the voicemail group 700 over members. A group
// is built with its members' boxes, as Load resolves them.
func groupFeatures(g snapshot.VoicemailGroup) func(s *snapshot.Snapshot) {
	return func(s *snapshot.Snapshot) {
		featureSnapshot(s)
		s.WithFeatureCodes([]snapshot.FeatureCode{
			{Code: "*97", Action: ActionVoicemail},
			{Code: "*63", Action: ActionVoicemailBroadcast},
		})
		s.WithExtensionData([]snapshot.Extension{
			{Number: "100", VoicemailEnabled: true, VoicemailBoxID: 3},
			{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5},
		})
		s.WithVoicemailGroups([]snapshot.VoicemailGroup{g})
	}
}

func testGroup(members ...string) snapshot.VoicemailGroup {
	g := snapshot.VoicemailGroup{ID: 1, Number: "700", Name: "Sales", Enabled: true, MaxSeconds: 60}
	boxes := map[string]int64{"100": 3, "200": 5}
	for _, m := range members {
		g.Members = append(g.Members, snapshot.VoicemailGroupMember{Extension: m, BoxID: boxes[m]})
	}
	return g
}

// recordGroupCall places the call dialled by 100 and records a short message.
func recordGroupCall(t *testing.T, pbx *testPBX, a *phone, dialled string) {
	t.Helper()
	r := waitCall(t, dial(t.Context(), a, dialled))
	if r.err != nil {
		t.Fatalf("%s: %v", dialled, r.err)
	}
	if answer := string(r.dcs.InviteResponse.Body()); !strings.Contains(answer, "m=audio") {
		t.Fatalf("answer is not the anchor's: %q", answer)
	}
	feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
	feed.silence(2.5)
	feed.digit('#')
}

func waitMessages(t *testing.T, vm *fakeVMStore, n int) []VoicemailMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(vm.snapshotMsgs()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("got %d messages, want %d", len(vm.snapshotMsgs()), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return vm.snapshotMsgs()
}

// TestVoicemailGroup fails if a message left for a group is not copied to
// every member box with one shared object, if a box's MWI is not updated, if
// a broadcast from a non-member is not refused with 403, or if a disabled
// group takes a message.
func TestVoicemailGroup(t *testing.T) {
	t.Run("dial copies to every member", func(t *testing.T) {
		vm := newVMStore()
		objs := &fakeObjects{}
		pbx := startPBX(t, callerDevices(), withFeatures(groupFeatures(testGroup("100", "200"))), withVoicemail(vm, objs))
		anchored(pbx)
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)
		b := newPhone(t, pbx, "b1", "pb1")
		b.register(t)

		recordGroupCall(t, pbx, a, "700")
		msgs := waitMessages(t, vm, 2)
		if len(msgs) != 2 || msgs[0].MinioObject != msgs[1].MinioObject {
			t.Fatalf("messages = %+v, want two rows sharing one object", msgs)
		}
		boxes := map[int64]bool{msgs[0].BoxID: true, msgs[1].BoxID: true}
		if !boxes[3] || !boxes[5] {
			t.Fatalf("boxes = %v, want 3 and 5", boxes)
		}
		if got := len(objs.snapshot()); got != 1 {
			t.Fatalf("%d stored objects, want 1", got)
		}
		if msgs[0].Caller != "100" || msgs[0].DurationMs < 1000 {
			t.Fatalf("message row = %+v", msgs[0])
		}
		if v := pbx.metric(t, "hello_voicemail_group_deliveries_total", map[string]string{"result": "delivered"}); v != 1 {
			t.Fatalf("group deliveries{delivered} = %v, want 1", v)
		}
		// MWI for the member whose phone is registered.
		m := waitReq(t, b.notifies, "MWI NOTIFY")
		if body := string(m.Body()); !strings.Contains(body, "Messages-Waiting: yes") || !strings.Contains(body, "Message-Account: sip:200@") {
			t.Fatalf("MWI body = %q", body)
		}
	})

	t.Run("broadcast by a member", func(t *testing.T) {
		vm := newVMStore()
		objs := &fakeObjects{}
		pbx := startPBX(t, callerDevices(), withFeatures(groupFeatures(testGroup("100", "200"))), withVoicemail(vm, objs))
		anchored(pbx)
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)

		recordGroupCall(t, pbx, a, "*63700")
		msgs := waitMessages(t, vm, 2)
		if msgs[0].MinioObject != msgs[1].MinioObject || msgs[0].Caller != "100" {
			t.Fatalf("messages = %+v", msgs)
		}
	})

	t.Run("broadcast by a non-member is refused", func(t *testing.T) {
		vm := newVMStore()
		objs := &fakeObjects{}
		pbx := startPBX(t, callerDevices(), withFeatures(groupFeatures(testGroup("200"))), withVoicemail(vm, objs))
		anchored(pbx)
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)

		r := waitCall(t, dial(t.Context(), a, "*63700"))
		if code := responseCode(r.err); code != 403 {
			t.Fatalf("*63700 from a non-member = %v, want 403", r.err)
		}
		if got := len(vm.snapshotMsgs()); got != 0 {
			t.Fatalf("%d messages stored for a refused broadcast", got)
		}
		if v := pbx.metric(t, "hello_voicemail_group_deliveries_total", map[string]string{"result": "refused"}); v != 1 {
			t.Fatalf("group deliveries{refused} = %v, want 1", v)
		}
		// A non-member may still leave a message by dialling the number.
		recordGroupCall(t, pbx, a, "700")
		waitMessages(t, vm, 1)
	})

	t.Run("unknown group and disabled group", func(t *testing.T) {
		g := testGroup("100", "200")
		g.Enabled = false
		vm := newVMStore()
		pbx := startPBX(t, callerDevices(), withFeatures(groupFeatures(g)), withVoicemail(vm, &fakeObjects{}))
		anchored(pbx)
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)

		if r := waitCall(t, dial(t.Context(), a, "*63999")); responseCode(r.err) != 404 {
			t.Fatalf("*63 of an unknown group = %v, want 404", r.err)
		}
		pbx.nextCDR(t)
		if r := waitCall(t, dial(t.Context(), a, "700")); responseCode(r.err) != 404 {
			t.Fatalf("a disabled group = %v, want 404", r.err)
		}
	})

	t.Run("a box removed after the snapshot is skipped", func(t *testing.T) {
		vm := newVMStore()
		vm.noBox = map[int64]bool{5: true}
		pbx := startPBX(t, callerDevices(), withFeatures(groupFeatures(testGroup("100", "200"))), withVoicemail(vm, &fakeObjects{}))
		anchored(pbx)
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)

		recordGroupCall(t, pbx, a, "700")
		msgs := waitMessages(t, vm, 1)
		time.Sleep(100 * time.Millisecond)
		if got := vm.snapshotMsgs(); len(got) != 1 || got[0].BoxID != 3 || msgs[0].BoxID != 3 {
			t.Fatalf("messages = %+v, want one for box 3", got)
		}
	})
}

// TestIVRVoicemailGroupDestination fails if a menu option of kind
// voicemail_group does not take the caller into the group's recording, or
// does not end the call after the take (nothing is recorded here, so the take
// is discarded and counted).
func TestIVRVoicemailGroupDestination(t *testing.T) {
	m := ivrMenu("main", "800", map[string]snapshot.IVRDest{"3": {Kind: snapshot.IVRVoicemailGroup, Target: "700"}})
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000), 8000))
	vm := newVMStore()
	feats := func(s *snapshot.Snapshot) {
		groupFeatures(testGroup("100", "200"))(s)
		s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav"})
		s.WithIVRMenus([]snapshot.IVRMenu{m})
	}
	pbx := startPBX(t, threeDevices(), withMedia(false), withFeatures(feats), withVoicemail(vm, objs))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	var seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "3", &seq)
	sendDigits(t, a, r.dcs, "#", &seq)
	waitReq(t, a.byes, "the call ends after the message")
	if v := pbx.metric(t, "hello_voicemail_group_deliveries_total", map[string]string{"result": "discarded"}); v != 1 {
		t.Fatalf("group deliveries{discarded} = %v, want 1", v)
	}
}
