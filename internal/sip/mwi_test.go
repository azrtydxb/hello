package sip

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// TestMessageSummarySubscribe fails if a message-summary SUBSCRIBE from the
// mailbox's own device is not answered 200 with an immediate NOTIFY carrying
// the current counts, if a change (new, heard) does not NOTIFY the
// subscription, if another extension's device can subscribe, if an unknown
// event is answered other than 489, if any MWI is sent as MESSAGE, or if a
// contact without a subscription gets no unsolicited NOTIFY (and one with a
// subscription gets two).
func TestMessageSummarySubscribe(t *testing.T) {
	old := mwiPollInterval
	mwiPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { mwiPollInterval = old })

	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
	if _, err := vm.InsertMessage(context.Background(), 5, "obj1", "100", "", 2000); err != nil {
		t.Fatal(err)
	}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
	}
	pbx := startPBX(t, callerDevices(), withFeatures(feats), withVoicemail(vm, &fakeObjects{}))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	drain := func(ch chan *sip.Request) {
		for {
			select {
			case <-ch:
			default:
				return
			}
		}
	}
	waitNotifyBody := func(ch chan *sip.Request, want, what string) *sip.Request {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case r := <-ch:
				if r.GetHeader("Event") != nil && r.GetHeader("Event").Value() == "message-summary" && strings.Contains(string(r.Body()), want) {
					return r
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %s (%q)", what, want)
			}
		}
	}
	drain(b.notifies)

	// Another extension's device is refused, an unknown event gets 489.
	if res := a.subscribeEvent(t, "200", "message-summary", 120); res.StatusCode != 403 {
		t.Fatalf("foreign mailbox SUBSCRIBE = %d, want 403", res.StatusCode)
	}
	res := b.subscribeEvent(t, "200", "x-unknown", 120)
	if res.StatusCode != 489 {
		t.Fatalf("unknown event SUBSCRIBE = %d, want 489", res.StatusCode)
	}
	if h := res.GetHeader("Allow-Events"); h == nil || !strings.Contains(h.Value(), "message-summary") {
		t.Fatalf("489 Allow-Events = %v", h)
	}

	// Own mailbox: 200 (Expires clamped up to 60) and an immediate NOTIFY.
	res = b.subscribeEvent(t, "200", "message-summary", 5)
	if res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE = %d", res.StatusCode)
	}
	if h := res.GetHeader("Expires"); h == nil || h.Value() != "60" {
		t.Fatalf("Expires = %v, want 60", h)
	}
	first := waitNotifyBody(b.notifies, "Messages-Waiting: yes", "initial NOTIFY")
	if body := string(first.Body()); !strings.Contains(body, "Voice-Message: 1/1") || !strings.Contains(body, "Message-Account: sip:200@") {
		t.Fatalf("initial NOTIFY body = %q", body)
	}
	if st := first.GetHeader("Subscription-State"); st == nil || !strings.HasPrefix(st.Value(), "active") {
		t.Fatalf("Subscription-State = %v", st)
	}
	if ct := first.GetHeader("Content-Type"); ct == nil || ct.Value() != "application/simple-message-summary" {
		t.Fatalf("Content-Type = %v", ct)
	}

	// Heard (changed behind the process's back): the poll NOTIFYs.
	if err := vm.MarkHeard(context.Background(), vm.snapshotMsgs()[0].ID); err != nil {
		t.Fatal(err)
	}
	waitNotifyBody(b.notifies, "Messages-Waiting: no", "NOTIFY after heard")

	// New message in-process: one NOTIFY to the subscription, none unsolicited.
	if _, err := vm.InsertMessage(context.Background(), 5, "obj2", "100", "", 2000); err != nil {
		t.Fatal(err)
	}
	pbx.srv.notifyMWI("200")
	waitNotifyBody(b.notifies, "Messages-Waiting: yes", "NOTIFY after new message")
	noReq(t, b.notifies, 300*time.Millisecond, "second NOTIFY for one change")
	noReq(t, b.messages, 10*time.Millisecond, "MWI MESSAGE")

	// A device with no subscription gets the unsolicited form, without
	// Subscription-State.
	drain(a.notifies)
	pbx.srv.notifyMWI("100")
	un := waitNotifyBody(a.notifies, "Messages-Waiting: no", "unsolicited NOTIFY")
	if un.GetHeader("Subscription-State") != nil {
		t.Fatalf("unsolicited NOTIFY carries Subscription-State: %s", un.String())
	}
	eventually(t, "unsolicited counter", func() bool {
		return pbx.metric(t, "hello_mwi_notify_total", map[string]string{"kind": "unsolicited", "result": "ok"}) >= 1
	})
}

// TestMailboxOf fails if a device resolves to a mailbox other than its own
// extension.
func TestMailboxOf(t *testing.T) {
	snap := snapshot.New(1, testDomain, callerDevices())
	b, _ := snap.DeviceByUsername("b1")
	for _, tc := range []struct {
		user string
		want bool
	}{{"200", true}, {"b1", true}, {"100", false}, {"a1", false}, {"", false}} {
		if _, ok := mailboxOf(snap, b, tc.user); ok != tc.want {
			t.Errorf("mailboxOf(%q) = %v, want %v", tc.user, ok, tc.want)
		}
	}
}

// fakeMailboxEvents is a MailboxEvents the test drives by hand.
type fakeMailboxEvents struct {
	ch chan int64
}

func (f *fakeMailboxEvents) Watch(ctx context.Context, changed func(int64)) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-f.ch:
			changed(b)
		}
	}
}

// TestMailboxEventNotifies fails if a mailbox event from the control plane
// (a message heard through the HTTP API) does not NOTIFY the box owner's
// subscription well before the safety-net re-read, or if an event for an
// unrelated box NOTIFYs it.
func TestMailboxEventNotifies(t *testing.T) {
	vm := newVMStore()
	vm.boxes["200"] = VoicemailBox{ID: 5, Extension: "200"}
	if _, err := vm.InsertMessage(context.Background(), 5, "obj1", "100", "", 2000); err != nil {
		t.Fatal(err)
	}
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", VoicemailEnabled: true, VoicemailBoxID: 5}})
	}
	ev := &fakeMailboxEvents{ch: make(chan int64, 4)}
	pbx := startPBX(t, callerDevices(), withFeatures(feats), withVoicemail(vm, &fakeObjects{}),
		func(_ *Config, d *Deps) { d.MailboxEvents = ev })
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	if res := b.subscribeEvent(t, "200", "message-summary", 120); res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE = %d", res.StatusCode)
	}
	wait := func(want string) {
		t.Helper()
		deadline := time.After(5 * time.Second) // the safety-net poll is 60 s
		for {
			select {
			case r := <-b.notifies:
				if strings.Contains(string(r.Body()), want) {
					return
				}
			case <-deadline:
				t.Fatalf("no NOTIFY with %q after the mailbox event", want)
			}
		}
	}
	wait("Messages-Waiting: yes")

	if err := vm.MarkHeard(context.Background(), vm.snapshotMsgs()[0].ID); err != nil {
		t.Fatal(err)
	}
	ev.ch <- 99 // another box: nothing for this phone
	noReq(t, b.notifies, 300*time.Millisecond, "NOTIFY for an unrelated box")
	ev.ch <- 5
	wait("Messages-Waiting: no")
}
