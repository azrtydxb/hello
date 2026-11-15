// WebRTC node status (clients-webrtc-mobile-video S-52): each hello-sip node
// that runs WebRTC media publishes its live browser legs (ICE and DTLS state),
// its last ICE and DTLS failures and its counters under one key with a short
// TTL, so a node that dies drops out on its own. hello-control reads every
// node's for GET /api/v1/webrtc/status and the Diagnostics "Clients" tab.
package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/valkey-io/valkey-go"
)

const webrtcPrefix = "hello:webrtc:node:"

// WebRTCLegsCap and WebRTCFailuresCap bound what a node publishes.
const (
	WebRTCLegsCap     = 100
	WebRTCFailuresCap = 20
)

// WebRTCLeg is one live leg on a node's shared WebRTC port.
type WebRTCLeg struct {
	// Call is the caller leg's SIP Call-ID; Side caller or callee.
	Call string `json:"call"`
	Side string `json:"side"`
	// Ringing is an alternate callee leg offered to a browser that has not
	// answered.
	Ringing bool `json:"ringing"`
	// ICE: checking, connected, failed. DTLS: new, connecting, connected,
	// failed, with DTLSReason (timeout, fingerprint, failed).
	ICE         string `json:"ice"`
	DTLS        string `json:"dtls"`
	DTLSReason  string `json:"dtlsReason,omitempty"`
	DTLSMillis  int64  `json:"dtlsMs,omitempty"`
	ICERestarts int    `json:"iceRestarts"`
	// Remote is the address ICE selected (ip:port).
	Remote           string    `json:"remote,omitempty"`
	Video            bool      `json:"video"`
	SRTPAuthFailures uint64    `json:"srtpAuthFailures"`
	Since            time.Time `json:"since"`
}

// WebRTCFailure is one recent ICE or DTLS failure on a node.
type WebRTCFailure struct {
	At     time.Time `json:"at"`
	Call   string    `json:"call"`
	Side   string    `json:"side"`
	Kind   string    `json:"kind"`   // ice or dtls
	Reason string    `json:"reason"` // failed, timeout, fingerprint
	Detail string    `json:"detail"`
}

// WebRTCCounters are a node's counters since it started.
type WebRTCCounters struct {
	ICE              map[string]uint64 `json:"ice"`
	DTLSHandshakes   uint64            `json:"dtlsHandshakes"`
	DTLSFailures     map[string]uint64 `json:"dtlsFailures"`
	SRTPAuthFailures map[string]uint64 `json:"srtpAuthFailures"`
}

// WebRTCNodeStatus is what one node publishes.
type WebRTCNodeStatus struct {
	Node string `json:"node"`
	// Port is the shared UDP port and Hosts the candidate addresses.
	Port      int             `json:"port"`
	Hosts     []string        `json:"hosts"`
	Legs      []WebRTCLeg     `json:"legs"`
	Failures  []WebRTCFailure `json:"failures"`
	Counters  WebRTCCounters  `json:"counters"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// PutWebRTCStatus publishes st for its node; it expires after ttl unless
// refreshed. Legs and failures beyond the caps are dropped.
func (s *Store) PutWebRTCStatus(ctx context.Context, st WebRTCNodeStatus, ttl time.Duration) error {
	if len(st.Legs) > WebRTCLegsCap {
		st.Legs = st.Legs[:WebRTCLegsCap]
	}
	if len(st.Failures) > WebRTCFailuresCap {
		st.Failures = st.Failures[:WebRTCFailuresCap]
	}
	v, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := s.c.Do(ctx, s.c.B().Set().Key(webrtcPrefix+st.Node).Value(string(v)).Px(ttl).Build()).Error(); err != nil {
		return fmt.Errorf("livestate: put webrtc status: %w", err)
	}
	return nil
}

// DeleteWebRTCStatus removes a node's status (a clean shutdown).
func (s *Store) DeleteWebRTCStatus(ctx context.Context, node string) error {
	return s.c.Do(ctx, s.c.B().Del().Key(webrtcPrefix+node).Build()).Error()
}

// WebRTCStatuses returns every node's published status, by node id.
func (s *Store) WebRTCStatuses(ctx context.Context) ([]WebRTCNodeStatus, error) {
	keys, err := s.scan(ctx, webrtcPrefix+"*")
	if err != nil {
		return nil, err
	}
	out := make([]WebRTCNodeStatus, 0, len(keys))
	for _, k := range keys {
		v, err := s.c.Do(ctx, s.c.B().Get().Key(k).Build()).ToString()
		if valkey.IsValkeyNil(err) {
			continue // expired between SCAN and GET
		}
		if err != nil {
			return nil, fmt.Errorf("livestate: get webrtc status: %w", err)
		}
		var st WebRTCNodeStatus
		if err := json.Unmarshal([]byte(v), &st); err != nil {
			return nil, fmt.Errorf("livestate: decode webrtc status: %w", err)
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}
