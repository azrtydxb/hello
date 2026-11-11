package config

import "testing"

// TestLoadSyslog fails if the sink is on by default, a default differs, or a
// bad address, host, rate or half a certificate pair is accepted.
func TestLoadSyslog(t *testing.T) {
	c, err := LoadControl(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Syslog.Enabled() || c.Syslog.Host != "" || c.Syslog.Rate != 20 || c.Syslog.Burst != 200 {
		t.Fatalf("defaults = %+v", c.Syslog)
	}
	c, err = LoadControl(env(map[string]string{"HELLO_SYSLOG_UDP_ADDR": ":5514", "HELLO_SYSLOG_HOST": "syslog.hello.test", "HELLO_SYSLOG_RATE": "5"}))
	if err != nil || !c.Syslog.Enabled() || c.Syslog.UDPAddr != ":5514" || c.Syslog.Host != "syslog.hello.test" || c.Syslog.Rate != 5 {
		t.Fatalf("set = %+v, %v", c.Syslog, err)
	}
	for key, m := range map[string]map[string]string{
		"HELLO_SYSLOG_UDP_ADDR": {"HELLO_SYSLOG_UDP_ADDR": "514"},
		"HELLO_SYSLOG_HOST":     {"HELLO_SYSLOG_HOST": "not a host"},
		"HELLO_SYSLOG_RATE":     {"HELLO_SYSLOG_RATE": "0"},
		"HELLO_SYSLOG_TLS_CERT": {"HELLO_SYSLOG_TLS_ADDR": ":6514"},
	} {
		wantProvErr(t, env(m), key, key)
	}
	wantProvErr(t, env(map[string]string{"HELLO_SYSLOG_TLS_KEY": "/nonexistent"}), "HELLO_SYSLOG_TLS_CERT", "key without certificate")
}
