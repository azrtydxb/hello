package api

// GET /api/v1/webrtc/status (clients-webrtc-mobile-video S-52): what the
// Diagnostics "Clients" tab shows about web and mobile clients. Registered
// clients by kind and transport, the live WebRTC legs every hello-sip node
// publishes (ICE and DTLS state, whether the selected address is a TURN
// server), TURN reachability, push health, the candidate types the clients
// reported, recent REGISTER failures of client devices and the last ICE,
// DTLS and REGISTER errors in one list.
//
// Each part comes from its own source (PostgreSQL, Valkey, the TURN servers);
// a source that cannot be read is named in "unavailable" and its part is
// empty, so the rest still shows. Nothing secret is in the answer: no
// credential, no TURN secret, no digest material.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/turn"
)

// WebRTCLive is the WebRTC status the hello-sip nodes publish; *LazyValkey
// implements it.
type WebRTCLive interface {
	WebRTCStatuses(ctx context.Context) ([]livestate.WebRTCNodeStatus, error)
}

// ClientQualityStats is the candidate-type summary of the clients' reports;
// *store.Store implements it (the status reads it from Config.Clients).
type ClientQualityStats interface {
	ClientCandidateStats(ctx context.Context, since time.Time) (store.ClientCandidateStats, error)
}

// Limits of the status answer.
const (
	// statusClientDevices is how many client devices' REGISTER histories
	// are read.
	statusClientDevices = 200
	// statusAuthFailures and statusLastErrors bound the lists.
	statusAuthFailures = 50
	statusLastErrors   = 20
	// statusRegistrations bounds the client registrations listed.
	statusRegistrations = 200
	// candidateWindow is how far back the candidate types are counted.
	candidateWindow = 24 * time.Hour
	// turnProbeTTL is how long a TURN check is reused; turnProbeTimeout
	// bounds one.
	turnProbeTTL     = 30 * time.Second
	turnProbeTimeout = 2 * time.Second
)

type clientCounts struct {
	// Enrolled counts the enabled client devices by kind (web, mobile).
	Enrolled map[string]int `json:"enrolled"`
	// Registered counts bindings by device kind (desk, web, mobile), and
	// ByTransport the client (web and mobile) bindings by transport.
	Registered  map[string]int `json:"registered"`
	ByTransport map[string]int `json:"byTransport"`
}

// clientRegistration is one binding of a web or mobile device.
type clientRegistration struct {
	DeviceID  int64     `json:"deviceId"`
	Device    string    `json:"device"`
	Kind      string    `json:"kind"`
	Label     string    `json:"label"`
	Extension string    `json:"extension"`
	Transport string    `json:"transport"`
	Source    string    `json:"source"`
	UserAgent string    `json:"userAgent"`
	Node      string    `json:"node"`
	Expires   time.Time `json:"expires"`
}

// webrtcLegView is a live leg with the call it belongs to and whether it is
// relayed through TURN.
type webrtcLegView struct {
	livestate.WebRTCLeg
	Node string `json:"node"`
	// From and To are the call's parties when the call is listed live.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// TURN is true when the selected address is one of the TURN servers'.
	TURN bool `json:"turn"`
}

type webrtcNodeView struct {
	Node      string                   `json:"node"`
	Port      int                      `json:"port"`
	Hosts     []string                 `json:"hosts"`
	Legs      int                      `json:"legs"`
	Counters  livestate.WebRTCCounters `json:"counters"`
	UpdatedAt time.Time                `json:"updatedAt"`
}

type turnStatus struct {
	// Configured is whether TURN credentials are handed out (URIs and the
	// shared secret are set); STUN lists the STUN URIs clients get.
	Configured bool         `json:"configured"`
	TTLSeconds int          `json:"ttlSeconds"`
	STUN       []string     `json:"stun"`
	Servers    []turn.Probe `json:"servers"`
	// RelayedLegs counts the live legs whose selected address is a TURN
	// server.
	RelayedLegs int `json:"relayedLegs"`
}

type pushStatus struct {
	Configured bool   `json:"configured"`
	Detail     string `json:"detail"`
}

// clientAuthFailure is a REGISTER of a client device Hello refused.
type clientAuthFailure struct {
	DeviceID    int64     `json:"deviceId"`
	Device      string    `json:"device"`
	Label       string    `json:"label"`
	Kind        string    `json:"kind"`
	At          time.Time `json:"at"`
	Code        int       `json:"code"`
	Reason      string    `json:"reason"`
	Source      string    `json:"source"`
	Node        string    `json:"node"`
	UserAgent   string    `json:"userAgent,omitempty"`
	Credentials bool      `json:"credentials"`
}

// clientError is one entry of the merged recent-errors list.
type clientError struct {
	At time.Time `json:"at"`
	// Kind is register, ice or dtls.
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
	Node   string `json:"node,omitempty"`
	Call   string `json:"call,omitempty"`
	Device string `json:"device,omitempty"`
}

