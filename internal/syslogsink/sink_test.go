package syslogsink

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeLines struct {
	mu      sync.Mutex
	phone   map[int64][]livestate.SyslogLine
	unknown map[string][]livestate.SyslogLine
}

func newFakeLines() *fakeLines {
	return &fakeLines{phone: map[int64][]livestate.SyslogLine{}, unknown: map[string][]livestate.SyslogLine{}}
}

func (f *fakeLines) AppendSyslogPhone(_ context.Context, id int64, l livestate.SyslogLine) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phone[id] = append(f.phone[id], l)
	return nil
}

func (f *fakeLines) AppendSyslogUnknown(_ context.Context, l livestate.SyslogLine) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unknown[l.Source] = append(f.unknown[l.Source], l)
	return nil
}

func (f *fakeLines) counts() (phone, unknown int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.phone {
		phone += len(v)
	}
	for _, v := range f.unknown {
		unknown += len(v)
	}
	return
}

type fakePhones map[netip.Addr]int64

func (p fakePhones) PhoneIPs(context.Context) (map[netip.Addr]int64, error) { return p, nil }

var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// TestSyslogSink fails if lines from a provisioned phone are not stored
// under it, lines from an unknown IP are not kept, the per-phone rate cap is
// not applied, or a redacted secret survives.
func TestSyslogSink(t *testing.T) {
	ctx := context.Background()
	phoneIP, otherIP := netip.MustParseAddr("10.0.0.7"), netip.MustParseAddr("192.0.2.9")
	now := t0
	lines := newFakeLines()
	s := New(Config{Lines: lines, Phones: fakePhones{phoneIP: 42}, RatePerSec: 10, Burst: 5, Now: func() time.Time { return now }})

	t.Run("provisioned phone", func(t *testing.T) {
		s.Handle(ctx, phoneIP, "<14>Oct 27 12:00:00 WP826 sip: registered")
		got := lines.phone[42]
		if len(got) != 1 || got[0].Message != "Oct 27 12:00:00 WP826 sip: registered" || got[0].Severity != 6 || got[0].Source != "10.0.0.7" {
			t.Fatalf("phone lines = %+v", got)
		}
	})
	t.Run("unknown IP kept", func(t *testing.T) {
		s.Handle(ctx, otherIP, "hello from nowhere")
		if got := lines.unknown["192.0.2.9"]; len(got) != 1 || got[0].Severity != -1 {
			t.Fatalf("unknown lines = %+v", got)
		}
	})
	t.Run("secrets redacted", func(t *testing.T) {
		const token = "abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst"
		for _, raw := range []string{
			`Authorization: Digest username="1001", response="deadbeefcafe", nonce="n0nce"`,
			`GET /p/` + token + `/cfg.xml`,
			`config password=hunter2 admin_password: s3cret!`,
			`fetch https://user:pw123@prov.example/x token=` + token,
		} {
			s.Handle(ctx, phoneIP, raw)
		}
		for _, l := range lines.phone[42] {
			for _, bad := range []string{"deadbeef", "n0nce", "abcdefghijklmnopqrstuvwxyz", "hunter2", "s3cret", "pw123"} {
				if strings.Contains(l.Message, bad) {
					t.Errorf("secret %q survived in %q", bad, l.Message)
				}
			}
		}
	})
	t.Run("rate cap", func(t *testing.T) {
		before, _ := lines.counts()
		now = now.Add(time.Hour) // buckets full
		for range 50 {
			s.Handle(ctx, phoneIP, "flood")
		}
		after, _ := lines.counts()
		if got := after - before; got != 5 {
			t.Fatalf("stored %d of 50 flood lines, want the burst of 5", got)
		}
		now = now.Add(time.Second) // refill 10
		s.Handle(ctx, phoneIP, "later")
		last := lines.phone[42]
		if n := len(last); n < 2 || !strings.Contains(last[n-2].Message, "45 lines dropped") || last[n-1].Message != "later" {
			t.Fatalf("no drop notice before the next line: %+v", last[len(last)-2:])
		}
		// another phone is not affected by this one's flood
		s.Handle(ctx, otherIP, "still ok")
	})
}

func TestSharedAddressIsUnknown(t *testing.T) {
	lines := newFakeLines()
	// the store leaves an address two phones share out of the map
	s := New(Config{Lines: lines, Phones: fakePhones{}, Now: func() time.Time { return t0 }})
	s.Handle(context.Background(), netip.MustParseAddr("10.0.0.8"), "x")
	if p, u := lines.counts(); p != 0 || u != 1 {
		t.Fatalf("phone %d, unknown %d", p, u)
	}
}

func TestParse(t *testing.T) {
	l, ok := Parse("<191>hi\x00\r\n", "1.2.3.4", t0)
	if !ok || l.Severity != 7 || l.Message != "hi" {
		t.Fatalf("%+v %v", l, ok)
	}
	if _, ok := Parse("  \r\n", "x", t0); ok {
		t.Fatal("empty line accepted")
	}
	l, _ = Parse(strings.Repeat("a ", 2500), "x", t0)
	if len(l.Message) > MaxLineBytes || len(l.Message) < MaxLineBytes-1 {
		t.Fatalf("len %d", len(l.Message))
	}
}

func TestUDPListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := newFakeLines()
	s := New(Config{UDPAddr: "127.0.0.1:0", Lines: lines, Phones: fakePhones{netip.MustParseAddr("127.0.0.1"): 7}})
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("udp", s.UDPAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte("<13>over udp")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		lines.mu.Lock()
		n := len(lines.phone[7])
		lines.mu.Unlock()
		if n == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("line sent over UDP was not stored")
}

func TestReadFrame(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("5 hello<14>line two\nthird\n"))
	for _, want := range []string{"hello", "<14>line two", "third"} {
		got, err := readFrame(r)
		if got != want || (err != nil && want != "third") {
			t.Fatalf("frame = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := readFrame(bufio.NewReader(strings.NewReader("99999 x"))); err == nil {
		t.Fatal("an octet count over the limit was accepted")
	}
}

func TestTLSListener(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: t0.Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := newFakeLines()
	s := New(Config{TLSAddr: "127.0.0.1:0", TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		Lines: lines, Phones: fakePhones{netip.MustParseAddr("127.0.0.1"): 9}})
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(tmpl)
	leaf, _ := x509.ParseCertificate(der)
	pool.AddCert(leaf)
	c, err := tls.Dial("tcp", s.TLSAddr().String(), &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte("<14>one\n12 <14>two two\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		lines.mu.Lock()
		n := len(lines.phone[9])
		lines.mu.Unlock()
		if n == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("lines sent over TLS were not stored")
}

// TestSyslogMetrics fails if received, dropped (rate cap) and unmatched lines
// are not counted separately.
func TestSyslogMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	phone := netip.MustParseAddr("10.0.0.7")
	s := New(Config{Lines: newFakeLines(), Phones: fakePhones{phone: 1}, RatePerSec: 1, Burst: 2, Metrics: m, Now: func() time.Time { return t0 }})
	ctx := context.Background()
	for range 5 {
		s.Handle(ctx, phone, "x") // 2 stored, 3 dropped
	}
	s.Handle(ctx, netip.MustParseAddr("192.0.2.1"), "y") // unmatched
	s.Handle(ctx, phone, "  ")                           // empty: not counted
	for c, want := range map[prometheus.Counter]float64{m.Received: 6, m.Dropped: 3, m.Unmatched: 1} {
		if got := testutil.ToFloat64(c); got != want {
			t.Errorf("counter = %v, want %v", got, want)
		}
	}
}
