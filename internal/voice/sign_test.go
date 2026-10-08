package voice

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/config"
)

// configVoice builds a config.Voice from the given secrets.
func configVoice(secrets []string) config.Voice {
	v := config.Voice{SIPAddress: "agent:5060", Tenant: "lab"}
	if len(secrets) > 0 {
		v.Secret = secrets[0]
	}
	if len(secrets) > 1 {
		v.SecretNext = secrets[1]
	}
	return v
}

// vectorsFile is the shared signature vector file: talking-agent's repo
// holds a copy, so both sides prove the same wire format (spec Constraints).
type vectorsFile struct {
	Version string `json:"version"`
	Keys    []struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	} `json:"keys"`
	Vectors []struct {
		Tenant      string `json:"tenant"`
		Agent       string `json:"agent"`
		SIPUser     string `json:"sipUser"`
		Correlation string `json:"correlation"`
		Ts          int64  `json:"ts"`
		KeyID       string `json:"keyId"`
		AuthHeader  string `json:"authHeader"`
	} `json:"vectors"`
}

// loadVectors reads internal/voice/testdata/voice_auth_vectors.json.
func loadVectors(t *testing.T) vectorsFile {
	t.Helper()
	b, err := os.ReadFile("testdata/voice_auth_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var f vectorsFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestVoiceCallAuth proves the X-Hello-Auth contract (S-16, shared contract
// 4): every vector signs back to its header, verifies against both keys,
// and fails under a tampered field, a tampered header, an unknown key, a
// timestamp outside the skew or a replayed correlation id. It also fails if
// Key derivation or Keys' rotation order changes.
func TestVoiceCallAuth(t *testing.T) {
	f := loadVectors(t)
	if f.Version != Version {
		t.Fatalf("vectors version %q, want %q", f.Version, Version)
	}
	keys := make([]Key, len(f.Keys))
	derivable := map[string]bool{}
	for i, k := range f.Keys {
		keys[i] = Key{ID: k.ID, Secret: []byte(k.Secret)}
		derivable[NewKey(k.Secret).ID] = true
		if NewKey(k.Secret).ID != k.ID {
			t.Fatalf("key id %q is not the derivation of its secret", k.ID)
		}
	}
	if len(derivable) < 2 {
		t.Fatal("the two secrets derive the same key id")
	}
	for i, v := range f.Vectors {
		ts := time.Unix(v.Ts, 0)
		now := ts
		key := keys[0]
		if got := Sign(key, v.Tenant, v.Agent, v.SIPUser, v.Correlation, ts); got != v.AuthHeader {
			t.Errorf("vector %d: Sign = %q, want %q", i, got, v.AuthHeader)
		}
		if err := Verify(keys, v.AuthHeader, now, v.Tenant, v.Agent, v.SIPUser, v.Correlation, ts); err != nil {
			t.Errorf("vector %d: Verify = %v, want nil", i, err)
		}
		// A tampered field never verifies.
		for _, mut := range []struct{ what, tenant, agent, sipUser, correlation string }{
			{"tenant", "other" + v.Tenant, v.Agent, v.SIPUser, v.Correlation},
			{"agent", v.Tenant, "other" + v.Agent, v.SIPUser, v.Correlation},
			{"sip user", v.Tenant, v.Agent, "other" + v.SIPUser, v.Correlation},
			{"correlation", v.Tenant, v.Agent, v.SIPUser, "other" + v.Correlation},
		} {
			err := Verify(keys, v.AuthHeader, now, mut.tenant, mut.agent, mut.sipUser, mut.correlation, ts)
			if err == nil {
				t.Errorf("vector %d: a changed %s verified", i, mut.what)
			}
		}
	}

	bad := []struct{ name, header string }{
		{"no v1", "v2:" + f.Keys[0].ID + ":" + strings.Repeat("a", 64)},
		{"two colons", "v1::" + strings.Repeat("a", 64)},
		{"short mac", "v1:" + f.Keys[0].ID + ":abcd"},
		{"upper hex", "v1:" + f.Keys[0].ID + ":" + strings.ToUpper(strings.Repeat("a", 64))},
		{"non hex", "v1:" + f.Keys[0].ID + ":" + strings.Repeat("z", 64)},
		{"unknown key id", "v1:deadbeef:" + strings.Repeat("a", 64)},
	}
	now := time.Unix(1767225600, 0)
	for _, tc := range bad {
		if err := Verify(keys, tc.header, now, "t", "a", "1", "c", now); err == nil {
			t.Errorf("%s verified", tc.name)
		}
	}
	if _, err := ParseTs("nope"); err == nil {
		t.Error("ParseTs accepted letters")
	}

	// Skew: MaxSkew exactly is allowed, one second more is not.
	v := f.Vectors[0]
	ts := time.Unix(v.Ts, 0)
	if err := Verify(keys, v.AuthHeader, ts.Add(MaxSkew), v.Tenant, v.Agent, v.SIPUser, v.Correlation, ts); err != nil {
		t.Errorf("at MaxSkew: %v", err)
	}
	if err := Verify(keys, v.AuthHeader, ts.Add(MaxSkew+time.Second), v.Tenant, v.Agent, v.SIPUser, v.Correlation, ts); !errors.Is(err, ErrSkew) {
		t.Errorf("past MaxSkew: %v, want ErrSkew", err)
	}

	// Two keys: a header signed with the rotation key verifies beside the
	// current one, with the rotation key alone as the signing key.
	next := NewKey(f.Keys[1].Secret)
	h := Sign(next, v.Tenant, v.Agent, v.SIPUser, v.Correlation, now)
	if err := Verify(keys, h, now, v.Tenant, v.Agent, v.SIPUser, v.Correlation, now); err != nil {
		t.Errorf("rotation key refused: %v", err)
	}
	if err := Verify(keys[:1], h, now, v.Tenant, v.Agent, v.SIPUser, v.Correlation, now); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("without the rotation key: %v, want ErrUnknownKey", err)
	}

	// Replay: the guard refuses the same correlation id inside the window.
	var g ReplayGuard
	if err := g.Check("corr-1", now); err != nil {
		t.Fatalf("first Check: %v", err)
	}
	if err := g.Check("corr-1", now.Add(time.Second)); !errors.Is(err, ErrReplay) {
		t.Errorf("replay = %v, want ErrReplay", err)
	}
	if err := g.Check("corr-1", now.Add(ReplayWindow+time.Second)); err != nil {
		t.Errorf("after the window: %v", err)
	}
}

// TestKeysFromConfig fails if Keys returns the wrong signing and accepted
// keys for a configuration, or keys without a secret.
func TestKeysFromConfig(t *testing.T) {
	f := loadVectors(t)
	k1 := NewKey(f.Keys[0].Secret)
	k2 := NewKey(f.Keys[1].Secret)
	if _, _, ok := Keys(configVoice(nil)); ok {
		t.Error("keys without a secret")
	}
	sign, accept, ok := Keys(configVoice([]string{f.Keys[0].Secret}))
	if !ok || sign.ID != k1.ID || len(accept) != 1 {
		t.Fatalf("one secret: sign %q accept %d ok %v", sign.ID, len(accept), ok)
	}
	sign, accept, ok = Keys(configVoice([]string{f.Keys[0].Secret, f.Keys[1].Secret}))
	if !ok || sign.ID != k1.ID || len(accept) != 2 || accept[1].ID != k2.ID {
		t.Fatalf("two secrets: sign %q accept %d ok %v", sign.ID, len(accept), ok)
	}
}
