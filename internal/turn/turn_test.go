package turn

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // the scheme under test
	"encoding/base64"
	"reflect"
	"testing"
	"time"
)

// TestCredentialVector pins the HMAC to a value computed outside this
// package (python hmac with sha1), so a change of algorithm, of the encoding
// or of what is signed fails: coturn would reject every credential.
func TestCredentialVector(t *testing.T) {
	u, p := Credential("s3cret-shared-with-coturn", "42", time.Unix(1893456000, 0))
	if u != "1893456000:42" {
		t.Fatalf("username = %q", u)
	}
	const want = "wLNnPgIF2d4hCLBsnBZuKTwe7Q4="
	if p != want {
		t.Fatalf("credential = %q, want %q", p, want)
	}
}

// TestCredentialValidatesLikeCoturn replays what coturn does with
// use-auth-secret: split the username, refuse an expired timestamp, recompute
// the HMAC with the shared secret and compare.
func TestCredentialValidatesLikeCoturn(t *testing.T) {
	const secret = "shared-secret"
	now := time.Unix(1800000000, 0)
	c := Config{TURN: []string{"turn:turn1.example:3478?transport=udp"}, Secret: secret, TTL: time.Hour, Now: func() time.Time { return now }}
	res := c.Servers("7")
	if len(res.IceServers) != 1 {
		t.Fatalf("servers = %+v", res.IceServers)
	}
	s := res.IceServers[0]
	check := func(user, cred string, at time.Time, key string) bool {
		var ts int64
		for i := 0; i < len(user) && user[i] != ':'; i++ {
			ts = ts*10 + int64(user[i]-'0')
		}
		if !time.Unix(ts, 0).After(at) {
			return false
		}
		m := hmac.New(sha1.New, []byte(key))
		m.Write([]byte(user))
		return hmac.Equal([]byte(base64.StdEncoding.EncodeToString(m.Sum(nil))), []byte(cred))
	}
	if !check(s.Username, s.Credential, now.Add(59*time.Minute), secret) {
		t.Error("a fresh credential is refused")
	}
	if check(s.Username, s.Credential, now.Add(61*time.Minute), secret) {
		t.Error("an expired credential is accepted")
	}
	if check(s.Username, s.Credential, now, "another-secret") {
		t.Error("a credential verifies under the wrong secret")
	}
	if check("1999999999:7", s.Credential, now, secret) {
		t.Error("a credential verifies after its expiry was extended")
	}
	if res.TTLSeconds != 3600 || !res.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("ttl = %d, expires = %v", res.TTLSeconds, res.ExpiresAt)
	}
}

func TestServers(t *testing.T) {
	c := Config{STUN: []string{"stun:a:3478"}, TURN: []string{"turn:a:3478?transport=udp", "turns:a:5349?transport=tcp"}, Secret: "x", Now: func() time.Time { return time.Unix(10, 0) }}
	res := c.Servers("1")
	if len(res.IceServers) != 2 || res.IceServers[0].Username != "" || res.IceServers[1].Username == "" || len(res.IceServers[1].URLs) != 2 {
		t.Fatalf("servers = %+v", res.IceServers)
	}
	if res.TTLSeconds != 3600 {
		t.Fatalf("default ttl = %d", res.TTLSeconds)
	}
	// No secret: STUN only, and no credential is ever minted.
	c.Secret = ""
	res = c.Servers("1")
	if len(res.IceServers) != 1 || res.IceServers[0].Credential != "" {
		t.Fatalf("without a secret: %+v", res.IceServers)
	}
	if (Config{TURN: []string{"turn:a"}}).Enabled() {
		t.Error("TURN without a secret counts as enabled")
	}
	if !(Config{STUN: []string{"stun:a"}}).Enabled() {
		t.Error("STUN alone is not enabled")
	}
}

func TestStunFromTurn(t *testing.T) {
	got := StunFromTurn([]string{"turn:a:3478?transport=udp", "turn:a:3478?transport=tcp", "turns:a:5349?transport=tcp", "turn:b:3478"})
	if want := []string{"stun:a:3478", "stun:b:3478"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
