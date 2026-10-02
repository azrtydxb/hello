package sip

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

func options(p *phone) *sip.Request {
	req := sip.NewRequest(sip.OPTIONS, sip.Uri{Scheme: "sip", Host: testDomain})
	req.SetDestination(p.pbx)
	return req
}

// TestOptionsPing fails if OPTIONS is not answered 200 with the advertised
// address (not the bind address) in the Contact.
func TestOptionsPing(t *testing.T) {
	const adv = "198.51.100.7:5099"
	pbx := startPBX(t, nil, func(c *Config, _ *Deps) { c.AdvertisedAddr = adv })
	p := newPhone(t, pbx, "x", "x")
	res := p.do(options(p))
	if res.StatusCode != 200 {
		t.Fatalf("OPTIONS = %d, want 200", res.StatusCode)
	}
	ct := res.Contact()
	if ct == nil || ct.Address.HostPort() != adv {
		t.Fatalf("Contact = %v, want host %s", ct, adv)
	}
	if v := res.Via(); v == nil || v.Host != "127.0.0.1" {
		t.Fatalf("Via = %v, want the phone's own Via echoed", v)
	}
	if h := res.GetHeader("Allow"); h == nil || !strings.Contains(h.Value(), "INVITE") {
		t.Fatalf("Allow = %v", h)
	}
}

// TestOptionsViaAdvertised fails if requests the node originates carry the
// bind address instead of the advertised one in Via and Contact.
func TestOptionsViaAdvertised(t *testing.T) {
	// The advertised host must route back here, so use the loopback name.
	var advertised string
	pbx := startPBX(t, []snapshot.Device{dev(1, "a1", "100", "pw-a"), dev(2, "b1", "200", "pw-b")},
		func(c *Config, _ *Deps) {
			advertised = strings.Replace(c.AdvertisedAddr, "127.0.0.1", "localhost", 1)
			c.AdvertisedAddr = advertised
		})
	a, b := newPhone(t, pbx, "a1", "pw-a"), newPhone(t, pbx, "b1", "pw-b")
	a.register(t)
	b.register(t)
	b.setCallee(ringForever())
	ctx, cancel := context.WithCancel(t.Context())
	res := dial(ctx, a, "200")
	defer func() { cancel(); waitCall(t, res) }()
	inv := waitReq(t, b.invites, "fork INVITE")
	if v := inv.Via(); v == nil || v.Host != "localhost" {
		t.Fatalf("fork Via = %v, want advertised host", v)
	}
	if c := inv.Contact(); c == nil || c.Address.HostPort() != advertised {
		t.Fatalf("fork Contact = %v, want %s", c, advertised)
	}
}

// TestMalformedDatagrams fails if garbage, binary, oversized datagrams or a
// request without Call-ID crash the node or stop it answering OPTIONS.
func TestMalformedDatagrams(t *testing.T) {
	pbx := startPBX(t, nil)
	c, err := net.Dial("udp", pbx.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	junk := make([]byte, 1400)
	_, _ = rand.Read(junk)
	big := bytes.Repeat([]byte("A"), 9000) // far above the path MTU; below macOS's UDP limit
	for _, d := range [][]byte{
		[]byte("hello"), junk, big, {0, 0, 0},
		[]byte("INVITE sip:x SIP/2.0\r\n\r\n"),
		[]byte("SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP 1.2.3.4\r\n\r\n"),
		[]byte("OPTIONS sip:x@hello.test SIP/2.0\r\nVia: SIP/2.0/UDP 127.0.0.1:1;branch=z9hG4bKx\r\nCSeq: 1 OPTIONS\r\nContent-Length: 999\r\n\r\n"),
	} {
		if _, err := c.Write(d); err != nil {
			t.Fatal(err)
		}
	}
	// A request missing Call-ID/From/To gets 400.
	noCallID := "OPTIONS sip:hello.test SIP/2.0\r\nVia: SIP/2.0/UDP " + c.LocalAddr().String() + ";branch=z9hG4bKmal1\r\nCSeq: 1 OPTIONS\r\nMax-Forwards: 70\r\nContent-Length: 0\r\n\r\n"
	if _, err := c.Write([]byte(noCallID)); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "SIP/2.0 400") {
		t.Fatalf("malformed OPTIONS answered %q, %v; want 400", buf[:n], err)
	}
	p := newPhone(t, pbx, "x", "x")
	if res := p.do(options(p)); res.StatusCode != 200 {
		t.Fatalf("OPTIONS after junk = %d", res.StatusCode)
	}
}

func TestRedactSIP(t *testing.T) {
	msg := "REGISTER sip:hello.test SIP/2.0\r\nAuthorization: Digest username=\"a\", response=\"deadbeef\"\r\n" +
		"proxy-authorization:Digest secret\r\nCall-ID: x\r\n\r\n"
	got := RedactSIP(msg)
	if strings.Contains(got, "deadbeef") || strings.Contains(got, "secret") || !strings.Contains(got, "Call-ID: x") {
		t.Fatalf("RedactSIP = %q", got)
	}
	var buf bytes.Buffer
	log := slog.New(NewRedactingHandler(slog.NewTextHandler(&buf, nil))).With("raw", msg)
	log.Error("failed to parse", "data", msg, slog.Group("g", "inner", msg))
	if strings.Contains(buf.String(), "deadbeef") {
		t.Fatalf("log leaked credentials: %s", buf.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestNoCredentialsInLogs fails if the node's debug log, including sipgo's
// parse-failure dumps, contains an Authorization value, a password, an HA1
// or the nonce secret.
func TestNoCredentialsInLogs(t *testing.T) {
	var buf syncBuffer
	d := dev(1, "alice", "100", "hunter2-pass")
	pbx := startPBX(t, []snapshot.Device{d}, func(_ *Config, deps *Deps) {
		deps.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	p := newPhone(t, pbx, "alice", "hunter2-pass")
	res := p.authDo(p.registerReq(300), AlgSHA256, "wrong-pass")
	if res.StatusCode != 401 {
		t.Fatalf("bad password = %d", res.StatusCode)
	}
	p.register(t)
	c, err := net.Dial("udp", pbx.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = c.Write([]byte("NOT-SIP\r\nAuthorization: Digest response=\"cafebabe1234\"\r\n\r\n"))
	eventually(t, "parse failure logged", func() bool { return strings.Contains(buf.String(), "NOT-SIP") })
	logs := buf.String()
	for _, secret := range []string{"cafebabe1234", "hunter2-pass", "wrong-pass", d.HA1MD5, d.HA1SHA256, testSecret, "response="} {
		if strings.Contains(logs, secret) {
			t.Fatalf("log contains %q:\n%s", secret, logs)
		}
	}
}
