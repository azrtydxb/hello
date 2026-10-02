package sip

import (
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

// TestDigestMD5AndSHA256 fails if either algorithm is rejected for the right
// password or accepted for a wrong one.
func TestDigestMD5AndSHA256(t *testing.T) {
	pbx := startPBX(t, []snapshot.Device{dev(1, "alice", "100", "right")})
	p := newPhone(t, pbx, "alice", "right")
	for _, alg := range []string{AlgMD5, AlgSHA256} {
		t.Run(alg, func(t *testing.T) {
			if res := p.authDo(p.registerReq(300), alg, "right"); res.StatusCode != 200 {
				t.Fatalf("right password: %d, want 200", res.StatusCode)
			}
			res := p.authDo(p.registerReq(300), alg, "wrong")
			if res.StatusCode != 401 {
				t.Fatalf("wrong password: %d, want 401", res.StatusCode)
			}
			if res.GetHeader("WWW-Authenticate") == nil {
				t.Fatal("wrong password: no new challenge")
			}
		})
	}
	res := p.do(p.registerReq(300))
	hs := res.GetHeaders("WWW-Authenticate")
	if len(hs) != 2 || pickChallenge(t, res, AlgSHA256).Realm != testDomain || !pickChallenge(t, res, AlgMD5).SupportsQOP("auth") {
		t.Fatalf("challenge headers = %v", hs)
	}
}

// TestNonceAcrossNodes fails if a nonce issued by one node is not accepted by
// another with the same secret, or is accepted by one with another secret.
func TestNonceAcrossNodes(t *testing.T) {
	devs := []snapshot.Device{dev(1, "alice", "100", "pw")}
	node1 := startPBX(t, devs)
	node2 := startPBX(t, devs, func(c *Config, _ *Deps) { c.NodeID = "sip-2" })
	other := startPBX(t, devs, func(c *Config, _ *Deps) { c.NonceSecret = []byte(testSecret + "-different") })
	p := newPhone(t, node1, "alice", "pw")

	req := p.registerReq(300)
	chal := pickChallenge(t, p.do(req), AlgSHA256) // issued by node1
	authorize(t, req, chal, "alice", "pw")
	req.SetDestination(node2.addr)
	if res := p.do(req); res.StatusCode != 200 {
		t.Fatalf("node1 nonce at node2 = %d, want 200", res.StatusCode)
	}
	authorize(t, req, chal, "alice", "pw")
	req.SetDestination(other.addr)
	if res := p.do(req); res.StatusCode != 401 {
		t.Fatalf("node1 nonce at node with another secret = %d, want 401", res.StatusCode)
	}
}

// TestStaleNonce fails if an expired but authentic nonce is not answered
// 401 with stale=true, or if a forged one is.
func TestStaleNonce(t *testing.T) {
	pbx := startPBX(t, []snapshot.Device{dev(1, "alice", "100", "pw")})
	p := newPhone(t, pbx, "alice", "pw")
	req := p.registerReq(300)
	chal := pickChallenge(t, p.do(req), AlgMD5)

	old := &Digest{Realm: testDomain, Secret: []byte(testSecret), Now: func() time.Time { return time.Now().Add(-NonceValidity - time.Minute) }}
	chal.Nonce = old.Nonce()
	authorize(t, req, chal, "alice", "pw")
	res := p.do(req)
	if res.StatusCode != 401 {
		t.Fatalf("expired nonce = %d, want 401", res.StatusCode)
	}
	if !pickChallenge(t, res, AlgMD5).Stale || !pickChallenge(t, res, AlgSHA256).Stale {
		t.Fatalf("expired nonce: challenge not stale: %v", res.GetHeaders("WWW-Authenticate"))
	}
	// The fresh nonce from the stale challenge works.
	authorize(t, req, pickChallenge(t, res, AlgMD5), "alice", "pw")
	if res := p.do(req); res.StatusCode != 200 {
		t.Fatalf("after stale = %d, want 200", res.StatusCode)
	}

	forged := &Digest{Realm: testDomain, Secret: []byte("another-secret-another-secret-xx"), Now: old.Now}
	chal.Nonce = forged.Nonce()
	authorize(t, req, chal, "alice", "pw")
	res = p.do(req)
	if res.StatusCode != 401 || pickChallenge(t, res, AlgMD5).Stale {
		t.Fatalf("forged old nonce = %d stale=%v, want 401 without stale", res.StatusCode, pickChallenge(t, res, AlgMD5).Stale)
	}
}

// TestDigestVerifyUnit covers the verdicts without the network.
func TestDigestVerifyUnit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	d := &Digest{Realm: testDomain, Secret: []byte(testSecret), Now: func() time.Time { return now }}
	device := dev(1, "alice", "100", "pw")
	nonce := d.Nonce()
	req := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: testDomain})
	for _, alg := range []string{AlgMD5, AlgSHA256} {
		for _, tc := range []struct {
			pass  string
			at    time.Duration
			nonce string
			want  Verdict
		}{
			{"pw", 0, nonce, OK},
			{"pw", NonceValidity - time.Second, nonce, OK},
			{"pw", NonceValidity + time.Second, nonce, Stale},
			{"bad", 0, nonce, Bad},
			{"pw", 0, nonce[:len(nonce)-2] + "AA", Bad},
			{"pw", 0, "not-base64!", Bad},
		} {
			chalNow := now
			d.Now = func() time.Time { return chalNow.Add(tc.at) }
			creds := credentialsFor(t, req, alg, tc.nonce, "alice", tc.pass)
			if got := d.Verify(creds, "REGISTER", device.HA1MD5, device.HA1SHA256); got != tc.want {
				t.Errorf("%s pass=%s at=+%s: verdict %d, want %d", alg, tc.pass, tc.at, got, tc.want)
			}
		}
	}
	d.Now = func() time.Time { return now }
	future := &Digest{Realm: testDomain, Secret: []byte(testSecret), Now: func() time.Time { return now.Add(time.Hour) }}
	if got := d.Verify(credentialsFor(t, req, AlgMD5, future.Nonce(), "alice", "pw"), "REGISTER", device.HA1MD5, device.HA1SHA256); got != Bad {
		t.Errorf("future nonce verdict %d, want Bad", got)
	}
}

func credentialsFor(t *testing.T, req *sip.Request, alg, nonce, user, pass string) *digest.Credentials {
	t.Helper()
	chal := &digest.Challenge{Realm: testDomain, Nonce: nonce, Algorithm: alg, QOP: []string{"auth"}}
	cred, err := digest.Digest(chal, digest.Options{Method: req.Method.String(), URI: req.Recipient.String(), Username: user, Password: pass})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCredentials(cred.String())
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
