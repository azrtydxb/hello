package turn

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

func TestParseURI(t *testing.T) {
	for _, tc := range []struct {
		uri, host string
		port      int
		transport string
		bad       bool
	}{
		{"turn:10.0.0.1:3478?transport=udp", "10.0.0.1", 3478, "udp", false},
		{"turn:turn.example:3479?transport=tcp", "turn.example", 3479, "tcp", false},
		{"turns:turn.example?transport=tcp", "turn.example", 5349, "tls", false},
		{"stun:10.0.0.1", "10.0.0.1", 3478, "udp", false},
		{"turn:[::1]:3478", "::1", 3478, "udp", false},
		{"http://x", "", 0, "", true},
		{"turn:", "", 0, "", true},
		{"turn:h:99999", "", 0, "", true},
	} {
		h, p, tr, err := ParseURI(tc.uri)
		if (err != nil) != tc.bad || (!tc.bad && (h != tc.host || p != tc.port || tr != tc.transport)) {
			t.Errorf("ParseURI(%q) = %q %d %q %v", tc.uri, h, p, tr, err)
		}
	}
}

// stunServer answers Binding requests on UDP and TCP like coturn does.
func stunServer(t *testing.T) (udpPort, tcpPort int) {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	answer := func(raw []byte, from net.Addr) []byte {
		m := &stun.Message{Raw: append([]byte(nil), raw...)}
		if m.Decode() != nil || m.Type != stun.BindingRequest {
			return nil
		}
		ua, _ := from.(*net.UDPAddr)
		ta, _ := from.(*net.TCPAddr)
		x := &stun.XORMappedAddress{}
		if ua != nil {
			x.IP, x.Port = ua.IP, ua.Port
		} else {
			x.IP, x.Port = ta.IP, ta.Port
		}
		out, err := stun.Build(stun.NewTransactionIDSetter(m.TransactionID), stun.BindingSuccess, x, stun.Fingerprint)
		if err != nil {
			return nil
		}
		return out.Raw
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if r := answer(buf[:n], from); r != nil {
				_, _ = pc.WriteTo(r, from)
			}
		}
	}()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 1500)
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				if r := answer(buf[:n], c.RemoteAddr()); r != nil {
					_, _ = c.Write(r)
				}
			}()
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port, ln.Addr().(*net.TCPAddr).Port
}

// TestProbeURI fails if a server that answers STUN Binding requests is not
// reported reachable over UDP and TCP, a TLS listener not by a connection, or
// if a silent or closed port is reported reachable.
func TestProbeURI(t *testing.T) {
	udp, tcp := stunServer(t)
	ctx := context.Background()
	for _, tc := range []struct {
		uri, method string
		ok          bool
	}{
		{"turn:127.0.0.1:" + strconv.Itoa(udp) + "?transport=udp", "stun", true},
		{"turn:127.0.0.1:" + strconv.Itoa(tcp) + "?transport=tcp", "stun", true},
		{"turns:127.0.0.1:" + strconv.Itoa(tcp) + "?transport=tcp", "connect", true},
		{"stun:localhost:" + strconv.Itoa(udp), "stun", true},
	} {
		p := ProbeURI(ctx, tc.uri, 2*time.Second)
		if p.Reachable != tc.ok || p.Method != tc.method || len(p.Addrs) == 0 || p.Error != "" {
			t.Errorf("%s: %+v", tc.uri, p)
		}
	}
	// A UDP port nobody answers on.
	silent, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	p := ProbeURI(ctx, "turn:127.0.0.1:"+strconv.Itoa(silent.LocalAddr().(*net.UDPAddr).Port), 1500*time.Millisecond)
	if p.Reachable || p.Error == "" {
		t.Errorf("silent server: %+v", p)
	}
	// A TCP port that refuses.
	ln, _ := net.Listen("tcp4", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	if p := ProbeURI(ctx, "turns:127.0.0.1:"+strconv.Itoa(port), time.Second); p.Reachable || p.Error == "" {
		t.Errorf("closed port: %+v", p)
	}
	if p := ProbeURI(ctx, "nonsense", time.Second); p.Reachable || p.Error == "" {
		t.Errorf("bad URI: %+v", p)
	}
}
