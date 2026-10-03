package config

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// env layers m over a complete valid environment for both services, so a
// test only states the keys it is about; an empty value in m unsets a key.
func env(m map[string]string) func(string) string {
	base := map[string]string{
		"HELLO_NODE_ID": "n1", "HELLO_DATABASE_URL": "postgres://db/hello", "HELLO_VALKEY_ADDR": "v:6379",
		"HELLO_SIP_DOMAIN": "hello.test", "HELLO_SIP_NONCE_SECRET": strings.Repeat("k", 32),
		"HELLO_SIP_ADVERTISED_ADDR": "10.0.0.5:5060",
		"HELLO_SECRET_KEY":          base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))),
	}
	return func(k string) string {
		if v, ok := m[k]; ok {
			return v
		}
		return base[k]
	}
}

func TestLoadMissingRequired(t *testing.T) {
	for _, key := range []string{"HELLO_NODE_ID", "HELLO_DATABASE_URL", "HELLO_VALKEY_ADDR", "HELLO_SIP_DOMAIN"} {
		if _, err := LoadControl(env(map[string]string{key: ""})); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("control without %s: want error naming it, got %v", key, err)
		}
	}
	for _, key := range []string{"HELLO_NODE_ID", "HELLO_DATABASE_URL", "HELLO_VALKEY_ADDR", "HELLO_SIP_DOMAIN", "HELLO_SIP_NONCE_SECRET"} {
		if _, err := LoadSIP(env(map[string]string{key: ""})); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("sip without %s: want error naming it, got %v", key, err)
		}
	}
	if _, err := LoadSIP(env(map[string]string{"HELLO_SIP_NONCE_SECRET": "short"})); err == nil || !strings.Contains(err.Error(), "HELLO_SIP_NONCE_SECRET") {
		t.Fatalf("short nonce secret accepted: %v", err)
	}
}

func TestLoadBadDuration(t *testing.T) {
	for _, v := range []string{"30", "-1s"} {
		_, err := LoadControl(env(map[string]string{
			"HELLO_NODE_ID": "c1", "HELLO_DATABASE_URL": "postgres://x", "HELLO_SHUTDOWN_TIMEOUT": v,
		}))
		if err == nil || !strings.Contains(err.Error(), "HELLO_SHUTDOWN_TIMEOUT") {
			t.Fatalf("%q: want error naming HELLO_SHUTDOWN_TIMEOUT, got %v", v, err)
		}
	}
}

func TestLoadUnspecifiedAdvertise(t *testing.T) {
	base := map[string]string{"HELLO_SIP_ADVERTISED_ADDR": ""}
	for _, bind := range []string{"", "0.0.0.0:5060", "[::]:5060", ":5060"} {
		base["HELLO_SIP_BIND_ADDR"] = bind
		_, err := LoadSIP(env(base))
		if err == nil || !strings.Contains(err.Error(), "HELLO_SIP_ADVERTISED_ADDR") {
			t.Fatalf("bind %q: want error naming HELLO_SIP_ADVERTISED_ADDR, got %v", bind, err)
		}
	}
	for _, bad := range []map[string]string{
		{"HELLO_SIP_BIND_ADDR": "garbage", "HELLO_SIP_ADVERTISED_ADDR": "10.0.0.5:5060"},
		{"HELLO_SIP_BIND_ADDR": "0.0.0.0:5060", "HELLO_SIP_ADVERTISED_ADDR": "0.0.0.0:5060"},
		{"HELLO_SIP_BIND_ADDR": "0.0.0.0:5060", "HELLO_SIP_ADVERTISED_ADDR": "sip.example.com"},
	} {
		if _, err := LoadSIP(env(bad)); err == nil {
			t.Fatalf("%v: want error, got nil", bad)
		}
	}
	base["HELLO_SIP_BIND_ADDR"] = "10.0.0.5:5060"
	c, err := LoadSIP(env(base))
	if err != nil || c.SIPAdvertisedAddr != "10.0.0.5:5060" {
		t.Fatalf("specific bind should default advertised: %+v %v", c, err)
	}
}

