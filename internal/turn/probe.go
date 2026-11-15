package turn

// Reachability of the configured STUN and TURN servers for the Diagnostics
// "Clients" tab (clients-webrtc-mobile-video S-52). A plain turn: or stun:
// URI over UDP or TCP is asked a STUN Binding request, which coturn answers
// without credentials, so the check proves the server is up and reachable from
// hello-control. A turns: (TLS) URI is checked by a TCP connection only: the
// server's certificate may come from a private CA hello-control does not
// hold, which is no fault of the TURN service. Nothing secret is sent.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/pion/stun/v3"
)

// Probe is the result of checking one server URI.
type Probe struct {
	URL string `json:"url"`
	// Transport is udp, tcp or tls; Method what was checked: stun (a
	// Binding request answered) or connect (a TCP connection made).
	Transport string `json:"transport"`
	Method    string `json:"method"`
	Reachable bool   `json:"reachable"`
	LatencyMs int64  `json:"latencyMs"`
	// Error says why it is not reachable, without secrets.
	Error string `json:"error,omitempty"`
	// Addrs are the server's addresses as resolved here: a WebRTC leg whose
	// selected address is one of them is relayed through TURN.
	Addrs     []string  `json:"addrs"`
	CheckedAt time.Time `json:"checkedAt"`
}

// ParseURI splits a stun:, stuns:, turn: or turns: URI into its host, port
// and transport (udp, tcp or tls).
func ParseURI(uri string) (host string, port int, transport string, err error) {
	scheme, rest, ok := strings.Cut(uri, ":")
	if !ok {
		return "", 0, "", fmt.Errorf("turn: %q has no scheme", uri)
	}
	hp, query, _ := strings.Cut(rest, "?")
	secure := false
	switch strings.ToLower(scheme) {
	case "stun", "turn":
	case "stuns", "turns":
		secure = true
	default:
		return "", 0, "", fmt.Errorf("turn: %q is not a STUN or TURN URI", uri)
	}
	port = 3478
	if secure {
		port = 5349
	}
	host = hp
	if h, p, err := net.SplitHostPort(hp); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", 0, "", fmt.Errorf("turn: %q has a bad port", uri)
		}
		host, port = h, n
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return "", 0, "", fmt.Errorf("turn: %q has no host", uri)
	}
	transport = "udp"
	if secure {
		transport = "tls"
	}
	for _, kv := range strings.Split(query, "&") {
		k, v, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, "transport") && strings.EqualFold(v, "tcp") {
			if secure {
				transport = "tls"
			} else {
				transport = "tcp"
			}
		}
	}
	return host, port, transport, nil
}

// ProbeURI checks one server within timeout.
func ProbeURI(ctx context.Context, uri string, timeout time.Duration) Probe {
	p := Probe{URL: uri, CheckedAt: time.Now().UTC(), Addrs: []string{}}
	host, port, transport, err := ParseURI(uri)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	p.Transport = transport
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		p.Error = "the host name does not resolve"
		return p
	}
	// IPv4 first: Hello's media and coturn's relay addresses are IPv4.
	target := ips[0].IP
	for _, ip := range ips {
		p.Addrs = append(p.Addrs, ip.IP.String())
		if ip.IP.To4() != nil && target.To4() == nil {
			target = ip.IP
		}
	}
	addr := net.JoinHostPort(target.String(), strconv.Itoa(port))
	start := time.Now()
	switch transport {
	case "udp":
		p.Method = "stun"
		err = bindingUDP(ctx, addr)
	case "tcp":
		p.Method = "stun"
		err = bindingTCP(ctx, addr)
	default:
		p.Method = "connect"
		var c net.Conn
		c, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = c.Close()
		}
	}
	p.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		p.Error = probeError(err)
		return p
	}
	p.Reachable = true
	return p
}

func probeError(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "no answer before the timeout"
	case errors.Is(err, errNotBinding):
		return "the server answered something other than a STUN Binding response"
	default:
		var oe *net.OpError
		if errors.As(err, &oe) {
			return "connection failed: " + oe.Err.Error()
		}
		return "the check failed"
	}
}

var errNotBinding = errors.New("turn: not a binding response")

func bindingRequest() (*stun.Message, error) {
	return stun.Build(stun.TransactionID, stun.BindingRequest, stun.Fingerprint)
}

func checkBinding(req *stun.Message, raw []byte) error {
	m := &stun.Message{Raw: raw}
	if err := m.Decode(); err != nil || m.TransactionID != req.TransactionID {
		return errNotBinding
	}
	if m.Type != stun.BindingSuccess {
		return errNotBinding
	}
	return nil
}

func bindingUDP(ctx context.Context, addr string) error {
	c, err := (&net.Dialer{}).DialContext(ctx, "udp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	req, err := bindingRequest()
	if err != nil {
		return err
	}
	// Two tries: a single datagram can be lost.
	buf := make([]byte, 1500)
	var last error
	for range 2 {
		if _, err := c.Write(req.Raw); err != nil {
			return err
		}
		_ = c.SetReadDeadline(earlier(ctx, time.Now().Add(700*time.Millisecond)))
		n, err := c.Read(buf)
		if err != nil {
			last = err
			continue
		}
		return checkBinding(req, buf[:n])
	}
	return last
}

func bindingTCP(ctx context.Context, addr string) error {
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	req, err := bindingRequest()
	if err != nil {
		return err
	}
	if _, err := c.Write(req.Raw); err != nil {
		return err
	}
	head := make([]byte, 20)
	if _, err := io.ReadFull(c, head); err != nil {
		return err
	}
	size := int(head[2])<<8 | int(head[3])
	if size > 1480 {
		return errNotBinding
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(c, body); err != nil {
		return err
	}
	return checkBinding(req, append(head, body...))
}

func earlier(ctx context.Context, t time.Time) time.Time {
	if dl, ok := ctx.Deadline(); ok && dl.Before(t) {
		return dl
	}
	return t
}
