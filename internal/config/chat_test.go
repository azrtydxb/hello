package config

import (
	"strings"
	"testing"
)

// TestLoadChat fails if the LAN chat listener is on by default, the kill
// switch defaults off, or a bad value is accepted.
func TestLoadChat(t *testing.T) {
	c, err := LoadControl(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Chat.LANAddr != "" || !c.Chat.Enabled || c.Chat.MaxInFlight != 500 || c.Chat.MaxStreams != 2000 || len(c.Chat.TrustedProxies) != 0 {
		t.Fatalf("defaults wrong: %+v", c.Chat)
	}
	c, err = LoadControl(env(map[string]string{
		"HELLO_MSG_CHAT_LAN_ADDR": ":8087", "HELLO_MSG_CHAT_ENABLED": "false",
		"HELLO_MSG_CHAT_TRUSTED_PROXIES": "10.1.0.0/16", "HELLO_MSG_CHAT_MAX_INFLIGHT": "50", "HELLO_MSG_CHAT_MAX_STREAMS": "9",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Chat.LANAddr != ":8087" || c.Chat.Enabled || c.Chat.MaxInFlight != 50 || c.Chat.MaxStreams != 9 || len(c.Chat.TrustedProxies) != 1 {
		t.Fatalf("set values wrong: %+v", c.Chat)
	}
	for key, val := range map[string]string{
		"HELLO_MSG_CHAT_LAN_ADDR": "nonsense", "HELLO_MSG_CHAT_ENABLED": "maybe",
		"HELLO_MSG_CHAT_TRUSTED_PROXIES": "0.0.0.0/0", "HELLO_MSG_CHAT_MAX_INFLIGHT": "0",
	} {
		if _, err := LoadControl(env(map[string]string{key: val})); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s=%q: want an error naming the key, got %v", key, val, err)
		}
	}
}
