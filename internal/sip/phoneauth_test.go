package sip

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

// countingSecrets is a DeviceSecrets that counts its reads.
type countingSecrets struct {
	pass  map[string]string
	reads atomic.Int64
}

func (c *countingSecrets) DeviceSecret(_ context.Context, user string) (string, error) {
	c.reads.Add(1)
	return c.pass[user], nil
}

func withDeviceSecrets(ds DeviceSecrets) pbxOpt {
	return func(_ *Config, d *Deps) { d.DeviceSecrets = ds }
}

// viaBranch is the branch parameter of a request's top Via.
func viaBranch(r *sip.Request) string {
	if v := r.Via(); v != nil {
		if b, ok := v.Params.Get("branch"); ok {
			return b
		}
	}
	return ""
}

// TestAuthenticatedInviteToPhone: a phone with "Authenticate Incoming INVITE"
// on (Grandstream P2346, spec S-22) challenges every INVITE Hello sends it.
// It fails if
//   - Hello does not answer the phone's 401 to an INVITE it originates, in
//     the device's realm (stored HA1) or in the phone's own realm (the
//     device password, read once and then cached),
//   - the re-sent INVITE is not a new transaction of the same dialog attempt
//     (RFC 3261 22.2: same Call-ID, From tag and To, CSeq one higher, a new
//     Via branch),
//   - a re-INVITE in the dialog (hold) that the phone challenges is not
//     answered,
//   - a phone whose credentials Hello cannot satisfy makes Hello retry
//     without end instead of failing the call.
func TestAuthenticatedInviteToPhone(t *testing.T) {
	secrets := &countingSecrets{pass: map[string]string{"b1": "pb1"}}
	pbx := startPBX(t, callerDevices(), withDeviceSecrets(secrets))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)

	t.Run("device realm", func(t *testing.T) {
		b.mu.Lock()
		b.invitePass, b.inviteRealm = "pb1", testDomain
		b.mu.Unlock()
		r := waitCall(t, dial(t.Context(), a, "200"))
		if r.err != nil {
			t.Fatalf("call to a phone that authenticates INVITEs: %v", r.err)
		}
		first := waitReq(t, b.challenged, "the challenged INVITE")
		second := waitReq(t, b.invites, "the authenticated INVITE")
		if second.GetHeader("Authorization") == nil {
			t.Fatal("the re-sent INVITE carries no Authorization")
		}
		if first.CallID().Value() != second.CallID().Value() {
			t.Errorf("Call-ID changed: %s then %s", first.CallID().Value(), second.CallID().Value())
		}
		if tagOfTest(first.From()) != tagOfTest(second.From()) {
			t.Errorf("From tag changed: %s then %s", tagOfTest(first.From()), tagOfTest(second.From()))
		}
		if _, ok := second.To().Params.Get("tag"); ok {
			t.Error("the re-sent INVITE has a To tag")
		}
		if got, want := second.CSeq().SeqNo, first.CSeq().SeqNo+1; got != want {
			t.Errorf("CSeq %d, want %d", got, want)
		}
		if viaBranch(first) == "" || viaBranch(first) == viaBranch(second) {
			t.Errorf("Via branch %q reused (first %q): the re-sent INVITE is not a new transaction", viaBranch(second), viaBranch(first))
		}
		// Hold from the caller: Hello's re-INVITE to b is challenged too.
		res := a.reinviteAsCaller(t, r.dcs, a.rewriteSDP(func(s string) string { return s + "a=sendonly\r\n" }))
		if res.StatusCode != 200 {
			t.Fatalf("hold re-INVITE = %d, want 200 after Hello answered the phone's challenge", res.StatusCode)
		}
		re := waitReq(t, b.challenged, "the challenged re-INVITE")
		if _, ok := re.To().Params.Get("tag"); !ok {
			t.Fatalf("challenged request is not in dialog: %s", re.StartLine())
		}
		waitReq(t, b.reinvites, "the authenticated re-INVITE")
		hangup(t, r.dcs)
		pbx.nextCDR(t)
		if v := pbx.metric(t, "hello_phone_challenges_total", map[string]string{"method": "INVITE", "result": "answered"}); v < 2 {
			t.Errorf("hello_phone_challenges_total{INVITE,answered} = %v, want 2", v)
		}
		if n := secrets.reads.Load(); n != 0 {
			t.Errorf("the password was read %d times for a challenge the stored HA1 answers", n)
		}
	})

	t.Run("phone realm", func(t *testing.T) {
		// A WP challenges in its configured server, not the SIP domain.
		b.mu.Lock()
		b.invitePass, b.inviteRealm = "pb1", "192.0.2.10:5060"
		b.mu.Unlock()
		for i := range 2 {
			r := waitCall(t, dial(t.Context(), a, "200"))
			if r.err != nil {
				t.Fatalf("call %d in the phone's realm: %v", i+1, r.err)
			}
			waitReq(t, b.challenged, "the challenged INVITE")
			waitReq(t, b.invites, "the authenticated INVITE")
			hangup(t, r.dcs)
			pbx.nextCDR(t)
		}
		if n := secrets.reads.Load(); n != 1 {
			t.Errorf("the password was read %d times over two calls, want once (cached)", n)
		}
	})

	t.Run("wrong credentials", func(t *testing.T) {
		b.mu.Lock()
		b.invitePass, b.inviteRealm = "not-the-device-password", testDomain
		b.mu.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if r := waitCall(t, dial(ctx, a, "200")); r.err == nil {
			t.Fatal("a phone that refuses Hello's credentials still connected")
		}
		pbx.nextCDR(t)
		n := 0
	drain:
		for {
			select {
			case <-b.challenged:
				n++
			case <-time.After(300 * time.Millisecond):
				break drain
			}
		}
		if n == 0 || n > 1+maxPhoneAuthAttempts {
			t.Fatalf("the phone challenged %d INVITEs, want 1 to %d", n, 1+maxPhoneAuthAttempts)
		}
		b.mu.Lock()
		b.invitePass = ""
		b.mu.Unlock()
	})
}
