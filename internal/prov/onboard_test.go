package prov

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeOnboarding records the candidates the handler captures.
type fakeOnboarding struct {
	mu      sync.Mutex
	got     map[string]Candidate
	calls   int
	outcome CaptureOutcome
	err     error
}

func newFakeOnboarding() *fakeOnboarding {
	return &fakeOnboarding{got: map[string]Candidate{}, outcome: CaptureStored}
}

func (f *fakeOnboarding) CaptureCandidate(_ context.Context, c Candidate) (CaptureOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	f.got[c.MAC] = c
	return f.outcome, nil
}

func (f *fakeOnboarding) candidate(mac string) (Candidate, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.got[mac]
	return c, ok
}

func (f *fakeOnboarding) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func onboardEnv(t *testing.T, ob *fakeOnboarding, lim Limits) *env {
	t.Helper()
	return newEnvWith(t, nil, func(log *slog.Logger) *Limiter { return NewLimiter(nil, lim, log) },
		func(o *Options) { o.Onboarding = ob })
}

var generousLimits = Limits{IPPerMin: 1000, DeniedPer10Min: 1000, PhonePerHour: 1000, OnboardPerMin: 1000}

// TestOnboardCaptureEveryVendor fails if an unknown MAC asking for its
// config at the boot path is not recorded with the vendor, model and
// firmware its User-Agent states, the source IP and the requested path; if
// the response is anything but an empty 404; or if any config reaches it.
func TestOnboardCaptureEveryVendor(t *testing.T) {
	ob := newFakeOnboarding()
	e := onboardEnv(t, ob, generousLimits)
	cases := []struct {
		name, path, ua, mac string
		vendor              Vendor
		model, fw           string
	}{
		{"yealink", "/p/boot/805ec0000001.cfg", "Yealink SIP-T54W 96.86.0.70 80:5e:c0:00:00:01", "805ec0000001", Yealink, "T54W", "96.86.0.70"},
		{"grandstream", "/p/boot/cfg000b82000003.xml", "Grandstream Model HW WP822 V1.0A SW 1.0.9.69 DevId 00:0b:82:00:00:03", "000b82000003", Grandstream, "WP822", "1.0.9.69"},
		{"snom", "/p/boot/snomD785-000413000004.htm", "Mozilla/4.0 (compatible; snomD785-SIP 10.1.2.3)", "000413000004", Snom, "D785", "10.1.2.3"},
		{"fanvil", "/p/boot/0c383e000005.cfg", "Fanvil X6U 2.4.1.5876 0c:38:3e:00:00:05", "0c383e000005", Generic, "X6U", "2.4.1.5876"},
		{"poly", "/p/boot/0004f2000002.cfg", "FileTransport PolycomVVX-VVX_450-UA/5.9.5.0614", "0004f2000002", Generic, "VVX 450", "5.9.5.0614"},
		{"no ua", "/p/boot/0011220000aa.cfg", "", "0011220000aa", Generic, "", ""},
	}
	for _, c := range cases {
		w := e.do(req{path: c.path, ua: c.ua, proto: "http"})
		if w.Code != 404 || w.Body.Len() != 0 {
			t.Fatalf("%s: %d with %d body bytes, want an empty 404", c.name, w.Code, w.Body.Len())
		}
		if r := e.last(); r.Result != ResultNotAllowlisted || r.Status != 404 {
			t.Fatalf("%s: audit row %s/%d", c.name, r.Result, r.Status)
		}
		got, ok := ob.candidate(c.mac)
		if !ok {
			t.Fatalf("%s: %s not captured", c.name, c.mac)
		}
		if got.Model != c.model || got.Firmware != c.fw || got.IP != netip.MustParseAddr("192.168.10.50") || got.Path != c.path {
			t.Fatalf("%s: captured %+v", c.name, got)
		}
		if c.vendor != Generic && got.Vendor != c.vendor {
			t.Fatalf("%s: vendor %s, want %s", c.name, got.Vendor, c.vendor)
		}
	}
	// The newer Poly and Fanvil User-Agents name their vendor.
	if c, _ := ob.candidate("0004f2000002"); c.Vendor != Poly {
		t.Fatalf("poly vendor %s", c.Vendor)
	}
	if c, _ := ob.candidate("0c383e000005"); c.Vendor != Fanvil {
		t.Fatalf("fanvil vendor %s", c.Vendor)
	}
	if n := testutil.ToFloat64(e.m.OnboardingCaptures.WithLabelValues("stored")); int(n) != len(cases) {
		t.Fatalf("stored counter %v", n)
	}
}

