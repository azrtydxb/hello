package sip

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/emiago/sipgo/sip"
)

// SIP over mutual TLS between the edge (Kamailio) and hello-sip
// (clients-webrtc-mobile-video S-12). The signalling of a browser carries the
// DTLS fingerprint of its media, which is only as trustworthy as the path that
// carries it, so the hop from Kamailio to Hello is TLS, both sides present a
// certificate of the Hello CA and each verifies the other. Everything else
// keeps using UDP on the edge network.

// closeTLS closes the TLS listener, if there is one.
func (s *Server) closeTLS() {
	if s.cfg.TLSListener != nil {
		_ = s.cfg.TLSListener.Close()
	}
}

// reloadingCert serves the certificate in two files and re-reads them when
// they change on disk (cert-manager renews a Secret in place), at most once a
// minute.
type reloadingCert struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	checked time.Time
	mod     time.Time
}

func (r *reloadingCert) get() (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cert != nil && time.Since(r.checked) < time.Minute {
		return r.cert, nil
	}
	r.checked = time.Now()
	st, err := os.Stat(r.certFile)
	if err != nil {
		if r.cert != nil {
			return r.cert, nil // keep serving the last good certificate
		}
		return nil, err
	}
	if r.cert != nil && st.ModTime().Equal(r.mod) {
		return r.cert, nil
	}
	c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.cert != nil {
			return r.cert, nil
		}
		return nil, err
	}
	r.cert, r.mod = &c, st.ModTime()
	return r.cert, nil
}

// TLSHop is the TLS material of the Kamailio to hello-sip hop.
type TLSHop struct {
	// Listener takes the edge's connections: TLS 1.2 or later, and a client
	// certificate signed by the CA is required.
	Listener net.Listener
	// Client is how Hello's own requests to the edge verify it (CA, server
	// name from the address dialled) and present Hello's certificate.
	Client *tls.Config
}

// ListenTLS listens on addr for the TLS hop with the certificate in
// certFile/keyFile and the CA in caFile. The certificate must be usable as
// both a server and a client certificate (the edge verifies Hello, Hello
// verifies the edge, and Hello presents it on its own connections).
func ListenTLS(addr, certFile, keyFile, caFile string) (*TLSHop, error) {
	pem, err := os.ReadFile(caFile) //nolint:gosec // operator-configured path
	if err != nil {
		return nil, fmt.Errorf("read the TLS CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("the TLS CA file holds no certificate")
	}
	rc := &reloadingCert{certFile: certFile, keyFile: keyFile}
	if _, err := rc.get(); err != nil {
		return nil, fmt.Errorf("load the TLS certificate: %w", err)
	}
	server := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      pool,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return rc.get() },
	}
	client := &tls.Config{
		MinVersion:           tls.VersionTLS12,
		RootCAs:              pool,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return rc.get() },
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &TLSHop{Listener: tls.NewListener(ln, server), Client: client}, nil
}

// wireLabel is the transport a request arrived on at this node: what sipgo's
// transport recorded when it read the message, not what the sender wrote in
// its Via. udp, tcp or tls; anything else is udp.
func wireLabel(req *sip.Request) string {
	switch strings.ToLower(req.Transport()) {
	case "tcp":
		return "tcp"
	case "tls":
		return "tls"
	}
	return "udp"
}

// WebRTCHop is how a WebRTC offer in req reached this node, for
// media.Relay.AnswerWebRTC: encrypted when the request arrived on this node's
// TLS listener from a trusted edge (the hop of S-12), with the operator's
// permission to answer over a cleartext hop (HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP).
func (s *Server) WebRTCHop(req *sip.Request) media.SignalingHop {
	return media.SignalingHop{
		Encrypted:      s.cfg.TLSListener != nil && s.fromTrustedProxy(req) && wireLabel(req) == transportTLS,
		AllowCleartext: s.cfg.WebRTCAllowCleartextHop,
	}
}

// checkWebRTCHop refuses a WebRTC offer that arrived over a cleartext hop
// while that is not allowed: it counts and logs the refusal with its reason and
// returns media.ErrCleartextHop for the caller to answer with (488). It is
// what a softphone call path runs before AnswerWebRTC, which checks it again.
func (s *Server) checkWebRTCHop(req *sip.Request) error {
	if err := s.WebRTCHop(req).Check(); err != nil {
		s.m.WebRTCRefused.WithLabelValues("cleartext_hop").Inc()
		s.log.Warn("WebRTC offer refused: it arrived over a cleartext edge-to-node hop",
			"reason", "cleartext_hop", "source", req.Source(), "wire", wireLabel(req),
			"hop_tls_enabled", s.cfg.TLSListener != nil,
			"fix", "enable the TLS hop (HELLO_SIP_TLS_ADDR, KAMAILIO_HELLO_TLS_PORT) or set HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP=true")
		return err
	}
	return nil
}
