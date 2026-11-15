package media

// WebRTC leg diagnostics and series (clients-webrtc-mobile-video S-52): the
// ICE and DTLS state of every live leg on the node's shared WebRTC port, a
// short history of the legs that failed, and the counters behind
// hello_webrtc_legs, hello_webrtc_ice_state_total,
// hello_webrtc_dtls_handshake_seconds, hello_webrtc_dtls_failures_total and
// hello_srtp_auth_failures_total. Every label is a word from a fixed list;
// no call or device id is ever one. The per-leg view is what the node
// publishes to live state for the Diagnostics "Clients" tab.

import (
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// What a Secure tells its watcher about (Secure.Watch).
const (
	watchICE  = "ice"
	watchDTLS = "dtls"
)

// secWatch is told about an endpoint's ICE and DTLS events: kind is watchICE
// (offered, restarted, connected) or watchDTLS (started, ok, timeout,
// fingerprint, failed); d is the handshake's duration for a DTLS outcome. It
// runs on the endpoint's goroutines, sometimes under its lock, so it must not
// call back into the endpoint.
type secWatch func(kind, result string, d time.Duration)

// Watch registers fn for the endpoint's ICE and DTLS events (nil stops).
func (s *Secure) Watch(fn func(kind, result string, d time.Duration)) {
	if fn == nil {
		s.watch.Store(nil)
		return
	}
	w := secWatch(fn)
	s.watch.Store(&w)
}

func (s *Secure) notify(kind, result string, d time.Duration) {
	if w := s.watch.Load(); w != nil {
		(*w)(kind, result, d)
	}
}

// ICE and DTLS states of a leg as the diagnostics show them.
const (
	iceChecking int32 = iota
	iceConnected
	iceFailed
)

const (
	dtlsNew int32 = iota
	dtlsConnecting
	dtlsConnected
	dtlsFailedState
)

var (
	iceStateNames  = [...]string{"checking", "connected", "failed"}
	dtlsStateNames = [...]string{"new", "connecting", "connected", "failed"}
	// The values of hello_webrtc_ice_state_total{state}.
	iceCounterNames = [...]string{"connected", "restarted", "failed"}
	// The values of hello_webrtc_dtls_failures_total{reason}.
	dtlsFailReasons = [...]string{"timeout", "fingerprint", "failed"}
	// The values of hello_srtp_auth_failures_total{direction}: the side of
	// the call the packet that failed came from.
	srtpDirections = [...]string{"from_caller", "from_callee"}
)

// iceFailGrace is how long a leg whose DTLS has started may go without an
// authenticated ICE check before its end counts as an ICE failure.
const iceFailGrace = 5 * time.Second

// webrtcSeries are the process-wide counters, read by the metric functions.
var webrtcSeries struct {
	ice      [len(iceCounterNames)]atomic.Uint64
	dtlsOK   atomic.Uint64
	dtlsFail [len(dtlsFailReasons)]atomic.Uint64
	srtpAuth [len(srtpDirections)]atomic.Uint64
}

// webrtcDTLSSeconds times the successful DTLS handshakes of WebRTC legs.
var webrtcDTLSSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "hello_webrtc_dtls_handshake_seconds",
	Help:    "Duration of successful DTLS-SRTP handshakes on WebRTC legs.",
	Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8},
})

// noteSRTPAuthFailure counts one inbound SRTP or SRTCP packet that failed
// authentication on a leg of the given side (0 caller, 1 callee).
func noteSRTPAuthFailure(side uint8) {
	if int(side) < len(webrtcSeries.srtpAuth) {
		webrtcSeries.srtpAuth[side].Add(1)
	}
}

func noteICEState(state string) {
	for i, n := range iceCounterNames {
		if n == state {
			webrtcSeries.ice[i].Add(1)
		}
	}
}

func noteWebDTLSFailure(reason string) {
	for i, n := range dtlsFailReasons {
		if n == reason {
			webrtcSeries.dtlsFail[i].Add(1)
			return
		}
	}
	webrtcSeries.dtlsFail[len(dtlsFailReasons)-1].Add(1)
}