// TestOnboardKnownPhoneNotCaptured fails if a MAC that is a phone is
// recorded, or if its boot flow changes.
func TestOnboardKnownPhoneNotCaptured(t *testing.T) {
	ob := newFakeOnboarding()
	e := onboardEnv(t, ob, generousLimits)
	e.s.addPhone(Yealink, "T54W", macA)
	w := e.do(req{path: "/p/boot/" + macA + ".cfg", proto: "http", ua: "Yealink SIP-T54W 96.86.0.70 80:5e:c0:aa:bb:cc"})
	if w.Code != 200 || ob.count() != 0 {
		t.Fatalf("known phone: %d, %d captures", w.Code, ob.count())
	}
}

// TestOnboardOnlyWellFormedMACs fails if a MAC no device can have (all
// zero, broadcast, multicast) is written.
func TestOnboardOnlyWellFormedMACs(t *testing.T) {
	ob := newFakeOnboarding()
	e := onboardEnv(t, ob, generousLimits)
	for _, mac := range []string{"ffffffffffff", "010203040506", "0100005e0001"} {
		expectEmpty(t, e, e.do(req{path: "/p/boot/" + mac + ".cfg", proto: "http"}), 404, ResultNotAllowlisted)
	}
	if ob.count() != 0 {
		t.Fatalf("%d malformed MACs written", ob.count())
	}
	for mac, want := range map[string]bool{
		"805ec0aabbcc": true, "000000000000": false, "ffffffffffff": false, "01005e000001": false,
		"805ec0aabbc": false, "805ec0aabbcg": false, "805EC0AABBCC": false, "": false,
	} {
		if got := CandidateMAC(mac); got != want {
			t.Errorf("CandidateMAC(%q) = %v", mac, got)
		}
	}
}

// TestOnboardWriteRateLimit fails if one source address can write more
// than its limit per minute, if the limit is shared between addresses, or
// if a limited request is not still an empty 404.
func TestOnboardWriteRateLimit(t *testing.T) {
	ob := newFakeOnboarding()
	lim := generousLimits
	lim.OnboardPerMin = 2
	e := onboardEnv(t, ob, lim)
	for _, mac := range []string{"805ec0000001", "805ec0000002", "805ec0000003", "805ec0000004"} {
		w := e.do(req{path: "/p/boot/" + mac + ".cfg", proto: "http"})
		if w.Code != 404 || w.Body.Len() != 0 {
			t.Fatalf("%s: %d", mac, w.Code)
		}
	}
	if ob.count() != 2 {
		t.Fatalf("%d writes from one address, want 2", ob.count())
	}
	if n := testutil.ToFloat64(e.m.OnboardingCaptures.WithLabelValues("rate_limited")); n != 2 {
		t.Fatalf("rate_limited counter %v", n)
	}
	e.do(req{path: "/p/boot/805ec0000005.cfg", proto: "http", client: "192.168.10.77"})
	if _, ok := ob.candidate("805ec0000005"); !ok {
		t.Fatal("another address was limited by the first")
	}
}

// TestOnboardOutcomesAndFailure fails if a full queue, an ignored MAC or a
// store failure changes the response (it stays an empty 404, never a 503),
// or is not counted.
func TestOnboardOutcomesAndFailure(t *testing.T) {
	ob := newFakeOnboarding()
	e := onboardEnv(t, ob, generousLimits)
	for _, out := range []CaptureOutcome{CaptureFull, CaptureIgnored, CaptureKnown} {
		ob.outcome = out
		expectEmpty(t, e, e.do(req{path: "/p/boot/805ec0000010.cfg", proto: "http"}), 404, ResultNotAllowlisted)
		if n := testutil.ToFloat64(e.m.OnboardingCaptures.WithLabelValues(string(out))); n != 1 {
			t.Fatalf("%s counter %v", out, n)
		}
	}
	ob.err = errors.New("database is down")
	expectEmpty(t, e, e.do(req{path: "/p/boot/805ec0000011.cfg", proto: "http"}), 404, ResultNotAllowlisted)
	if n := testutil.ToFloat64(e.m.OnboardingCaptures.WithLabelValues("error")); n != 1 {
		t.Fatalf("error counter %v", n)
	}
}

