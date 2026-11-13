package sip

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/snapshot"
)

var (
	derivedKey    = strings.Repeat("a", 40)
	derivedKeyOld = strings.Repeat("b", 40)
)

// webDev is a web device of extension ext, as the snapshot loads it: no
// stored HA1, derived credentials, the stable base name w<id>.
func webDev(id int64, ext string) snapshot.Device {
	return snapshot.Device{
		ID: id, Username: auth.DerivedBase(auth.KindWeb, id), Realm: testDomain, Extension: ext, ExtensionName: "Ext " + ext,
		Kind: auth.KindWeb, AuthMode: snapshot.AuthDerived, UserID: 7,
	}
}

func withClientKeys(cur, prev string) pbxOpt {
	return func(c *Config, _ *Deps) { c.ClientAuthKey, c.ClientAuthKeyPrevious = cur, prev }
}

// derivedPhone is a phone registering as the web device with the given
// expiring name, holding the secret derived under key.
func derivedPhone(t *testing.T, pbx *testPBX, base string, expires time.Time, key string) *phone {
	t.Helper()
	name := auth.DerivedUsername(base, expires)
	return newPhone(t, pbx, name, auth.DerivedSecret([]byte(key), name, testDomain))
}

func registerStatus(p *phone, alg string) int {
	return p.authDo(p.registerReq(300), alg, p.pass).StatusCode
}

// TestDerivedCredentials fails if a web device cannot register with a valid
// derived credential (under either digest algorithm), or if one is accepted
// that has expired, is for another realm, was made under an unknown key or
// for another expiry, has no expiry at all, or belongs to a revoked device;
// and if the previous key stops being accepted, or the stored-HA1 path
// changes for a desk phone.
func TestDerivedCredentials(t *testing.T) {
	devs := []snapshot.Device{webDev(12, "100"), dev(1, "desk", "100", "pw")}
	pbx := startPBX(t, devs, withClientKeys(derivedKey, derivedKeyOld))
	future := time.Now().Add(time.Hour)

	t.Run("valid under both algorithms", func(t *testing.T) {
		for _, alg := range []string{AlgSHA256, AlgMD5} {
			p := derivedPhone(t, pbx, "w12", future, derivedKey)
			if got := registerStatus(p, alg); got != 200 {
				t.Fatalf("%s: register = %d, want 200", alg, got)
			}
		}
		// The binding is stored under the stable base AOR, not under the
		// expiring name, so a refreshed credential replaces it.
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		bs, err := pbx.state.Bindings(ctx, pbx.srv.aor("w12"))
		if err != nil || len(bs) == 0 || bs[0].Device != "w12" || bs[0].Extension != "100" {
			t.Fatalf("bindings under the base AOR = %+v, %v", bs, err)
		}
	})

	t.Run("expired, then accepted after a refresh", func(t *testing.T) {
		if got := registerStatus(derivedPhone(t, pbx, "w12", time.Now().Add(-time.Minute), derivedKey), AlgSHA256); got != 401 {
			t.Fatalf("expired credential = %d, want 401", got)
		}
		if got := registerStatus(derivedPhone(t, pbx, "w12", time.Now().Add(time.Hour), derivedKey), AlgSHA256); got != 200 {
			t.Fatalf("refreshed credential = %d, want 200", got)
		}
	})

	t.Run("wrong realm", func(t *testing.T) {
		name := auth.DerivedUsername("w12", future)
		p := newPhone(t, pbx, name, auth.DerivedSecret([]byte(derivedKey), name, "other.realm"))
		if got := registerStatus(p, AlgSHA256); got != 401 {
			t.Fatalf("secret derived for another realm = %d, want 401", got)
		}
	})

	t.Run("previous key overlaps, an unknown key does not", func(t *testing.T) {
		if got := registerStatus(derivedPhone(t, pbx, "w12", future, derivedKeyOld), AlgSHA256); got != 200 {
			t.Fatalf("credential under the previous key = %d, want 200", got)
		}
		if got := registerStatus(derivedPhone(t, pbx, "w12", future, strings.Repeat("c", 40)), AlgSHA256); got != 401 {
			t.Fatalf("credential under an unknown key = %d, want 401", got)
		}
		only := startPBX(t, devs, withClientKeys(derivedKey, ""))
		if got := registerStatus(derivedPhone(t, only, "w12", future, derivedKeyOld), AlgSHA256); got != 401 {
			t.Fatalf("previous key after it was dropped = %d, want 401", got)
		}
	})

	t.Run("forged expiry", func(t *testing.T) {
		honest := auth.DerivedUsername("w12", time.Now().Add(time.Minute))
		secret := auth.DerivedSecret([]byte(derivedKey), honest, testDomain)
		forged := auth.DerivedUsername("w12", time.Now().Add(240*time.Hour))
		if got := registerStatus(newPhone(t, pbx, forged, secret), AlgSHA256); got != 401 {
			t.Fatalf("secret of one expiry used for a later one = %d, want 401", got)
		}
	})

	t.Run("no expiry in the name", func(t *testing.T) {
		p := newPhone(t, pbx, "w12", auth.DerivedSecret([]byte(derivedKey), "w12", testDomain))
		if got := registerStatus(p, AlgSHA256); got != 401 && got != 403 {
			t.Fatalf("base name with its derived secret = %d, want a refusal", got)
		}
	})

	t.Run("revoked device", func(t *testing.T) {
		revoked := startPBX(t, []snapshot.Device{dev(1, "desk", "100", "pw")}, withClientKeys(derivedKey, ""))
		if got := registerStatus(derivedPhone(t, revoked, "w12", future, derivedKey), AlgSHA256); got == 200 {
			t.Fatal("a device missing from the snapshot (disabled) registered")
		}
	})

	t.Run("derived credentials off without a key", func(t *testing.T) {
		off := startPBX(t, devs)
		if got := registerStatus(derivedPhone(t, off, "w12", future, derivedKey), AlgSHA256); got == 200 {
			t.Fatal("a web device registered on a node with no HELLO_CLIENT_AUTH_KEY")
		}
	})

	t.Run("a desk phone keeps its stored HA1", func(t *testing.T) {
		p := newPhone(t, pbx, "desk", "pw")
		if got := registerStatus(p, AlgSHA256); got != 200 {
			t.Fatalf("desk register = %d, want 200", got)
		}
		bad := newPhone(t, pbx, "desk", "nope")
		if got := registerStatus(bad, AlgSHA256); got != 401 {
			t.Fatalf("desk wrong password = %d, want 401", got)
		}
		// A desk device cannot be reached through the derived path: its
		// username does not become derived by looking like w<id>.<expiry>.
		lookalike := dev(2, "w99", "101", "pw")
		pbx2 := startPBX(t, []snapshot.Device{lookalike}, withClientKeys(derivedKey, ""))
		if got := registerStatus(derivedPhone(t, pbx2, "w99", future, derivedKey), AlgSHA256); got == 200 {
			t.Fatal("a digest device was reached by a derived name")
		}
	})
}

