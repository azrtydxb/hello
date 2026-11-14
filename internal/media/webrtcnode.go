package media

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// WebRTCNode is a node's WebRTC media side: the shared UDP port and the
// candidate addresses it advertises (S-4). It is made once at start when
// HELLO_MEDIA_WEBRTC_ENABLED is on.
type WebRTCNode struct {
	mux   *Mux
	hosts []string
}

// StartWebRTC binds the shared port (HELLO_MEDIA_WEBRTC_PORT) and fixes the
// candidate addresses: host is the node address (HELLO_MEDIA_ANCHOR_HOST),
// publicIP the optional extra address for remote clients
// (HELLO_MEDIA_WEBRTC_PUBLIC_IP). Both must be IPv4 literals; a WebRTC leg
// cannot be answered without a reachable node address.
func StartWebRTC(port int, host, publicIP string) (*WebRTCNode, error) {
	hosts := []string{}
	for _, h := range []string{host, publicIP} {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("media: WebRTC candidate address %q is not an IPv4 address", h)
		}
		if len(hosts) == 1 && hosts[0] == h {
			continue
		}
		hosts = append(hosts, h)
	}
	if host == "" {
		return nil, errors.New("media: WebRTC needs HELLO_MEDIA_ANCHOR_HOST, the node address clients send media to")
	}
	m, err := ListenMux(port)
	if err != nil {
		return nil, err
	}
	return &WebRTCNode{mux: m, hosts: hosts}, nil
}

// Mux is the shared-port mux legs attach to (Relay.AddMuxLeg).
func (n *WebRTCNode) Mux() *Mux { return n.mux }

// Hosts are the candidate addresses, the node address first.
func (n *WebRTCNode) Hosts() []string { return append([]string(nil), n.hosts...) }

// Local fills Hello's side of a WebRTC SDP for one leg: the node's port and
// addresses with the leg's ICE credentials, fingerprint and DTLS role.
func (n *WebRTCNode) Local(ufrag, pwd, fingerprint, setup string) WebRTCLocal {
	return WebRTCLocal{Ufrag: ufrag, Pwd: pwd, Hosts: n.Hosts(), Port: n.mux.Port(), Fingerprint: fingerprint, Setup: setup}
}

// Close closes the shared port and every leg on it.
func (n *WebRTCNode) Close() error { return n.mux.Close() }

// Register exposes the node's mux series on reg.
func (n *WebRTCNode) Register(reg prometheus.Registerer) {
	registerVideoMetrics(reg)
	reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "hello_webrtc_mux_legs", Help: "WebRTC legs on this node's shared UDP port.",
		}, func() float64 { return float64(n.mux.Stats().Endpoints) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "hello_webrtc_mux_unrouted_total", Help: "Datagrams on the WebRTC port that no leg owns.",
		}, func() float64 { return float64(n.mux.Stats().Unrouted) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "hello_webrtc_mux_queue_drops_total", Help: "Datagrams dropped because a leg's queue was full.",
		}, func() float64 { return float64(n.mux.Stats().QueueDrops) }),
	)
}

var webrtcNode struct {
	mu sync.RWMutex
	n  *WebRTCNode
}

// SetWebRTCNode makes n the node's WebRTC side for the process (nil turns
// WebRTC off); the call code reads it with CurrentWebRTC.
func SetWebRTCNode(n *WebRTCNode) {
	webrtcNode.mu.Lock()
	webrtcNode.n = n
	webrtcNode.mu.Unlock()
}

// CurrentWebRTC returns the node's WebRTC side, nil when WebRTC is off.
func CurrentWebRTC() *WebRTCNode {
	webrtcNode.mu.RLock()
	defer webrtcNode.mu.RUnlock()
	return webrtcNode.n
}