// TestOnboardDoesNotBlockSource fails if phones waiting to be adopted count
// as denials: many of them behind one address must not get that address
// blocked (adopted phones share it).
func TestOnboardDoesNotBlockSource(t *testing.T) {
	ob := newFakeOnboarding()
	lim := generousLimits
	lim.DeniedPer10Min = 2
	e := onboardEnv(t, ob, lim)
	for range 6 {
		expectEmpty(t, e, e.do(req{path: "/p/boot/805ec0000020.cfg", proto: "http"}), 404, ResultNotAllowlisted)
	}
	if w := e.do(req{path: "/p/boot/y000000000096.cfg", proto: "http"}); w.Code != 200 {
		t.Fatalf("the source was blocked: %d", w.Code)
	}
}

// TestOnboardWithoutStoreIsUnchanged fails if a handler with no onboarding
// store stops counting an unknown MAC as a denial.
func TestOnboardWithoutStoreIsUnchanged(t *testing.T) {
	lim := generousLimits
	lim.DeniedPer10Min = 2
	e := newEnvWith(t, nil, func(log *slog.Logger) *Limiter { return NewLimiter(nil, lim, log) })
	for range 3 {
		e.do(req{path: "/p/boot/805ec0000020.cfg", proto: "http"})
	}
	if w := e.do(req{path: "/p/boot/y000000000096.cfg", proto: "http"}); w.Code != 429 {
		t.Fatalf("denials no longer block the source: %d", w.Code)
	}
}

// TestOnboardCommonFileUserAgentMAC fails if a common boot file asked by a
// phone whose User-Agent carries its MAC is not recorded, or if the boot
// body is not served as before.
func TestOnboardCommonFileUserAgentMAC(t *testing.T) {
	ob := newFakeOnboarding()
	e := onboardEnv(t, ob, generousLimits)
	w := e.do(req{path: "/p/boot/y000000000096.cfg", proto: "http", ua: "Yealink SIP-T54W 96.86.0.70 80:5e:c0:00:00:30"})
	if w.Code != 200 || e.last().Result != ResultBootServed {
		t.Fatalf("common file: %d", w.Code)
	}
	c, ok := ob.candidate("805ec0000030")
	if !ok || c.Vendor != Yealink || c.Model != "T54W" || c.Firmware != "96.86.0.70" {
		t.Fatalf("captured %+v %v", c, ok)
	}
	if w := e.do(req{path: "/p/boot/y000000000096.cfg", proto: "http"}); w.Code != 200 || ob.count() != 1 {
		t.Fatalf("a request with no MAC wrote: %d, %d", w.Code, ob.count())
	}
}

func TestParseUserAgent(t *testing.T) {
	for _, c := range []struct {
		ua        string
		hint      Vendor
		v         Vendor
		model, fw string
	}{
		{"Yealink SIP-T54W 96.86.0.70 00:15:65:aa:bb:cc", Generic, Yealink, "T54W", "96.86.0.70"},
		{"Grandstream Model HW GRP2615 V1.2A SW 1.0.3.30", Generic, Grandstream, "GRP2615", "1.0.3.30"},
		{"Grandstream GXP2130 1.0.11.5", Generic, Grandstream, "GXP2130", "1.0.11.5"},
		{"Fanvil X6U 2.4.1.5876", Generic, Fanvil, "X6U", "2.4.1.5876"},
		{"Mozilla/4.0 (compatible; snom370-SIP 8.7.5.35; snom370)", Generic, Snom, "370", "8.7.5.35"},
		{"PolycomSoundPointIP-SPIP_550-UA/4.0.13", Generic, Poly, "SPIP 550", "4.0.13"},
		{"curl/8.0", Snom, Snom, "", ""},
		{"", Generic, Generic, "", ""},
		{"Yealink SIP-<script>alert(1)</script> 1.2.3", Generic, Yealink, "scriptalert(1)/script", "1.2.3"},
	} {
		v, m, fw := ParseUserAgent(c.ua, c.hint)
		if v != c.v || m != c.model || fw != c.fw {
			t.Errorf("ParseUserAgent(%q) = %s %q %q, want %s %q %q", c.ua, v, m, fw, c.v, c.model, c.fw)
		}
	}
	if _, m, _ := ParseUserAgent("Yealink SIP-"+strings.Repeat("A", 200), Generic); len(m) > 64 {
		t.Errorf("model of %d bytes", len(m))
	}
}
