package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadMissingRequired(t *testing.T) {
	_, err := LoadControl(env(map[string]string{"HELLO_NODE_ID": "c1"}))
	if err == nil || !strings.Contains(err.Error(), "HELLO_DATABASE_URL") {
		t.Fatalf("want error naming HELLO_DATABASE_URL, got %v", err)
	}
	_, err = LoadSIP(env(map[string]string{"HELLO_VALKEY_ADDR": "v:6379", "HELLO_SIP_ADVERTISED_ADDR": "a:5060"}))
	if err == nil || !strings.Contains(err.Error(), "HELLO_NODE_ID") {
		t.Fatalf("want error naming HELLO_NODE_ID, got %v", err)
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
	base := map[string]string{"HELLO_NODE_ID": "s1", "HELLO_VALKEY_ADDR": "v:6379"}
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
		m := map[string]string{"HELLO_NODE_ID": "s1", "HELLO_VALKEY_ADDR": "v:6379"}
		for k, v := range bad {
			m[k] = v
		}
		if _, err := LoadSIP(env(m)); err == nil {
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

func TestLoadDefaults(t *testing.T) {
	c, err := LoadControl(env(map[string]string{"HELLO_NODE_ID": "c1", "HELLO_DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8081" || c.ShutdownTimeout.Seconds() != 30 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}