// webTrack is one WebRTC leg's diagnostics.
type webTrack struct {
	leg      *relayLeg
	since    time.Time
	ice      atomic.Int32
	dtls     atomic.Int32
	reason   atomic.Pointer[string]
	dtlsAt   atomic.Int64 // when the current DTLS session started (unix ns)
	dtlsMs   atomic.Int64
	restarts atomic.Int32
	// iceFailCounted is true once the leg's ICE failure was counted (since
	// its last restart), so it counts once.
	iceFailCounted atomic.Bool
}

var webTracks struct {
	mu sync.Mutex
	m  map[*webTrack]struct{}
}

// trackWeb starts the diagnostics of a leg on the shared WebRTC port.
func (l *relayLeg) trackWeb() {
	t := &webTrack{leg: l, since: time.Now()}
	l.wt = t
	webTracks.mu.Lock()
	if webTracks.m == nil {
		webTracks.m = map[*webTrack]struct{}{}
	}
	webTracks.m[t] = struct{}{}
	webTracks.mu.Unlock()
	l.secure.Watch(t.event)
}

// untrackWeb ends a leg's diagnostics. failed says whether an ICE check that
// never came counts against the leg: true when its call ends, false when the
// leg is dropped because another fork answered.
func (l *relayLeg) untrackWeb(failed bool) {
	t := l.wt
	if t == nil {
		return
	}
	webTracks.mu.Lock()
	_, live := webTracks.m[t]
	delete(webTracks.m, t)
	webTracks.mu.Unlock()
	if !live {
		return
	}
	l.secure.Watch(nil)
	if !failed || l.parked.Load() || t.ice.Load() == iceConnected {
		return
	}
	at := t.dtlsAt.Load()
	if at == 0 || time.Since(time.Unix(0, at)) < iceFailGrace {
		return // the peer was never expected to check yet (no answer, or just now)
	}
	t.iceFail("no ICE check arrived")
}

// iceFail counts the leg's ICE failure once.
func (t *webTrack) iceFail(why string) {
	t.ice.Store(iceFailed)
	if t.iceFailCounted.CompareAndSwap(false, true) {
		noteICEState("failed")
		noteWebFailure(t, "ice", "failed", why)
	}
}

// event is the leg's Secure watcher.
func (t *webTrack) event(kind, result string, d time.Duration) {
	switch kind {
	case watchICE:
		switch result {
		case "restarted":
			t.restarts.Add(1)
			t.ice.Store(iceChecking)
			t.iceFailCounted.Store(false)
			noteICEState("restarted")
		case "connected":
			t.ice.Store(iceConnected)
			noteICEState("connected")
		}
	case watchDTLS:
		switch result {
		case "started":
			t.dtls.Store(dtlsConnecting)
			t.dtlsAt.Store(time.Now().UnixNano())
			t.reason.Store(nil)
		case "ok":
			t.dtls.Store(dtlsConnected)
			t.dtlsMs.Store(d.Milliseconds())
			webrtcSeries.dtlsOK.Add(1)
			webrtcDTLSSeconds.Observe(d.Seconds())
		default:
			r := result
			t.dtls.Store(dtlsFailedState)
			t.reason.Store(&r)
			noteWebDTLSFailure(result)
			noteWebFailure(t, "dtls", result, dtlsFailWhy(result))
			if t.ice.Load() != iceConnected {
				t.iceFail("no ICE check arrived before the DTLS handshake gave up")
			}
		}
	}
}

func dtlsFailWhy(reason string) string {
	switch reason {
	case "timeout":
		return "the DTLS handshake did not finish in time"
	case "fingerprint":
		return "the peer's certificate does not match the fingerprint in its SDP"
	default:
		return "the DTLS handshake failed"
	}
}

