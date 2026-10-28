package integration

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/icholy/digest"
)

// labTLSAddr is Kamailio's SIP-over-TLS socket as the lab publishes it
// (compose.yaml: host TCP 5081 -> 5061).
const labTLSAddr = "127.0.0.1:5081"

// labSIPCA copies the lab's SIP CA out of the Kamailio container.
func labSIPCA(t *testing.T) *x509.CertPool {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "ca.crt")
	if out, err := compose("cp", "kamailio:/etc/hello-tls/ca.crt", dst).CombinedOutput(); err != nil {
		t.Fatalf("copy the SIP CA: %v\n%s", err, out)
	}
	pem, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("the lab SIP CA does not parse")
	}
	return pool
}

// sipConn is a minimal SIP-over-TLS client for the lab: one request, one
// final response.
type sipConn struct {
	t  *testing.T
	c  net.Conn
	br *bufio.Reader
}

func dialTLS(t *testing.T, cfg *tls.Config) (*sipConn, error) {
	t.Helper()
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := tls.DialWithDialer(&d, "tcp", labTLSAddr, cfg)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = c.Close() })
	return &sipConn{t: t, c: c, br: bufio.NewReader(c)}, nil
}

// roundTrip sends raw and returns the first final (>= 200) response's status
// line and headers; provisional responses are skipped.
func (s *sipConn) roundTrip(raw string) (status string, h textproto.MIMEHeader) {
	s.t.Helper()
	_ = s.c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(s.c, raw); err != nil {
		s.t.Fatalf("write: %v", err)
	}
	tp := textproto.NewReader(s.br)
	for {
		line, err := tp.ReadLine()
		if err != nil {
			s.t.Fatalf("read status line: %v", err)
		}
		hdr, err := tp.ReadMIMEHeader()
		if err != nil {
			s.t.Fatalf("read headers: %v", err)
		}
		if n, _ := strconv.Atoi(hdr.Get("Content-Length")); n > 0 {
			if _, err := io.CopyN(io.Discard, s.br, int64(n)); err != nil {
				s.t.Fatalf("read body: %v", err)
			}
		}
		if f := strings.Fields(line); len(f) >= 2 && f[1] >= "200" {
			return line, hdr
		}
	}
}

func registerTLS(user, callID string, cseq int, auth string) string {
	b := "z9hG4bK" + callID + strconv.Itoa(cseq)
	r := fmt.Sprintf("REGISTER sip:%s SIP/2.0\r\n"+
		"Via: SIP/2.0/TLS 127.0.0.1:5999;branch=%s;rport\r\n"+
		"Max-Forwards: 70\r\n"+
		"From: <sip:%s@%s>;tag=t%s\r\n"+
		"To: <sip:%s@%s>\r\n"+
		"Call-ID: %s\r\nCSeq: %d REGISTER\r\n"+
		"Contact: <sip:%s@127.0.0.1:5999;transport=tls>\r\n"+
		"Expires: 120\r\n", labDomain, b, user, labDomain, callID, user, labDomain, callID, cseq, user)
	if auth != "" {
		r += "Authorization: " + auth + "\r\n"
	}
	return r + "Content-Length: 0\r\n\r\n"
}

// TestLabSIPTLS runs the SIP-over-TLS edge (phone-feature-parity S-14)
// against the lab: Kamailio's TLS socket accepts TLS 1.2 and later with the
// lab SIP CA's certificate and refuses older protocols, a phone registers
// through it with digest authentication, and the binding records the TLS
// transport so a sips: call can find it.
func TestLabSIPTLS(t *testing.T) {
	lc := newLabClient(t)
	pool := labSIPCA(t)
	ds := lc.extension("tls")
	d := ds[0]
	remember(d.Secret)

	t.Run("TLS 1.2 and later with the CA's certificate", func(t *testing.T) {
		c, err := dialTLS(t, &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatalf("TLS handshake: %v", err)
		}
		if v := c.c.(*tls.Conn).ConnectionState().Version; v < tls.VersionTLS12 {
			t.Fatalf("negotiated TLS version %#x, want 1.2 or later", v)
		}
	})

	t.Run("an untrusted client sees the lab CA only", func(t *testing.T) {
		if _, err := dialTLS(t, &tls.Config{ServerName: "localhost", MinVersion: tls.VersionTLS12}); err == nil {
			t.Fatal("a client without the lab CA accepted the certificate")
		}
	})

	t.Run("TLS below 1.2 is refused", func(t *testing.T) {
		for _, v := range []uint16{tls.VersionTLS10, tls.VersionTLS11} {
			if _, err := dialTLS(t, &tls.Config{InsecureSkipVerify: true, MinVersion: v, MaxVersion: v}); err == nil { //nolint:gosec // probing the server's protocol floor
				t.Fatalf("handshake at TLS %#x succeeded", v)
			}
		}
	})

	t.Run("REGISTER over TLS records the TLS transport", func(t *testing.T) {
		c, err := dialTLS(t, &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatal(err)
		}
		callID := randDigits(10) + "@lab-tls"
		status, h := c.roundTrip(registerTLS(d.User, callID, 1, ""))
		if !strings.Contains(status, " 401 ") {
			t.Fatalf("unauthenticated REGISTER over TLS: %q, want 401", status)
		}
		chal := pickLabChallenge(t, h["Www-Authenticate"])
		cred, err := digest.Digest(chal, digest.Options{Method: "REGISTER", URI: "sip:" + labDomain, Username: d.User, Password: d.Secret})
		if err != nil {
			t.Fatal(err)
		}
		status, _ = c.roundTrip(registerTLS(d.User, callID, 2, cred.String()))
		if !strings.Contains(status, " 200 ") {
			t.Fatalf("authenticated REGISTER over TLS: %q, want 200", status)
		}
		var found bool
		for _, b := range lc.registrations() {
			if b.Device == d.User {
				found = true
				if b.Transport != "tls" {
					t.Errorf("binding transport = %q, want tls", b.Transport)
				}
			}
		}
		if !found {
			t.Fatal("the TLS registration is not in /api/v1/registrations")
		}
	})
}

func pickLabChallenge(t *testing.T, headers []string) *digest.Challenge {
	t.Helper()
	var first *digest.Challenge
	for _, v := range headers {
		ch, err := digest.ParseChallenge(v)
		if err != nil {
			continue
		}
		if first == nil {
			first = ch
		}
		if strings.EqualFold(ch.Algorithm, "SHA-256") {
			return ch
		}
	}
	if first == nil {
		t.Fatalf("no digest challenge in %q", headers)
	}
	return first
}
