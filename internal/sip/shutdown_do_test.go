package sip

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

// TestRequestAfterShutdownReturns fails if a request the server sends while
// it shuts down (a NOTIFY from a poll tick that fired as the context ended)
// is left waiting for its ctx once the user agent is closed. sipgo does not
// end a transaction created on a closed layer, so such a request held
// Serve's wait for background work for up to byeTimeout.
func TestRequestAfterShutdownReturns(t *testing.T) {
	pbx := startPBX(t, threeDevices())
	sink, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() }) // never answers
	pbx.stop()
	eventually(t, "Serve to stop", func() bool { return !pbx.srv.Serving() })
	select {
	case <-pbx.srv.done:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve never closed its done channel")
	}

	req := sip.NewRequest(sip.NOTIFY, sip.Uri{Scheme: "sip", User: "x", Host: "127.0.0.1", Port: sink.LocalAddr().(*net.UDPAddr).Port})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	start := time.Now()
	_, _ = pbx.srv.do(ctx, req)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a request after shutdown waited %v for an answer that cannot come", d)
	}
}
