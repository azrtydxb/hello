package sipua

import (
	"bytes"
	"context"
	"testing"
	"time"
)

var offer = []byte("v=0\r\no=a 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40000 RTP/AVP 0\r\n")
var answer = []byte("v=0\r\no=b 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40002 RTP/AVP 0\r\n")

// pair returns phone a configured to send everything straight to phone b, so
// the user agent itself is tested without a PBX in between.
func pair(t *testing.T) (a, b *Phone) {
	t.Helper()
	b, err := New(Options{User: "b", Domain: "test.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	a, err = New(Options{User: "a", Domain: "test.invalid", Proxy: b.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, b
}

func TestDirectCallAnswerHangup(t *testing.T) {
	a, b := pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan *Incoming, 1)
	go func() {
		in, err := b.Next(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		_ = in.Ring()
		if err := in.Answer(answer); err != nil {
			t.Error(err)
		}
		done <- in
	}()
	out, err := a.Dial(ctx, "b", offer)
	if err != nil || out.Status != 200 {
		t.Fatalf("dial = %+v, %v; want 200", out, err)
	}
	if !bytes.Equal(out.Response.Body(), answer) {
		t.Fatalf("answer SDP = %q", out.Response.Body())
	}
	in := <-done
	if !bytes.Equal(in.Request.Body(), offer) {
		t.Fatalf("offer SDP = %q", in.Request.Body())
	}
	if err := out.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-in.Ended():
	case <-ctx.Done():
		t.Fatal("callee never saw the BYE")
	}
}

func TestDirectCallBusy(t *testing.T) {
	a, b := pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		if in, err := b.Next(ctx); err == nil {
			_ = in.Reject(486, "Busy Here")
		}
	}()
	out, err := a.Dial(ctx, "b", offer)
	if err != nil || out.Status != 486 {
		t.Fatalf("dial = %+v, %v; want 486", out, err)
	}
}
