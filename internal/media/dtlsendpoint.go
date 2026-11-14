package media

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/pion/dtls/v3"
)

// DTLSEndpoint is a standalone DTLS-SRTP endpoint on a UDP socket: what a
// phone or a browser does in front of the anchor, for tests and tools. It
// owns a certificate of its own, runs the handshake over the DTLS records its
// socket receives (told from STUN and RTP by their first byte, RFC 7983), and
// protects and unprotects media with the keys the handshake exported.
type DTLSEndpoint struct {
	conn *net.UDPConn
	cert *Cert
	pc   *dtlsPacketConn
	rtp  chan []byte
	stun chan []byte

	mu   sync.Mutex
	dc   *dtls.Conn
	prot *Protector
}

// NewDTLSEndpoint starts an endpoint reading conn; it never writes before a
// handshake or Send is asked for. Close stops it (the socket stays the
// caller's).
func NewDTLSEndpoint(conn *net.UDPConn) (*DTLSEndpoint, error) {
	cert, err := newCert()
	if err != nil {
		return nil, err
	}
	e := &DTLSEndpoint{conn: conn, cert: cert, rtp: make(chan []byte, 256), stun: make(chan []byte, 16)}
	e.pc = newDTLSPacketConn(conn.LocalAddr(), &net.UDPAddr{IP: net.IPv4zero}, nil)
	go e.read()
	return e, nil
}

func (e *DTLSEndpoint) read() {
	buf := make([]byte, 2048)
	for {
		n, _, err := e.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		d := append([]byte(nil), buf[:n]...)
		switch {
		case isSTUN(d):
			select {
			case e.stun <- d:
			default:
			}
		case isDTLS(d):
			e.pc.push(d)
		default:
			select {
			case e.rtp <- d:
			default:
			}
		}
	}
}

// Fingerprint is the endpoint's certificate fingerprint, for its SDP.
func (e *DTLSEndpoint) Fingerprint() Fingerprint { return e.cert.fp }

// Datagrams are the media (non-DTLS, non-STUN) datagrams the socket received.
func (e *DTLSEndpoint) Datagrams() <-chan []byte { return e.rtp }

// STUN are the STUN datagrams the socket received.
func (e *DTLSEndpoint) STUN() <-chan []byte { return e.stun }

// Handshake runs the DTLS handshake with peer as the client or the server,
// requiring a certificate that matches one of want (the fingerprints in the
// peer's SDP), and keys the endpoint's SRTP from it.
func (e *DTLSEndpoint) Handshake(ctx context.Context, client bool, peer *net.UDPAddr, want ...Fingerprint) error {
	e.pc.send = func(b []byte) error { _, err := e.conn.WriteToUDP(b, peer); return err }
	e.pc.remote = peer
	dc, err := newDTLSConn(e.pc, peer, client, e.cert, want)
	if err != nil {
		return err
	}
	if err := dc.HandshakeContext(ctx); err != nil {
		_ = dc.Close()
		return err
	}
	st, ok := dc.ConnectionState()
	prof, ok2 := dc.SelectedSRTPProtectionProfile()
	if !ok || !ok2 {
		_ = dc.Close()
		return errors.New("media: DTLS handshake exported no keys")
	}
	local, remote, err := keysFromState(&st, prof, client)
	if err != nil {
		_ = dc.Close()
		return err
	}
	p, err := NewEndpoint(local, remote)
	if err != nil {
		_ = dc.Close()
		return err
	}
	e.mu.Lock()
	e.dc, e.prot = dc, p
	e.mu.Unlock()
	go func() {
		b := make([]byte, 1500)
		for {
			if _, err := dc.Read(b); err != nil {
				return
			}
		}
	}()
	return nil
}

// Keys returns the endpoint's SRTP protector once a handshake has finished.
func (e *DTLSEndpoint) Keys() (*Protector, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.prot, e.prot != nil
}

// Close ends the DTLS connection.
func (e *DTLSEndpoint) Close() {
	e.mu.Lock()
	dc := e.dc
	e.mu.Unlock()
	if dc != nil {
		_ = dc.Close()
	}
	_ = e.pc.Close()
}
