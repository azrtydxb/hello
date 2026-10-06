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
		"MINIO_ENDPOINT":            "minio:9000",
		"MINIO_ACCESS_KEY":          "hello-minio",
		"MINIO_SECRET_KEY":          "hello-minio-" + "secret",
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

// TestNodeIDShape fails if a HELLO_NODE_ID outside the shape the cluster
// API accepts in /api/v1/cluster/nodes/{id} (1-64 characters of letters,
// digits, dots, underscores, dashes) is accepted: such a node could publish
// membership and become READY while every drain/undrain URL for it was
// rejected. Both services must refuse it.
func TestNodeIDShape(t *testing.T) {
	good := []string{"n1", "hello-sip-1", "a", strings.Repeat("x", 64), "A.b_c-d"}
	for _, id := range good {
		if _, err := LoadControl(env(map[string]string{"HELLO_NODE_ID": id})); err != nil {
			t.Fatalf("control rejected node id %q: %v", id, err)
		}
		if _, err := LoadSIP(env(map[string]string{"HELLO_NODE_ID": id})); err != nil {
			t.Fatalf("sip rejected node id %q: %v", id, err)
		}
	}
	bad := []string{"", "bad id", "bad/id", "bad+id", "x y", strings.Repeat("x", 65), "nö", "🎉"}
	for _, id := range bad {
		if _, err := LoadControl(env(map[string]string{"HELLO_NODE_ID": id})); err == nil || !strings.Contains(err.Error(), "HELLO_NODE_ID") {
			t.Fatalf("control accepted node id %q: %v", id, err)
		}
		if _, err := LoadSIP(env(map[string]string{"HELLO_NODE_ID": id})); err == nil || !strings.Contains(err.Error(), "HELLO_NODE_ID") {
			t.Fatalf("sip accepted node id %q: %v", id, err)
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
	if c.DrainTimeout != 2*time.Hour || c.MemberHeartbeat != time.Second {
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
		{"HELLO_MEMBER_HEARTBEAT", "1334ms"},
		{"HELLO_MEMBER_HEARTBEAT", "5s"},
		{"HELLO_SIP_TRUSTED_PROXIES", "0.0.0.0/0"},
		{"HELLO_SIP_TRUSTED_PROXIES", "10.0.0.0/8,::/0"},
		{"HELLO_SIP_TRUSTED_PROXIES", "10.0.0.0/8,"},
		{"HELLO_HA_TAKEOVER_POLL", "0"},
		{"HELLO_HA_TAKEOVER_POLL", "-1s"},
		{"HELLO_HA_TAKEOVER_POLL", "later"},
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
	c, err := LoadSIP(env(map[string]string{"HELLO_MEMBER_HEARTBEAT": "1333ms", "HELLO_DRAIN_TIMEOUT": "1s"}))
	if err != nil || c.MemberHeartbeat != 1333*time.Millisecond || c.DrainTimeout != time.Second {
		t.Fatalf("boundary values = %s %s, %v", c.MemberHeartbeat, c.DrainTimeout, err)
	}
}

// TestLoadHATakeover fails if the takeover defaults are wrong, a disabled
// switch is read as enabled, a non-positive poll is accepted, or the jitter
// is not parsed.
func TestLoadHATakeover(t *testing.T) {
	c, err := LoadSIP(env(nil))
	if err != nil || !c.HATakeoverEnabled || c.HATakeoverPoll != time.Second || c.HATakeoverJitter != 500*time.Millisecond {
		t.Fatalf("takeover defaults = %+v, %v", c, err)
	}
	c, err = LoadSIP(env(map[string]string{
		"HELLO_HA_TAKEOVER_ENABLED": "false", "HELLO_HA_TAKEOVER_POLL": "3s", "HELLO_HA_TAKEOVER_JITTER": "0",
	}))
	if err != nil || c.HATakeoverEnabled || c.HATakeoverPoll != 3*time.Second || c.HATakeoverJitter != 0 {
		t.Fatalf("takeover overrides = %+v, %v", c, err)
	}
}

// TestLoadMinioSmtp fails if the MinIO settings are not required together,
// if SMTP_PORT is not a port, or if SMTP_FROM is required with SMTP_HOST.
func TestLoadMinioSmtp(t *testing.T) {
	sip, err := LoadSIP(env(nil))
	if err != nil || sip.MinioEndpoint == "" || sip.MinioAccessKey == "" || sip.MinioSecretKey == "" {
		t.Fatalf("sip minio defaults missing: %+v %v", sip, err)
	}
	if _, err := LoadSIP(env(map[string]string{"MINIO_ENDPOINT": "", "MINIO_ACCESS_KEY": ""})); err == nil || !strings.Contains(err.Error(), "MINIO_ENDPOINT") {
		t.Fatalf("empty MINIO_ENDPOINT accepted: %v", err)
	}
	if _, err := LoadSIP(env(map[string]string{"MINIO_ENDPOINT": "minio:9000", "MINIO_ACCESS_KEY": "", "MINIO_SECRET_KEY": ""})); err == nil || !strings.Contains(err.Error(), "MINIO_ACCESS_KEY") {
		t.Fatalf("keys required with endpoint: %v", err)
	}
	if !sip.MinioSecure {
		t.Fatal("MINIO_SECURE should default to true (TLS); the lab sets it false explicitly")
	}
	if sip2, err := LoadSIP(env(map[string]string{"MINIO_SECURE": "false"})); err != nil || sip2.MinioSecure {
		t.Fatalf("MINIO_SECURE=false not honored: %+v %v", sip2, err)
	}
	ctl, err := LoadControl(env(nil))
	if err != nil || ctl.MinioEndpoint == "" {
		t.Fatalf("control minio: %+v %v", ctl, err)
	}
	if ctl.SmtpHost != "" {
		t.Fatalf("SMTP should default off, got %q", ctl.SmtpHost)
	}
	if _, err := LoadControl(env(map[string]string{"SMTP_HOST": "smtp", "SMTP_PORT": "notaport"})); err == nil || !strings.Contains(err.Error(), "SMTP_PORT") {
		t.Fatalf("bad SMTP_PORT accepted: %v", err)
	}
	if _, err := LoadControl(env(map[string]string{"SMTP_HOST": "smtp", "SMTP_PORT": "587"})); err == nil || !strings.Contains(err.Error(), "SMTP_FROM") {
		t.Fatalf("SMTP_FROM required with host: %v", err)
	}
	c, err := LoadControl(env(map[string]string{"SMTP_HOST": "smtp", "SMTP_PORT": "587", "SMTP_FROM": "hello@kw.watteel.lab"}))
	if err != nil || c.SmtpPort != 587 || c.SmtpFrom == "" {
		t.Fatalf("smtp config = %+v %v", c, err)
	}
}

// TestLoadMediaSettings fails if the RTP port range does not default to
// 20000-21000, parse, or reject an inverted or single-port range, if the
// force switch does not default to off, or if the recording notice switch
// does not default to on (spec interfaces).
func TestLoadMediaSettings(t *testing.T) {
	c, err := LoadSIP(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.RTPPortMin != 20000 || c.RTPPortMax != 21000 {
		t.Fatalf("default RTP range = %d-%d, want 20000-21000", c.RTPPortMin, c.RTPPortMax)
	}
	if c.MediaForceAnchor {
		t.Fatal("HELLO_MEDIA_FORCE_ANCHOR should default to false")
	}
	if !c.MediaRecordingNotice {
		t.Fatal("HELLO_MEDIA_RECORDING_NOTICE should default to true")
	}
	c, err = LoadSIP(env(map[string]string{
		"HELLO_RTP_PORT_MIN": "30000", "HELLO_RTP_PORT_MAX": "30100",
		"HELLO_MEDIA_FORCE_ANCHOR": "true", "HELLO_MEDIA_RECORDING_NOTICE": "false",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.RTPPortMin != 30000 || c.RTPPortMax != 30100 || !c.MediaForceAnchor || c.MediaRecordingNotice {
		t.Fatalf("media settings = %+v", c)
	}
	for _, bad := range []map[string]string{
		{"HELLO_RTP_PORT_MIN": "40000", "HELLO_RTP_PORT_MAX": "39000"},
		{"HELLO_RTP_PORT_MIN": "50000", "HELLO_RTP_PORT_MAX": "50000"},
		{"HELLO_RTP_PORT_MIN": "0", "HELLO_RTP_PORT_MAX": "60000"},
		{"HELLO_RTP_PORT_MAX": "70000"},
		{"HELLO_RTP_PORT_MIN": "junk"},
	} {
		if _, err := LoadSIP(env(bad)); err == nil || !strings.Contains(err.Error(), "HELLO_RTP_PORT") {
			t.Fatalf("%v accepted: %v", bad, err)
		}
	}
}

// TestLoadMediaAnchorHost fails if HELLO_MEDIA_ANCHOR_HOST is not picked up,
// or accepted when it is not an IPv4 literal — the SDP answer must carry a
// routable host, and media.NewAnchor refuses anything else at runtime.
func TestLoadMediaAnchorHost(t *testing.T) {
	c, err := LoadSIP(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.MediaAnchorHost != "" {
		t.Fatalf("default anchor host = %q, want empty", c.MediaAnchorHost)
	}
	c, err = LoadSIP(env(map[string]string{"HELLO_MEDIA_ANCHOR_HOST": "192.0.2.10"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.MediaAnchorHost != "192.0.2.10" {
		t.Fatalf("anchor host = %q, want 192.0.2.10", c.MediaAnchorHost)
	}
	for _, bad := range []string{"anchor.example", "fd00::1", "192.0.2.10:5060"} {
		if _, err := LoadSIP(env(map[string]string{"HELLO_MEDIA_ANCHOR_HOST": bad})); err == nil || !strings.Contains(err.Error(), "HELLO_MEDIA_ANCHOR_HOST") {
			t.Fatalf("%q accepted: %v", bad, err)
		}
	}
}
