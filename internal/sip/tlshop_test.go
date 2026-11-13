package sip

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
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

// hopPKI is a throwaway CA and leaf certificates for the TLS hop tests.
type hopPKI struct {
	dir    string
	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caFile string
}

func newHopPKI(t *testing.T) *hopPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hop test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	p := &hopPKI{dir: t.TempDir(), ca: ca, caKey: key}
	p.caFile = filepath.Join(p.dir, "ca.crt")
	writePEM(t, p.caFile, "CERTIFICATE", der)
	return p
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// leaf issues a certificate for 127.0.0.1 with both server and client use,
// and returns its files.
func (p *hopPKI) leaf(t *testing.T, name string, signer *hopPKI) (certFile, keyFile string) {
	t.Helper()
	if signer == nil {
		signer = p
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, signer.ca, &key.PublicKey, signer.caKey)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certFile, keyFile = filepath.Join(p.dir, name+".crt"), filepath.Join(p.dir, name+".key")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "EC PRIVATE KEY", kb)
	return certFile, keyFile
}

func (p *hopPKI) clientConfig(t *testing.T, certFile, keyFile string) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(p.ca)
	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	if certFile != "" {
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{c}
	}
	return cfg
}

func hopRegister(user, callID string, cseq int, auth string) string {
	r := fmt.Sprintf("REGISTER sip:%s SIP/2.0\r\n"+
		"Via: SIP/2.0/TLS 127.0.0.1:5073;branch=z9hG4bK%s%d;rport\r\n"+
		"Max-Forwards: 70\r\n"+
		"From: <sip:%s@%s>;tag=t%s\r\nTo: <sip:%s@%s>\r\n"+
		"Call-ID: %s\r\nCSeq: %d REGISTER\r\n"+
		"Contact: <sip:%s@192.0.2.5:5999;transport=ws>\r\nExpires: 120\r\n"+
		"X-Hello-Transport: tls\r\nX-Hello-Client: 198.51.100.7:4000\r\n",
		testDomain, callID, cseq, user, testDomain, callID, user, testDomain, callID, cseq, user)
	if auth != "" {
		r += "Authorization: " + auth + "\r\n"
	}
	return r + "Content-Length: 0\r\n\r\n"
}

// hopRoundTrip sends one request on c and returns the first final response.
func hopRoundTrip(t *testing.T, c net.Conn, br *bufio.Reader, raw string) (string, textproto.MIMEHeader) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
	tp := textproto.NewReader(br)
	for {
		line, err := tp.ReadLine()
		if err != nil {
			t.Fatalf("read status line: %v", err)
		}
		h, err := tp.ReadMIMEHeader()
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if n, _ := strconv.Atoi(h.Get("Content-Length")); n > 0 {
			_, _ = io.CopyN(io.Discard, br, int64(n))
		}
		if f := strings.Fields(line); len(f) >= 2 && f[1] >= "200" {
			return line, h
		}
	}
}

// TestSIPOverTLSHop fails if hello-sip does not take SIP over mutual TLS from
// the edge next to UDP (S-12): a REGISTER with the edge's client certificate
// is answered on the same connection, authenticates with digest and is
// recorded with the transport the edge reports; a client with no certificate,
// or one signed by another CA, is refused at the handshake; and a server whose
// certificate the Hello CA did not sign is refused by Hello's own client
// configuration.
func TestSIPOverTLSHop(t *testing.T) {
	pki := newHopPKI(t)
	srvCert, srvKey := pki.leaf(t, "hello-sip", nil)
	edgeCert, edgeKey := pki.leaf(t, "kamailio", nil)
	hop, err := ListenTLS("127.0.0.1:0", srvCert, srvKey, pki.caFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hop.Listener.Close() })
	pbx := startPBX(t, ringAllDevices(), trustLoopback, func(c *Config, _ *Deps) { c.TLSListener, c.TLSClient = hop.Listener, hop.Client })
	addr := hop.Listener.Addr().String()

	t.Run("a REGISTER over mutual TLS is answered on the connection", func(t *testing.T) {
		c, err := tls.Dial("tcp", addr, pki.clientConfig(t, edgeCert, edgeKey))
		if err != nil {
			t.Fatalf("handshake with the edge's certificate: %v", err)
		}
		defer func() { _ = c.Close() }()
		if v := c.ConnectionState().Version; v < tls.VersionTLS12 {
			t.Fatalf("TLS version %#x, want 1.2 or later", v)
		}
		br := bufio.NewReader(c)
		callID := "hop1@test"
		status, h := hopRoundTrip(t, c, br, hopRegister("b1", callID, 1, ""))
		if !strings.Contains(status, " 401 ") {
			t.Fatalf("unauthenticated REGISTER over TLS: %q, want 401", status)
		}
		var chal *digest.Challenge
		for _, v := range h["Www-Authenticate"] {
			ch, err := digest.ParseChallenge(v)
			if err != nil {
				continue
			}
			if chal == nil || strings.EqualFold(ch.Algorithm, "SHA-256") {
				chal = ch
			}
		}
		if chal == nil {
			t.Fatalf("no digest challenge in %v", h["Www-Authenticate"])
		}
		cred, err := digest.Digest(chal, digest.Options{Method: "REGISTER", URI: "sip:" + testDomain, Username: "b1", Password: "pb1"})
		if err != nil {
			t.Fatal(err)
		}
		status, _ = hopRoundTrip(t, c, br, hopRegister("b1", callID, 2, cred.String()))
		if !strings.Contains(status, " 200 ") {
			t.Fatalf("authenticated REGISTER over TLS: %q, want 200", status)
		}
		var transports []string
		for _, b := range bindings(t, pbx, "sip:b1@"+testDomain) {
			transports = append(transports, b.Transport)
		}
		if len(transports) != 1 || transports[0] != "tls" {
			t.Fatalf("binding transports = %v, want [tls]", transports)
		}
	})

	t.Run("a client without a certificate is refused", func(t *testing.T) {
		if err := hopHandshakeFails(addr, pki.clientConfig(t, "", "")); err == nil {
			t.Fatal("hello-sip took a TLS client that presented no certificate")
		}
	})

	t.Run("a client certificate of another CA is refused", func(t *testing.T) {
		other := newHopPKI(t)
		cert, key := other.leaf(t, "stranger", nil)
		cfg := pki.clientConfig(t, cert, key)
		if err := hopHandshakeFails(addr, cfg); err == nil {
			t.Fatal("hello-sip took a client certificate its CA did not sign")
		}
	})

	t.Run("Hello's client verifies the far side", func(t *testing.T) {
		other := newHopPKI(t)
		cert, key := other.leaf(t, "impostor", nil)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		c, _ := tls.LoadX509KeyPair(cert, key)
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{c}}).Handshake()
			_ = conn.Close()
		}()
		conn, err := tls.Dial("tcp", ln.Addr().String(), hop.Client)
		if err == nil {
			_ = conn.Close()
			t.Fatal("Hello's TLS client accepted a certificate the Hello CA did not sign")
		}
	})
}

// hopHandshakeFails dials addr and reports the error of the first read after
// the handshake: with TLS 1.3 a refused client certificate surfaces there.
func hopHandshakeFails(addr string, cfg *tls.Config) error {
	c, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = c.Read(make([]byte, 1))
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return nil
	}
	return err
}