type webrtcStatusView struct {
	GeneratedAt   time.Time                   `json:"generatedAt"`
	Clients       clientCounts                `json:"clients"`
	Registrations []clientRegistration        `json:"registrations"`
	Nodes         []webrtcNodeView            `json:"nodes"`
	Legs          []webrtcLegView             `json:"legs"`
	TURN          turnStatus                  `json:"turn"`
	Push          pushStatus                  `json:"push"`
	Candidates    *store.ClientCandidateStats `json:"candidates"`
	AuthFailures  []clientAuthFailure         `json:"authFailures"`
	LastErrors    []clientError               `json:"lastErrors"`
	// Unavailable names the sources that could not be read (devices, live,
	// webrtc, quality); their parts are empty.
	Unavailable []string `json:"unavailable"`
}

// turnProber checks the TURN servers, reusing a result for turnProbeTTL so a
// polling page does not hammer them.
type turnProber struct {
	mu    sync.Mutex
	at    time.Time
	key   string
	cache []turn.Probe
	// probe replaces turn.ProbeURI in tests.
	probe func(ctx context.Context, uri string, timeout time.Duration) turn.Probe
}

func (p *turnProber) check(ctx context.Context, uris []string, now time.Time) []turn.Probe {
	key := strings.Join(uris, "\n")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.key == key && now.Sub(p.at) < turnProbeTTL && p.cache != nil {
		return p.cache
	}
	probe := p.probe
	if probe == nil {
		probe = turn.ProbeURI
	}
	out := make([]turn.Probe, len(uris))
	var wg sync.WaitGroup
	for i, u := range uris {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = probe(ctx, u, turnProbeTimeout)
		}()
	}
	wg.Wait()
	p.key, p.at, p.cache = key, now, out
	return out
}