// TestRingAllDesktopWeb fails if an extension with a desk phone and a web
// client does not fork to both, if a web client whose owner turned ring_web
// off still rings, if the first answer does not CANCEL the other branch with
// the "Call completed elsewhere" Reason.
func TestRingAllDesktopWeb(t *testing.T) {
	future := time.Now().Add(time.Hour)
	web := webDev(12, "200")
	run := func(t *testing.T, ringOff bool) (pbx *testPBX, caller, desk, browser *phone) {
		web.RingOff = ringOff
		pbx = startPBX(t, []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "d1", "200", "pd"), web}, withClientKeys(derivedKey, ""))
		caller, desk = newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "d1", "pd")
		browser = derivedPhone(t, pbx, "w12", future, derivedKey)
		for _, p := range []*phone{caller, desk, browser} {
			if got := p.authDo(p.registerReq(300), AlgSHA256, p.pass).StatusCode; got != 200 {
				t.Fatalf("%s register = %d", p.user, got)
			}
		}
		return pbx, caller, desk, browser
	}

	t.Run("both ring, the answer cancels the other with a Reason", func(t *testing.T) {
		_, caller, desk, browser := run(t, false)
		answer := make(chan struct{})
		desk.setCallee(answerAfter(answer))
		browser.setCallee(ringForever())
		res := dial(t.Context(), caller, "200")
		waitReq(t, desk.invites, "INVITE to the desk phone")
		waitReq(t, browser.invites, "INVITE to the web client")
		close(answer)
		if r := waitCall(t, res); r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		cancelElsewhere(t, waitReq(t, browser.cancels, "CANCEL to the web client"))
	})

	t.Run("the trace names the web device that answered", func(t *testing.T) {
		pbx, caller, desk, browser := run(t, false)
		desk.setCallee(ringForever())
		browser.setCallee(answerAfter(nil))
		res := dial(t.Context(), caller, "200")
		r := waitCall(t, res)
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		hangup(t, r.dcs)
		cd := pbx.nextCDR(t)
		found := false
		for _, st := range cd.Trace {
			found = found || st.Text == "Answered by web device w12"
		}
		if !found {
			t.Fatalf("trace = %+v, want a step naming the web device", cd.Trace)
		}
	})

	t.Run("ring_web off skips the web client", func(t *testing.T) {
		_, caller, desk, browser := run(t, true)
		answer := make(chan struct{})
		desk.setCallee(answerAfter(answer))
		browser.setCallee(ringForever())
		res := dial(t.Context(), caller, "200")
		waitReq(t, desk.invites, "INVITE to the desk phone")
		noReq(t, browser.invites, 300*time.Millisecond, "INVITE to a web client whose owner turned ring_web off")
		close(answer)
		if r := waitCall(t, res); r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
	})
}

// TestMaxContactsConfigurable fails if the cap on bindings of one AOR ignores
// Config.MaxContacts: with 2, a third distinct contact is refused with 403
// Too Many Contacts, and a refresh of an existing one is not.
func TestMaxContactsConfigurable(t *testing.T) {
	pbx := startPBX(t, []snapshot.Device{dev(1, "a1", "100", "pa")}, func(c *Config, _ *Deps) { c.MaxContacts = 2 })
	p := newPhone(t, pbx, "a1", "pa")
	contact := func(port string) string { return "<sip:a1@127.0.0.1:" + port + ">" }
	for _, port := range []string{"7001", "7002"} {
		if got := p.authDo(p.registerReq(300, contact(port)), AlgSHA256, "pa").StatusCode; got != 200 {
			t.Fatalf("contact %s = %d, want 200", port, got)
		}
	}
	if got := p.authDo(p.registerReq(300, contact("7003")), AlgSHA256, "pa"); got.StatusCode != 403 {
		t.Fatalf("third contact = %d, want 403", got.StatusCode)
	}
	if got := p.authDo(p.registerReq(300, contact("7002")), AlgSHA256, "pa").StatusCode; got != 200 {
		t.Fatalf("refresh of an existing contact = %d, want 200", got)
	}
}
