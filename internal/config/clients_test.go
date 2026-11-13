package config

import (
	"strings"
	"testing"
	"time"
)

const (
	clientKey = "0123456789abcdef0123456789abcdef" // 32 bytes
	turnKey   = "turn-shared-secret-0123"
)

// TestLoadClientsDefaults fails if web devices are on without a key, or the
// defaults (5 devices, 10 contacts, an hour of TURN) change.
func TestLoadClientsDefaults(t *testing.T) {
	c, err := LoadControl(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Clients.AuthKey != "" || c.Clients.MaxDevices != 5 || c.Clients.TURNTTL != time.Hour || len(c.Clients.TURNURLs) != 0 {
		t.Fatalf("control clients = %+v", c.Clients)
	}
	s, err := LoadSIP(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.ClientAuthKey != "" || s.MaxContacts != 10 {
		t.Fatalf("sip: key %q contacts %d", s.ClientAuthKey, s.MaxContacts)
	}
}

// TestLoadClients fails if the keys, the TURN servers and the caps are not
// read, or if a short key, a TURN server without its secret, a previous key
// without a current one or a malformed URI is accepted.
func TestLoadClients(t *testing.T) {
	c, err := LoadControl(env(map[string]string{
		"HELLO_CLIENT_AUTH_KEY": clientKey, "HELLO_CLIENT_AUTH_KEY_PREVIOUS": clientKey + "x", "HELLO_CLIENT_MAX_DEVICES": "3",
		"HELLO_WSS_URL": "wss://hello.test/ws", "HELLO_STUN_URLS": "stun:a:3478",
		"HELLO_TURN_URLS": "turn:a:3478?transport=udp, turns:a:5349?transport=tcp", "HELLO_TURN_SECRET": turnKey, "HELLO_TURN_TTL": "30m",
	}))
	if err != nil {
		t.Fatal(err)
	}
	cl := c.Clients
	if cl.AuthKey != clientKey || cl.AuthKeyPrevious != clientKey+"x" || cl.MaxDevices != 3 || cl.WSSURL != "wss://hello.test/ws" ||
		len(cl.TURNURLs) != 2 || cl.TURNURLs[1] != "turns:a:5349?transport=tcp" || cl.STUNURLs[0] != "stun:a:3478" ||
		cl.TURNSecret != turnKey || cl.TURNTTL != 30*time.Minute {
		t.Fatalf("clients = %+v", cl)
	}
	s, err := LoadSIP(env(map[string]string{"HELLO_CLIENT_AUTH_KEY": clientKey, "HELLO_CLIENT_AUTH_KEY_PREVIOUS": clientKey + "x", "HELLO_SIP_MAX_CONTACTS": "20"}))
	if err != nil || s.ClientAuthKey != clientKey || s.ClientAuthKeyPrevious != clientKey+"x" || s.MaxContacts != 20 {
		t.Fatalf("sip = %+v, %v", s, err)
	}

	for name, m := range map[string]struct {
		env  map[string]string
		want string
	}{
		"short key":              {map[string]string{"HELLO_CLIENT_AUTH_KEY": "short"}, "HELLO_CLIENT_AUTH_KEY"},
		"previous without key":   {map[string]string{"HELLO_CLIENT_AUTH_KEY_PREVIOUS": clientKey}, "HELLO_CLIENT_AUTH_KEY_PREVIOUS"},
		"turn without secret":    {map[string]string{"HELLO_TURN_URLS": "turn:a:3478"}, "HELLO_TURN_SECRET"},
		"turn with short secret": {map[string]string{"HELLO_TURN_URLS": "turn:a:3478", "HELLO_TURN_SECRET": "x"}, "HELLO_TURN_SECRET"},
		"bad scheme":             {map[string]string{"HELLO_TURN_URLS": "http://a", "HELLO_TURN_SECRET": turnKey}, "HELLO_TURN_URLS"},
		"bad stun":               {map[string]string{"HELLO_STUN_URLS": "turn:a"}, "HELLO_STUN_URLS"},
		"bad wss":                {map[string]string{"HELLO_WSS_URL": "https://a"}, "HELLO_WSS_URL"},
		"zero devices":           {map[string]string{"HELLO_CLIENT_MAX_DEVICES": "0"}, "HELLO_CLIENT_MAX_DEVICES"},
		"tiny ttl":               {map[string]string{"HELLO_TURN_TTL": "1s"}, "HELLO_TURN_TTL"},
	} {
		_, err := LoadControl(env(m.env))
		if err == nil || !strings.Contains(err.Error(), m.want) {
			t.Errorf("%s: err = %v, want it to name %s", name, err, m.want)
		}
		if err != nil && (strings.Contains(err.Error(), turnKey) || strings.Contains(err.Error(), clientKey)) {
			t.Errorf("%s: the error echoes a secret: %v", name, err)
		}
	}
	if _, err := LoadSIP(env(map[string]string{"HELLO_SIP_MAX_CONTACTS": "0"})); err == nil {
		t.Error("zero max contacts accepted")
	}
}