// getWebRTCStatus is GET /api/v1/webrtc/status.
func (s *server) getWebRTCStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.ClientAuth.now()
	v := webrtcStatusView{
		GeneratedAt:   now.UTC(),
		Clients:       clientCounts{Enrolled: map[string]int{"web": 0, "mobile": 0}, Registered: map[string]int{"desk": 0, "web": 0, "mobile": 0}, ByTransport: map[string]int{"udp": 0, "tcp": 0, "tls": 0, "ws": 0, "wss": 0}},
		Registrations: []clientRegistration{}, Nodes: []webrtcNodeView{}, Legs: []webrtcLegView{},
		AuthFailures: []clientAuthFailure{}, LastErrors: []clientError{}, Unavailable: []string{},
		Push: pushStatus{Detail: "The push gateway is not built yet (mobile apps are not built); web clients do not need it."},
	}
	unavailable := func(what string, err error) {
		s.Log.Warn("webrtc status: source unavailable", "source", what, "error", err)
		v.Unavailable = append(v.Unavailable, what)
	}

	// Client devices, by SIP username.
	devices := map[string]store.Device{}
	var clientNames []string
	for _, kind := range []string{auth.KindWeb, auth.KindMobile} {
		ds, err := s.Store.ListDevices(ctx, kind)
		if err != nil {
			unavailable("devices", err)
			break
		}
		for _, d := range ds {
			devices[d.SIPUsername] = d
			if d.Enabled {
				v.Clients.Enrolled[kind]++
				clientNames = append(clientNames, d.SIPUsername)
			}
		}
	}
	sort.Strings(clientNames)

	// Registrations and the calls the legs belong to.
	calls := map[string]livestate.Call{}
	if s.Live != nil {
		lctx, cancel := context.WithTimeout(ctx, liveTimeout)
		bs, err := s.Live.AllBindings(lctx)
		if err == nil {
			var cs []livestate.Call
			if cs, err = s.Live.Calls(lctx); err == nil {
				for _, c := range cs {
					calls[c.SIPCallID] = c
				}
			}
		}
		cancel()
		if err != nil {
			unavailable("live", err)
		}
		sort.Slice(bs, func(i, j int) bool { return bs[i].Device < bs[j].Device })
		for _, b := range bs {
			d, client := devices[b.Device]
			if !client || (d.Kind != auth.KindWeb && d.Kind != auth.KindMobile) {
				v.Clients.Registered["desk"]++
				continue
			}
			v.Clients.Registered[d.Kind]++
			t := strings.ToLower(b.Transport)
			if _, ok := v.Clients.ByTransport[t]; !ok {
				t = "udp"
			}
			v.Clients.ByTransport[t]++
			if len(v.Registrations) < statusRegistrations {
				v.Registrations = append(v.Registrations, clientRegistration{
					DeviceID: d.ID, Device: d.SIPUsername, Kind: d.Kind, Label: d.Label, Extension: b.Extension,
					Transport: t, Source: b.Source, UserAgent: b.UserAgent, Node: b.ReceivedNode, Expires: b.Expires,
				})
			}
		}
	} else {
		unavailable("live", errors.New("no live state configured"))
	}

	// TURN configuration and reachability.
	ice := s.ClientAuth.ICE
	v.TURN = turnStatus{Configured: len(ice.TURN) > 0 && ice.Secret != "", TTLSeconds: int(ice.Lifetime() / time.Second),
		STUN: append([]string{}, ice.STUN...), Servers: []turn.Probe{}}
	turnIPs := map[string]bool{}
	if len(ice.TURN) > 0 {
		v.TURN.Servers = s.turnProbe.check(ctx, ice.TURN, now)
		for _, p := range v.TURN.Servers {
			for _, a := range p.Addrs {
				turnIPs[a] = true
			}
		}
	}

	// The nodes' WebRTC legs and failures.
	if s.WebRTC != nil {
		lctx, cancel := context.WithTimeout(ctx, liveTimeout)
		sts, err := s.WebRTC.WebRTCStatuses(lctx)
		cancel()
		if err != nil {
			unavailable("webrtc", err)
		}
		for _, st := range sts {
			hosts := st.Hosts
			if hosts == nil {
				hosts = []string{}
			}
			v.Nodes = append(v.Nodes, webrtcNodeView{Node: st.Node, Port: st.Port, Hosts: hosts, Legs: len(st.Legs), Counters: nonNilCounters(st.Counters), UpdatedAt: st.UpdatedAt})
			for _, l := range st.Legs {
				lv := webrtcLegView{WebRTCLeg: l, Node: st.Node}
				if c, ok := calls[l.Call]; ok {
					lv.From, lv.To = c.From, c.To
				}
				if host, _, err := net.SplitHostPort(l.Remote); err == nil && turnIPs[host] {
					lv.TURN = true
					v.TURN.RelayedLegs++
				}
				v.Legs = append(v.Legs, lv)
			}
			for _, f := range st.Failures {
				v.LastErrors = append(v.LastErrors, clientError{At: f.At, Kind: f.Kind, Reason: f.Reason, Detail: f.Detail, Node: st.Node, Call: f.Call})
			}
		}
	}

	// The candidate types the clients reported.
	if qs, ok := s.Clients.(ClientQualityStats); ok {
		cs, err := qs.ClientCandidateStats(ctx, now.Add(-candidateWindow))
		if err != nil {
			unavailable("quality", err)
		} else {
			v.Candidates = &cs
		}
	}

	// Recent REGISTER failures of client devices.
	if s.Diagnostics != nil && len(clientNames) > 0 {
		if len(clientNames) > statusClientDevices {
			clientNames = clientNames[:statusClientDevices]
		}
		lctx, cancel := context.WithTimeout(ctx, liveTimeout)
		for _, name := range clientNames {
			as, err := s.Diagnostics.RegisterAttempts(lctx, name)
			if err != nil {
				unavailable("register_attempts", err)
				break
			}
			d := devices[name]
			for _, a := range as {
				if !registerFailed(a) {
					continue
				}
				v.AuthFailures = append(v.AuthFailures, clientAuthFailure{
					DeviceID: d.ID, Device: name, Label: d.Label, Kind: d.Kind, At: a.At, Code: a.Code, Reason: a.Reason,
					Source: a.Source, Node: a.Node, UserAgent: a.UserAgent, Credentials: a.Credentials,
				})
			}
		}
		cancel()
	}
	sort.Slice(v.AuthFailures, func(i, j int) bool { return v.AuthFailures[i].At.After(v.AuthFailures[j].At) })
	if len(v.AuthFailures) > statusAuthFailures {
		v.AuthFailures = v.AuthFailures[:statusAuthFailures]
	}
	for _, f := range v.AuthFailures {
		v.LastErrors = append(v.LastErrors, clientError{At: f.At, Kind: "register", Reason: registerReason(f.Code, f.Credentials),
			Detail: f.Reason, Node: f.Node, Device: f.Device})
	}
	sort.SliceStable(v.LastErrors, func(i, j int) bool { return v.LastErrors[i].At.After(v.LastErrors[j].At) })
	if len(v.LastErrors) > statusLastErrors {
		v.LastErrors = v.LastErrors[:statusLastErrors]
	}
	writeJSON(w, http.StatusOK, v)
}

// registerFailed reports whether an attempt is a refusal rather than the
// normal digest exchange: a first REGISTER without credentials is challenged
// (401) and a stale nonce re-challenged, which is not a failure.
func registerFailed(a livestate.RegisterAttempt) bool {
	switch {
	case a.Code < 300:
		return false
	case a.Code == http.StatusUnauthorized:
		return a.Credentials && !a.Stale
	}
	return true
}

// registerReason is a word for a refused REGISTER.
func registerReason(code int, credentials bool) string {
	switch {
	case code == http.StatusUnauthorized && credentials:
		return "bad_credentials"
	case code == http.StatusForbidden:
		return "forbidden"
	case code == http.StatusServiceUnavailable:
		return "unavailable"
	default:
		return "rejected"
	}
}

// nonNilCounters gives every map of c a value, so the answer always has
// objects there.
func nonNilCounters(c livestate.WebRTCCounters) livestate.WebRTCCounters {
	for _, m := range []*map[string]uint64{&c.ICE, &c.DTLSFailures, &c.SRTPAuthFailures} {
		if *m == nil {
			*m = map[string]uint64{}
		}
	}
	return c
}