// WebRTCLeg is one live WebRTC leg as the diagnostics show it.
type WebRTCLeg struct {
	// Call is the relay's label (the caller leg's SIP Call-ID); Side is
	// caller or callee.
	Call string `json:"call"`
	Side string `json:"side"`
	// Ringing is an alternate callee leg offered to a browser that has not
	// answered yet.
	Ringing bool `json:"ringing"`
	// ICE is checking, connected or failed; DTLS new, connecting,
	// connected or failed, with DTLSReason (timeout, fingerprint, failed)
	// when it failed.
	ICE         string `json:"ice"`
	DTLS        string `json:"dtls"`
	DTLSReason  string `json:"dtlsReason,omitempty"`
	DTLSMillis  int64  `json:"dtlsMs,omitempty"`
	ICERestarts int    `json:"iceRestarts"`
	// Remote is the address ICE selected (ip:port), empty before a check.
	Remote string `json:"remote,omitempty"`
	Video  bool   `json:"video"`
	// SRTPAuthFailures counts the leg's inbound packets that failed SRTP
	// authentication.
	SRTPAuthFailures uint64    `json:"srtpAuthFailures"`
	Since            time.Time `json:"since"`
}

// WebRTCLegs returns the node's live WebRTC legs, oldest first, at most max
// (0 for all).
func WebRTCLegs(max int) []WebRTCLeg {
	webTracks.mu.Lock()
	ts := make([]*webTrack, 0, len(webTracks.m))
	for t := range webTracks.m {
		ts = append(ts, t)
	}
	webTracks.mu.Unlock()
	sort.Slice(ts, func(i, j int) bool { return ts[i].since.Before(ts[j].since) })
	if max > 0 && len(ts) > max {
		ts = ts[:max]
	}
	out := make([]WebRTCLeg, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.view())
	}
	return out
}

// WebRTCLegCount is how many WebRTC legs the node carries.
func WebRTCLegCount() int {
	webTracks.mu.Lock()
	defer webTracks.mu.Unlock()
	return len(webTracks.m)
}

func (t *webTrack) view() WebRTCLeg {
	l := t.leg
	v := WebRTCLeg{
		Call: l.r.Label(), Side: sideName(l.side), Ringing: l.parked.Load(),
		ICE: iceStateNames[t.ice.Load()], DTLS: dtlsStateNames[t.dtls.Load()],
		DTLSMillis: t.dtlsMs.Load(), ICERestarts: int(t.restarts.Load()),
		Video: l.vid.on(), SRTPAuthFailures: l.authFail.Load(), Since: t.since.UTC(),
	}
	if r := t.reason.Load(); r != nil {
		v.DTLSReason = *r
	}
	if t.ice.Load() == iceConnected {
		l.remoteMu.Lock()
		if l.remote != nil {
			v.Remote = (&net.UDPAddr{IP: l.remote.IP, Port: l.remote.Port}).String()
		}
		l.remoteMu.Unlock()
	}
	return v
}

func sideName(side uint8) string {
	if side == 0 {
		return "caller"
	}
	return "callee"
}

