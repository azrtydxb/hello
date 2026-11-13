package integration

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/icholy/digest"
)

// labWSSAddr is Kamailio's SIP-over-WebSocket socket as the lab publishes it
// (compose.yaml: host TCP 5083 -> 8088).
const labWSSAddr = "127.0.0.1:5083"

// labWSSOrigin is an origin the lab allows (KAMAILIO_WS_ORIGINS).
const labWSSOrigin = "http://localhost:8081"

// dialWSS opens a WebSocket to wss://<lab>/sip with the sip sub-protocol and
// the given Origin ("" sends none).
func dialWSS(t *testing.T, cfg *tls.Config, origin string) (net.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	d := ws.Dialer{
		Protocols: []string{"sip"},
		TLSConfig: cfg,
		Header:    ws.HandshakeHeaderHTTP(h),
	}
	c, _, _, err := d.Dial(ctx, "wss://"+labWSSAddr+"/sip")
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, nil
}

// wssRoundTrip sends one SIP message as a text frame and returns the first
// final response's status line and headers.
func wssRoundTrip(t *testing.T, c net.Conn, raw string) (string, textproto.MIMEHeader) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if err := wsutil.WriteClientText(c, []byte(raw)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	for {
		msg, err := wsutil.ReadServerText(c)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		tp := textproto.NewReader(bufio.NewReader(strings.NewReader(string(msg))))
		line, err := tp.ReadLine()
		if err != nil {
			t.Fatalf("status line: %v", err)
		}
		h, _ := tp.ReadMIMEHeader()
		if f := strings.Fields(line); len(f) >= 2 && f[1] >= "200" {
			return line, h
		}
	}
}

func registerWSS(user, callID string, cseq int, auth string) string {
	b := "z9hG4bK" + callID + fmt.Sprint(cseq)
	r := fmt.Sprintf("REGISTER sip:%s SIP/2.0\r\n"+
		"Via: SIP/2.0/WSS abc123.invalid;branch=%s\r\n"+
		"Max-Forwards: 70\r\n"+
		"From: <sip:%s@%s>;tag=t%s\r\n"+
		"To: <sip:%s@%s>\r\n"+
		"Call-ID: %s\r\nCSeq: %d REGISTER\r\n"+
		"Contact: <sip:%s@abc123.invalid;transport=ws>\r\n"+
		"Expires: 120\r\n", labDomain, b, user, labDomain, callID, user, labDomain, callID, cseq, user)
	if auth != "" {
		r += "Authorization: " + auth + "\r\n"
	}
	return r + "Content-Length: 0\r\n\r\n"
}

// TestLabWSSRegister runs SIP over WebSocket (clients-webrtc-mobile-video
// S-8 to S-12) against the lab: the handshake is accepted from an allowed
// origin and from a client that sends none, refused from any other origin and
// for another path, and a client registers through it with digest
// authentication; the binding records the wss transport.
func TestLabWSSRegister(t *testing.T) {
	lc := newLabClient(t)
	pool := labSIPCA(t)
	cfg := &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}
	d := lc.extension("wss")[0]
	remember(d.Secret)

	t.Run("a cross-origin handshake is refused", func(t *testing.T) {
		if c, err := dialWSS(t, cfg, "https://evil.example"); err == nil {
			_ = c.Close()
			t.Fatal("a WebSocket from an origin that is not allowed was accepted")
		}
	})

	t.Run("plain HTTP gets no page", func(t *testing.T) {
		tr := &http.Transport{TLSClientConfig: cfg}
		defer tr.CloseIdleConnections()
		resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Get("https://" + labWSSAddr + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatalf("GET /metrics on the WSS socket = %d, want an error", resp.StatusCode)
		}
	})

	t.Run("REGISTER over WSS records the wss transport", func(t *testing.T) {
		c, err := dialWSS(t, cfg, labWSSOrigin)
		if err != nil {
			t.Fatalf("WebSocket handshake: %v", err)
		}
		callID := randDigits(10) + "@lab-wss"
		status, h := wssRoundTrip(t, c, registerWSS(d.User, callID, 1, ""))
		if !strings.Contains(status, " 401 ") {
			t.Fatalf("unauthenticated REGISTER over WSS: %q, want 401", status)
		}
		chal := pickLabChallenge(t, h["Www-Authenticate"])
		cred, err := digest.Digest(chal, digest.Options{Method: "REGISTER", URI: "sip:" + labDomain, Username: d.User, Password: d.Secret})
		if err != nil {
			t.Fatal(err)
		}
		status, _ = wssRoundTrip(t, c, registerWSS(d.User, callID, 2, cred.String()))
		if !strings.Contains(status, " 200 ") {
			t.Fatalf("authenticated REGISTER over WSS: %q, want 200", status)
		}
		var found bool
		for _, b := range lc.registrations() {
			if b.Device == d.User {
				found = true
				if b.Transport != "wss" {
					t.Errorf("binding transport = %q, want wss", b.Transport)
				}
			}
		}
		if !found {
			t.Fatal("the WSS registration is not in /api/v1/registrations")
		}
	})

	t.Run("a client without an Origin may connect", func(t *testing.T) {
		if _, err := dialWSS(t, cfg, ""); err != nil {
			t.Fatalf("handshake without an Origin: %v", err)
		}
	})
}
