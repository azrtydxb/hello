package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/azrtydxb/hello/test/sipua"
)

func freePort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// startCarrier runs a carrier on loopback and returns its SIP and HTTP
// addresses.
func startCarrier(t *testing.T, inviteAuth string) (sipAddr, httpURL string) {
	t.Helper()
	c := &carrier{name: "test", user: "acct", password: "acct-" + "secret", realm: "carrier.test", inviteAuth: inviteAuth,
		helloDomain: "hello.test", regs: map[string]time.Time{}, nonces: map[string]bool{}, log: slog.New(slog.DiscardHandler)}
	sipAddr = "127.0.0.1:" + strconv.Itoa(freePort(t, "udp"))
	httpAddr := "127.0.0.1:" + strconv.Itoa(freePort(t, "tcp"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.run(ctx, sipAddr, httpAddr) }()
	for range 50 {
		if resp, err := http.Get("http://" + httpAddr + "/log"); err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return sipAddr, "http://" + httpAddr
}

// TestCarrierRegisterAndOutcomes fails if registration does not require the
// carrier's credentials or the scripted INVITE outcomes are wrong.
func TestCarrierRegisterAndOutcomes(t *testing.T) {
	sipAddr, _ := startCarrier(t, "407")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	bad, _ := sipua.New(sipua.Options{User: "acct", Password: "wrong", Domain: "carrier.test", Proxy: sipAddr})
	defer bad.Close()
	if res, err := bad.Register(ctx, time.Hour); err != nil || res.StatusCode != 401 {
		t.Fatalf("register with a wrong password = %v, %v; want 401", res, err)
	}
	p, _ := sipua.New(sipua.Options{User: "acct", Password: "acct-" + "secret", Domain: "carrier.test", Proxy: sipAddr})
	defer p.Close()
	if res, err := p.Register(ctx, time.Hour); err != nil || res.StatusCode != 200 {
		t.Fatalf("register = %v, %v", res, err)
	}
	for number, want := range map[string]int{"97150000000": 200, "97150000086": 486, "97150000003": 503} {
		out, err := p.Dial(ctx, number, []byte("v=0\r\n"))
		if err != nil || out.Status != want {
			t.Fatalf("dial %s = %+v, %v; want %d (through a 407 challenge)", number, out, err, want)
		}
		if want == 200 {
			_ = out.Hangup(ctx)
		}
	}
}

// TestCarrierPlacesCall fails if POST /call does not reach the target and
// report the final status.
func TestCarrierPlacesCall(t *testing.T) {
	_, httpURL := startCarrier(t, "")
	hello, _ := sipua.New(sipua.Options{User: "hello", Domain: "hello.test"})
	defer hello.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() {
		if in, err := hello.Next(ctx); err == nil {
			_ = in.Answer([]byte("v=0\r\n"))
		}
	}()
	body, _ := json.Marshal(map[string]any{"from": "+97145550000", "to": "+97145551234", "target": hello.Addr(), "hangupAfterMs": 100})
	resp, err := http.Post(httpURL+"/call", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct{ Status int }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Status != 200 {
		t.Fatalf("POST /call = %d %+v %v", resp.StatusCode, out, err)
	}
}
