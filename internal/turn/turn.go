// Package turn builds the ICE server list a WebRTC client needs: STUN and
// TURN URIs with short-lived TURN credentials (clients-webrtc-mobile-video
// S-23). The credentials follow the TURN REST API scheme coturn calls
// use-auth-secret: the username is "<expiryUnix>:<name>" and the credential
// is base64(HMAC-SHA1(sharedSecret, username)). coturn holds the same shared
// secret and recomputes the credential, so nothing is stored or looked up
// per credential.
package turn

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // The TURN REST API scheme (and coturn) define the credential as HMAC-SHA1.
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// DefaultTTL is how long a credential is valid.
const DefaultTTL = time.Hour

// Server is one entry of an RTCPeerConnection iceServers list.
type Server struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Config is the deployment's ICE settings.
type Config struct {
	// STUN and TURN are the URIs handed to clients (stun:host:port,
	// turn:host:port?transport=udp, turns:host:port?transport=tcp).
	STUN []string
	TURN []string
	// Secret is coturn's static-auth-secret. Empty turns TURN off.
	Secret string
	// TTL is the credential lifetime (DefaultTTL when zero).
	TTL time.Duration
	Now func() time.Time
}

// Enabled reports whether there is anything to hand out.
func (c Config) Enabled() bool { return len(c.STUN) > 0 || (len(c.TURN) > 0 && c.Secret != "") }

func (c Config) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultTTL
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Credential returns the TURN username and credential for name, valid until
// expires. name carries no secret: it only labels the credential in coturn's
// logs.
func Credential(secret, name string, expires time.Time) (username, credential string) {
	username = strconv.FormatInt(expires.Unix(), 10)
	if name != "" {
		username += ":" + name
	}
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(username))
	return username, base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// Result is the endpoint's answer.
type Result struct {
	IceServers []Server  `json:"iceServers"`
	ExpiresAt  time.Time `json:"expiresAt"`
	TTLSeconds int       `json:"ttlSeconds"`
}

// Servers returns the ICE servers for the client name (a device id), with
// fresh TURN credentials.
func (c Config) Servers(name string) Result {
	exp := c.now().Add(c.ttl())
	out := Result{IceServers: []Server{}, ExpiresAt: exp.UTC(), TTLSeconds: int(c.ttl() / time.Second)}
	if len(c.STUN) > 0 {
		out.IceServers = append(out.IceServers, Server{URLs: append([]string{}, c.STUN...)})
	}
	if len(c.TURN) > 0 && c.Secret != "" {
		u, p := Credential(c.Secret, name, exp)
		out.IceServers = append(out.IceServers, Server{URLs: append([]string{}, c.TURN...), Username: u, Credential: p})
	}
	return out
}

// StunFromTurn derives the STUN URI of each distinct TURN host and port:
// turn:h:3478?transport=udp gives stun:h:3478. A turns: URI is skipped (TLS
// listeners do not serve plain STUN).
func StunFromTurn(turnURIs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range turnURIs {
		rest, ok := strings.CutPrefix(u, "turn:")
		if !ok {
			continue
		}
		hp, _, _ := strings.Cut(rest, "?")
		if !seen[hp] {
			seen[hp] = true
			out = append(out, "stun:"+hp)
		}
	}
	return out
}