// WebRTCFailure is one recent ICE or DTLS failure of a WebRTC leg.
type WebRTCFailure struct {
	At   time.Time `json:"at"`
	Call string    `json:"call"`
	Side string    `json:"side"`
	// Kind is ice or dtls; Reason a word (failed, timeout, fingerprint) and
	// Detail the sentence the diagnostics show.
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// webFailuresCap is how many failures the node remembers.
const webFailuresCap = 20

var webFailures struct {
	mu   sync.Mutex
	ring []WebRTCFailure // newest last
}

func noteWebFailure(t *webTrack, kind, reason, detail string) {
	f := WebRTCFailure{At: time.Now().UTC(), Call: t.leg.r.Label(), Side: sideName(t.leg.side), Kind: kind, Reason: reason, Detail: detail}
	webFailures.mu.Lock()
	webFailures.ring = append(webFailures.ring, f)
	if len(webFailures.ring) > webFailuresCap {
		webFailures.ring = append([]WebRTCFailure(nil), webFailures.ring[len(webFailures.ring)-webFailuresCap:]...)
	}
	webFailures.mu.Unlock()
}

// WebRTCRecentFailures returns the node's last ICE and DTLS failures, newest
// first.
func WebRTCRecentFailures() []WebRTCFailure {
	webFailures.mu.Lock()
	defer webFailures.mu.Unlock()
	out := make([]WebRTCFailure, len(webFailures.ring))
	for i, f := range webFailures.ring {
		out[len(out)-1-i] = f
	}
	return out
}

// WebRTCCounters are the node's WebRTC counters since it started.
type WebRTCCounters struct {
	// ICE counts legs by state reached: connected, restarted, failed.
	ICE map[string]uint64 `json:"ice"`
	// DTLSHandshakes counts the successful handshakes, DTLSFailures the
	// failed ones by reason.
	DTLSHandshakes uint64            `json:"dtlsHandshakes"`
	DTLSFailures   map[string]uint64 `json:"dtlsFailures"`
	// SRTPAuthFailures counts packets that failed SRTP authentication on
	// any leg, by the side they came from.
	SRTPAuthFailures map[string]uint64 `json:"srtpAuthFailures"`
}

// WebRTCStats snapshots the counters.
func WebRTCStats() WebRTCCounters {
	c := WebRTCCounters{ICE: map[string]uint64{}, DTLSFailures: map[string]uint64{}, SRTPAuthFailures: map[string]uint64{},
		DTLSHandshakes: webrtcSeries.dtlsOK.Load()}
	for i, n := range iceCounterNames {
		c.ICE[n] = webrtcSeries.ice[i].Load()
	}
	for i, n := range dtlsFailReasons {
		c.DTLSFailures[n] = webrtcSeries.dtlsFail[i].Load()
	}
	for i, n := range srtpDirections {
		c.SRTPAuthFailures[n] = webrtcSeries.srtpAuth[i].Load()
	}
	return c
}

// SetLabel names the relay's call in the WebRTC diagnostics (the caller
// leg's SIP Call-ID).
func (r *Relay) SetLabel(label string) {
	r.mu.Lock()
	r.label = label
	r.mu.Unlock()
}

// Label is what SetLabel set.
func (r *Relay) Label() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.label
}

// registerWebRTCMetrics exposes the WebRTC leg series on reg.
func registerWebRTCMetrics(reg prometheus.Registerer) {
	reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "hello_webrtc_legs", Help: "Live WebRTC legs (browser and app media) on this node.",
		}, func() float64 { return float64(WebRTCLegCount()) }),
		webrtcDTLSSeconds,
	)
	for i, st := range iceCounterNames {
		reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "hello_webrtc_ice_state_total", Help: "WebRTC legs reaching an ICE state: connected (first authenticated check), restarted (new remote ufrag), failed (no check arrived).",
			ConstLabels: prometheus.Labels{"state": st},
		}, func() float64 { return float64(webrtcSeries.ice[i].Load()) }))
	}
	for i, why := range dtlsFailReasons {
		reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "hello_webrtc_dtls_failures_total", Help: "Failed DTLS-SRTP handshakes on WebRTC legs, by reason.",
			ConstLabels: prometheus.Labels{"reason": why},
		}, func() float64 { return float64(webrtcSeries.dtlsFail[i].Load()) }))
	}
}

// registerSRTPAuthMetrics exposes the SRTP authentication failures of every
// encrypted leg (SDES phones and WebRTC alike) on reg.
func registerSRTPAuthMetrics(reg prometheus.Registerer) {
	for i, dir := range srtpDirections {
		reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "hello_srtp_auth_failures_total", Help: "Inbound SRTP and SRTCP packets that failed authentication, by the side of the call they came from.",
			ConstLabels: prometheus.Labels{"direction": dir},
		}, func() float64 { return float64(webrtcSeries.srtpAuth[i].Load()) }))
	}
}