func TestLoadMalformedDatabaseURL(t *testing.T) {
	_, err := LoadControl(env(map[string]string{
		"HELLO_NODE_ID": "c1", "HELLO_DATABASE_URL": "postgres://u:s3cret@db:notaport/x",
	}))
	if err == nil || !strings.Contains(err.Error(), "HELLO_DATABASE_URL") {
		t.Fatalf("want error naming HELLO_DATABASE_URL, got %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestLoadMalformedValkeyAddr(t *testing.T) {
	_, err := LoadSIP(env(map[string]string{
		"HELLO_VALKEY_ADDR": "valkey",
	}))
	if err == nil || !strings.Contains(err.Error(), "HELLO_VALKEY_ADDR") {
		t.Fatalf("want error naming HELLO_VALKEY_ADDR, got %v", err)
	}
}

func TestLoadSecretKey(t *testing.T) {
	urlSafe33 := base64.URLEncoding.EncodeToString(append(bytes.Repeat([]byte{0xfb}, 32), 0xff)) // 33 bytes, '-' and '_'
	zero := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, bad := range []string{"", "not-base64!", base64.StdEncoding.EncodeToString([]byte("short")), urlSafe33, zero} {
		_, errC := LoadControl(env(map[string]string{"HELLO_SECRET_KEY": bad}))
		_, errS := LoadSIP(env(map[string]string{"HELLO_SECRET_KEY": bad}))
		for _, err := range []error{errC, errS} {
			if err == nil || !strings.Contains(err.Error(), "HELLO_SECRET_KEY") {
				t.Fatalf("secret key %q accepted: %v", bad, err)
			}
			if bad != "" && strings.Contains(err.Error(), bad) {
				t.Fatalf("error echoes the key: %v", err)
			}
		}
	}
}

func TestLoadZeroSessionTTL(t *testing.T) {
	if _, err := LoadControl(env(map[string]string{"HELLO_SESSION_TTL": "0s"})); err == nil || !strings.Contains(err.Error(), "HELLO_SESSION_TTL") {
		t.Fatalf("zero session TTL accepted: %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := LoadControl(env(map[string]string{"HELLO_NODE_ID": "c1", "HELLO_DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8081" || c.ShutdownTimeout.Seconds() != 30 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

// TestLoadMaxCallDuration fails if HELLO_SIP_MAX_CALL_DURATION does not
// default to 4h, parse, or reject zero and junk.
func TestLoadMaxCallDuration(t *testing.T) {
	c, err := LoadSIP(env(nil))
	if err != nil || c.MaxCallDuration != 4*time.Hour {
		t.Fatalf("default = %v, %v; want 4h", c.MaxCallDuration, err)
	}
	if c, err := LoadSIP(env(map[string]string{"HELLO_SIP_MAX_CALL_DURATION": "90m"})); err != nil || c.MaxCallDuration != 90*time.Minute {
		t.Fatalf("90m = %v, %v", c.MaxCallDuration, err)
	}
	for _, bad := range []string{"0s", "-1h", "forever"} {
		if _, err := LoadSIP(env(map[string]string{"HELLO_SIP_MAX_CALL_DURATION": bad})); err == nil || !strings.Contains(err.Error(), "HELLO_SIP_MAX_CALL_DURATION") {
			t.Fatalf("%q accepted: %v", bad, err)
		}
	}
}

// TestLoadValkeyTopology fails if both or neither Valkey topologies are
// accepted, if Sentinel mode lacks its master, or if a valid Sentinel list
// is not parsed.
func TestLoadValkeyTopology(t *testing.T) {
	for _, bad := range []map[string]string{
		{"HELLO_VALKEY_ADDR": ""},
		{"HELLO_VALKEY_SENTINELS": "s1:26379", "HELLO_VALKEY_MASTER": "hello"}, // plus the base ADDR: both
		{"HELLO_VALKEY_ADDR": "", "HELLO_VALKEY_SENTINELS": "s1:26379"},
		{"HELLO_VALKEY_ADDR": "", "HELLO_VALKEY_MASTER": "hello"},
		{"HELLO_VALKEY_ADDR": "", "HELLO_VALKEY_SENTINELS": "nope", "HELLO_VALKEY_MASTER": "hello"},
	} {
		if _, err := LoadSIP(env(bad)); err == nil {
			t.Fatalf("%v accepted", bad)
		}
		if _, err := LoadControl(env(bad)); err == nil {
			t.Fatalf("control: %v accepted", bad)
		}
	}
	c, err := LoadSIP(env(map[string]string{"HELLO_VALKEY_ADDR": "", "HELLO_VALKEY_SENTINELS": "s1:26379, s2:26379", "HELLO_VALKEY_MASTER": "hello"}))
	if err != nil || len(c.ValkeySentinels) != 2 || c.ValkeySentinels[1] != "s2:26379" || c.ValkeyMaster != "hello" {
		t.Fatalf("sentinel config = %+v, %v", c, err)
	}
}

// TestLoadTrustedProxies fails if a malformed CIDR is accepted or a valid
// list is not parsed and masked.
func TestLoadTrustedProxies(t *testing.T) {
	if _, err := LoadSIP(env(map[string]string{"HELLO_SIP_TRUSTED_PROXIES": "10.0.0.0/8,garbage"})); err == nil || !strings.Contains(err.Error(), "HELLO_SIP_TRUSTED_PROXIES") {
		t.Fatalf("malformed proxy CIDR accepted: %v", err)
	}
	c, err := LoadSIP(env(map[string]string{"HELLO_SIP_TRUSTED_PROXIES": "172.20.0.7/32, 10.1.2.3/16"}))
	if err != nil || len(c.TrustedProxies) != 2 || c.TrustedProxies[1].String() != "10.1.0.0/16" {
		t.Fatalf("trusted proxies = %v, %v", c.TrustedProxies, err)
	}
	if c.DrainTimeout != 2*time.Hour || c.MemberHeartbeat != 5*time.Second {
		t.Fatalf("defaults = %s %s", c.DrainTimeout, c.MemberHeartbeat)
	}
}

// TestLoadHAValues fails if a zero, negative or malformed drain timeout or
// member heartbeat is accepted, if a heartbeat longer than a third of the
// membership TTL is accepted, if an empty Sentinel entry (a trailing comma)
// is accepted, or if a /0 trusted proxy is accepted.
func TestLoadHAValues(t *testing.T) {
	for _, tc := range []struct{ key, val string }{
		{"HELLO_DRAIN_TIMEOUT", "0"},
		{"HELLO_DRAIN_TIMEOUT", "0s"},
		{"HELLO_DRAIN_TIMEOUT", "-1m"},
		{"HELLO_DRAIN_TIMEOUT", "soon"},
		{"HELLO_MEMBER_HEARTBEAT", "0"},
		{"HELLO_MEMBER_HEARTBEAT", "-5s"},
		{"HELLO_MEMBER_HEARTBEAT", "often"},
		{"HELLO_MEMBER_HEARTBEAT", "5001ms"},
		{"HELLO_MEMBER_HEARTBEAT", "10s"},
		{"HELLO_SIP_TRUSTED_PROXIES", "0.0.0.0/0"},
		{"HELLO_SIP_TRUSTED_PROXIES", "10.0.0.0/8,::/0"},
		{"HELLO_SIP_TRUSTED_PROXIES", "10.0.0.0/8,"},
	} {
		if _, err := LoadSIP(env(map[string]string{tc.key: tc.val})); err == nil || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s=%q: want error naming it, got %v", tc.key, tc.val, err)
		}
	}
	for _, v := range []string{"s1:26379,", ",s1:26379", "s1:26379,,s2:26379", " , "} {
		e := env(map[string]string{"HELLO_VALKEY_ADDR": "", "HELLO_VALKEY_SENTINELS": v, "HELLO_VALKEY_MASTER": "hello"})
		if _, err := LoadSIP(e); err == nil || !strings.Contains(err.Error(), "HELLO_VALKEY_SENTINELS") {
			t.Errorf("sip sentinels %q: want error, got %v", v, err)
		}
		if _, err := LoadControl(e); err == nil || !strings.Contains(err.Error(), "HELLO_VALKEY_SENTINELS") {
			t.Errorf("control sentinels %q: want error, got %v", v, err)
		}
	}
	c, err := LoadSIP(env(map[string]string{"HELLO_MEMBER_HEARTBEAT": "5s", "HELLO_DRAIN_TIMEOUT": "1s"}))
	if err != nil || c.MemberHeartbeat != 5*time.Second || c.DrainTimeout != time.Second {
		t.Fatalf("boundary values = %s %s, %v", c.MemberHeartbeat, c.DrainTimeout, err)
	}
}
