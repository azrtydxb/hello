package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestPublic fails if a loopback, link-local (including the cloud metadata
// address), private, carrier-grade NAT, unspecified, multicast, IPv4-mapped
// or translator-embedded private address counts as public, or a public one
// does not.
func TestPublic(t *testing.T) {
	for s, want := range map[string]bool{
		"93.184.216.34": true, "2606:4700::1": true, "8.8.8.8": true,
		"10.1.2.3": false, "192.168.1.1": false, "172.16.0.1": false, "172.31.255.255": false,
		"127.0.0.1": false, "127.1.2.3": false, "169.254.169.254": false, "169.254.1.1": false,
		"::1": false, "fe80::1": false, "fd00::1": false, "fd00:ec2::254": false,
		"100.64.0.1": false, "0.0.0.0": false, "224.0.0.1": false, "255.255.255.255": false,
		"ff02::1": false, "::": false,
		"::ffff:10.0.0.1": false, "::ffff:127.0.0.1": false, "::ffff:169.254.169.254": false, "::ffff:8.8.8.8": true,
		"64:ff9b::a00:1": false, "64:ff9b::7f00:1": false, "64:ff9b::808:808": true,
		"2002:7f00:1::": false, "2002:a00:1::": false,
	} {
		if got := Public(addr(t, s)); got != want {
			t.Errorf("Public(%s) = %v, want %v", s, got, want)
		}
	}
}

// TestParseAllow fails if an allow-list can open a never-allowed range or a
// public range, or a valid private range is rejected.
func TestParseAllow(t *testing.T) {
	ps, err := ParseAllow("10.0.0.0/8, 192.168.10.5  fd12::/16")
	if err != nil || len(ps) != 3 {
		t.Fatalf("ParseAllow = %v, %v", ps, err)
	}
	for _, bad := range []string{"0.0.0.0/0", "127.0.0.1", "169.254.169.254/32", "169.254.0.0/16", "::1", "fe80::/10",
		"8.8.8.0/24", "not-an-ip", "224.0.0.0/4", "10.0.0.0/7"} {
		if _, err := ParseAllow(bad); err == nil {
			t.Errorf("ParseAllow(%q) accepted", bad)
		}
	}
	o := Options{Allow: ps}
	for s, want := range map[string]bool{"10.9.9.9": true, "192.168.10.5": true, "192.168.10.6": false, "172.16.0.1": false,
		"fd12::1": true, "127.0.0.1": false, "169.254.169.254": false} {
		if got := o.allowed(addr(t, s)); got != want {
			t.Errorf("allowed(%s) = %v, want %v", s, got, want)
		}
	}
	if !(Options{AllowAllPrivate: true}).allowed(addr(t, "10.0.0.1")) || (Options{AllowAllPrivate: true}).allowed(addr(t, "127.0.0.1")) {
		t.Error("AllowAllPrivate must open private ranges and nothing else")
	}
}

// TestValidateURL fails if a URL a webhook or connector must not use is
// accepted.
func TestValidateURL(t *testing.T) {
	for _, ok := range []string{"https://example.com/hook", "https://example.com:8443/a?b=c", "https://93.184.216.34/x"} {
		if _, err := ValidateURL(ok, true, Options{}); err != nil {
			t.Errorf("ValidateURL(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "example.com/hook", "ftp://example.com/", "http://example.com/", "https://user:pw@example.com/",
		"https://example.com/#frag", "https:///path", "https://127.0.0.1/", "https://[::1]/", "https://169.254.169.254/latest",
		"https://10.0.0.1/", "https://localhost/", "https://app.localhost/", "https://[::ffff:127.0.0.1]/",
		"https://example.com/" + strings.Repeat("a", 2100),
	} {
		if _, err := ValidateURL(bad, true, Options{}); err == nil {
			t.Errorf("ValidateURL(%q) accepted", bad)
		}
	}
	if _, err := ValidateURL("http://10.0.0.1/", false, Options{AllowAllPrivate: true}); err != nil {
		t.Errorf("http to an allowed private host = %v", err)
	}
	if _, err := ValidateURL("http://example.com/", false, Options{}); err != nil {
		t.Errorf("http when https is not required = %v", err)
	}
}

// TestClientRefusesLoopback fails if the guarded client reaches the test
// server on loopback, with or without an allow-list.
func TestClientRefusesLoopback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hi")) }))
	defer ts.Close()
	for _, o := range []Options{{}, {AllowAllPrivate: true}, {Allow: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}} {
		resp, err := NewClient(o).Get(ts.URL)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err == nil || !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("Get(loopback) with %+v = %v, want ErrBlockedAddress", o, err)
		}
	}
}

// TestRedirects fails if a redirect is followed by default, or a same-host
// redirect is refused when allowed, or a cross-host one is followed.
func TestRedirects(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("other")) }))
	defer other.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/final", http.StatusFound) })
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("final")) })
	mux.HandleFunc("/cross", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) })
	ts := httptest.NewServer(mux)
	defer ts.Close()
	// Tests may dial loopback only through the override.
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	get := func(o Options, path string) (int, string) {
		o.DialContext = dial
		resp, err := NewClient(o).Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := ReadBody(resp.Body, 100)
		return resp.StatusCode, string(b)
	}
	if code, _ := get(Options{}, "/same"); code != http.StatusFound {
		t.Errorf("default followed a redirect: %d", code)
	}
	if code, body := get(Options{FollowSameHostRedirects: 2}, "/same"); code != 200 || body != "final" {
		t.Errorf("same-host redirect = %d %q", code, body)
	}
	if code, _ := get(Options{FollowSameHostRedirects: 2}, "/cross"); code != http.StatusFound {
		t.Errorf("cross-host redirect followed: %d", code)
	}
}

// TestReadBody fails if the size limit is not enforced exactly.
func TestReadBody(t *testing.T) {
	if b, err := ReadBody(strings.NewReader("12345"), 5); err != nil || string(b) != "12345" {
		t.Errorf("at the limit = %q, %v", b, err)
	}
	if _, err := ReadBody(strings.NewReader("123456"), 5); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over the limit = %v", err)
	}
}

// TestTimeout fails if a request outlives the client's timeout.
func TestTimeout(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer ts.Close()
	defer close(block)
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	start := time.Now()
	resp, err := NewClient(Options{Timeout: 200 * time.Millisecond, DialContext: dial}).Get(ts.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("Get = %v after %v", err, time.Since(start))
	}
}
