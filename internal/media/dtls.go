// DTLS-SRTP for the anchored media path (phone-feature-parity S-16,
// RFC 5763 and RFC 5764). The key exchange runs over the media socket itself:
// DTLS records arrive on the leg's RTP port (told apart from STUN and RTP by
// their first byte, RFC 7983), pion/dtls runs the handshake over a packet
// conn that is fed from the leg's receive loop, and the keys it exports go
// into the same per-leg SRTP state SDES uses (srtp.go), so the relay, the
// recorder and the announcements see no difference.
//
// The certificate is a per-node self-signed ECDSA P-256 one that is made when
// first needed and never leaves the process. It carries no identity: the peer
// trusts it only through the fingerprint in the signalling (a=fingerprint),
// and Hello trusts the peer's certificate only through the fingerprint in the
// peer's SDP. A mismatch fails the handshake. Keys exist in memory only.
package media

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // a=fingerprint may name sha-1 (RFC 8122); it is only compared, never chosen
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/srtp/v3"
	"github.com/pion/transport/v4/deadline"
)

// dtlsHandshakeTimeout bounds one handshake, retransmissions included; tests
// shorten it with SetDTLSTimeout.
var dtlsHandshakeTimeout = 8 * time.Second

// certLifetime is how long a node certificate stays in use for new sessions;
// the certificate itself is valid for three times as long, so a session that
// began near the end of the period never carries an expired one.
const certLifetime = 30 * 24 * time.Hour

// Cert is the node's DTLS certificate and its SHA-256 fingerprint.
type Cert struct {
	tls     tls.Certificate
	fp      Fingerprint
	created time.Time
}

// Fingerprint returns the certificate's a=fingerprint value (sha-256).
func (c *Cert) Fingerprint() Fingerprint { return c.fp }

var nodeCert struct {
	mu sync.Mutex
	c  *Cert
}

// NodeCertificate returns the node's DTLS certificate, making one on first
// use and again once the current one is older than 30 days. It is not shared
// between nodes and not stored: after a restart or a takeover the fingerprint
// is a new one, which the peer learns from the new SDP.
func NodeCertificate() (*Cert, error) {
	nodeCert.mu.Lock()
	defer nodeCert.mu.Unlock()
	if nodeCert.c != nil && time.Since(nodeCert.c.created) < certLifetime {
		return nodeCert.c, nil
	}
	c, err := newCert()
	if err != nil {
		return nil, err
	}
	nodeCert.c = c
	return c, nil
}

func newCert() (*Cert, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("media: DTLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, fmt.Errorf("media: DTLS serial: %w", err)
	}
	var id [4]byte
	_, _ = rand.Read(id[:])
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "hello-" + hex.EncodeToString(id[:])},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(3 * certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("media: DTLS certificate: %w", err)
	}
	sum := sha256.Sum256(der)
	return &Cert{
		tls:     tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		fp:      Fingerprint{Hash: "sha-256", Sum: sum[:]},
		created: now,
	}, nil
}

// Fingerprint is a certificate fingerprint as written in SDP (RFC 8122): a
// hash function name and the digest.
type Fingerprint struct {
	Hash string // sha-1, sha-256, sha-384 or sha-512
	Sum  []byte
}

// IsZero reports an unset fingerprint.
func (f Fingerprint) IsZero() bool { return f.Hash == "" }

// String is the a=fingerprint value: "sha-256 AB:CD:...".
func (f Fingerprint) String() string {
	var b strings.Builder
	b.WriteString(f.Hash)
	b.WriteByte(' ')
	for i, c := range f.Sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02X", c)
	}
	return b.String()
}

func fingerprintHash(name string) (func() hash.Hash, int) {
	switch name {
	case "sha-1":
		return sha1.New, sha1.Size //nolint:gosec // see the import
	case "sha-256":
		return sha256.New, sha256.Size
	case "sha-384":
		return sha512.New384, sha512.Size384
	case "sha-512":
		return sha512.New, sha512.Size
	}
	return nil, 0
}

// ParseFingerprint parses an a=fingerprint value. Hash functions other than
// sha-1, sha-256, sha-384 and sha-512 (md5, md2) are refused: a peer that
// offers only those cannot be authenticated.
func ParseFingerprint(v string) (Fingerprint, error) {
	fields := strings.Fields(v)
	if len(fields) != 2 {
		return Fingerprint{}, errors.New("media: a=fingerprint needs a hash function and a digest")
	}
	name := strings.ToLower(fields[0])
	newHash, size := fingerprintHash(name)
	if newHash == nil {
		return Fingerprint{}, fmt.Errorf("media: a=fingerprint hash %q is not supported", name)
	}
	sum, err := hex.DecodeString(strings.ReplaceAll(fields[1], ":", ""))
	if err != nil || len(sum) != size {
		return Fingerprint{}, errors.New("media: a=fingerprint digest is malformed")
	}
	return Fingerprint{Hash: name, Sum: sum}, nil
}

// Match reports whether der hashes to the fingerprint (constant time).
func (f Fingerprint) Match(der []byte) bool {
	newHash, _ := fingerprintHash(f.Hash)
	if newHash == nil || len(f.Sum) == 0 {
		return false
	}
	h := newHash()
	h.Write(der)
	return subtle.ConstantTimeCompare(h.Sum(nil), f.Sum) == 1
}

// DTLS setup roles (RFC 4145 as used by RFC 5763).
const (
	SetupActive  = "active"
	SetupPassive = "passive"
	SetupActPass = "actpass"
)

// dtlsProfiles are the SRTP protection profiles offered in the handshake, in
// preference order: AES-GCM first (what browsers prefer), then the two
// AES-CM ones every phone has.
var dtlsProfiles = []dtls.SRTPProtectionProfile{
	dtls.SRTP_AEAD_AES_128_GCM,
	dtls.SRTP_AES128_CM_HMAC_SHA1_80,
	dtls.SRTP_AES128_CM_HMAC_SHA1_32,
}

// suiteOfProfile maps the negotiated DTLS profile to the leg's cipher suite.
func suiteOfProfile(p dtls.SRTPProtectionProfile) (CryptoSuite, bool) {
	switch p {
	case dtls.SRTP_AEAD_AES_128_GCM:
		return SuiteAEADGCM128, true
	case dtls.SRTP_AES128_CM_HMAC_SHA1_80:
		return SuiteAES128SHA1_80, true
	case dtls.SRTP_AES128_CM_HMAC_SHA1_32:
		return SuiteAES128SHA1_32, true
	}
	return "", false
}

// dtlsPacketConn presents the leg's socket to pion/dtls: datagrams the leg's
// receive loop classified as DTLS are queued here, and what DTLS writes goes
// out of the leg's socket to the current peer address. Closing it does not
// close the socket.
type dtlsPacketConn struct {
	in     chan []byte
	closed chan struct{}
	once   sync.Once
	rd     *deadline.Deadline
	send   func([]byte) error
	local  net.Addr
	remote net.Addr
}

func newDTLSPacketConn(local, remote net.Addr, send func([]byte) error) *dtlsPacketConn {
	return &dtlsPacketConn{
		in: make(chan []byte, 64), closed: make(chan struct{}), rd: deadline.New(),
		send: send, local: local, remote: remote,
	}
}

// push queues one received datagram (a copy); a full queue drops it, which
// DTLS retransmission covers.
func (p *dtlsPacketConn) push(b []byte) {
	select {
	case p.in <- append([]byte(nil), b...):
	case <-p.closed:
	default:
	}
}

func (p *dtlsPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case <-p.closed:
		return 0, nil, net.ErrClosed
	default:
	}
	select {
	case d := <-p.in:
		return copy(b, d), p.remote, nil
	case <-p.rd.Done():
		return 0, nil, os.ErrDeadlineExceeded
	case <-p.closed:
		return 0, nil, net.ErrClosed
	}
}

func (p *dtlsPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	select {
	case <-p.closed:
		return 0, net.ErrClosed
	default:
	}
	if err := p.send(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (p *dtlsPacketConn) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func (p *dtlsPacketConn) LocalAddr() net.Addr { return p.local }

func (p *dtlsPacketConn) SetDeadline(t time.Time) error { return p.SetReadDeadline(t) }

func (p *dtlsPacketConn) SetReadDeadline(t time.Time) error { p.rd.Set(t); return nil }

func (p *dtlsPacketConn) SetWriteDeadline(time.Time) error { return nil }

// dtlsSession is one DTLS-SRTP negotiation of a Secure: Hello's certificate
// (and so fingerprint) for it, its role, the peer's fingerprint once known and
// the handshake.
type dtlsSession struct {
	cert  *Cert
	local Fingerprint
	// setup is the role Hello announced: actpass while Hello's offer awaits
	// the answer, then active (Hello is the DTLS client) or passive.
	setup  string
	remote Fingerprint
	// proto is the m= transport negotiated (UDP/TLS/RTP/SAVP or SAVPF).
	proto string
	// wants are the fingerprints the peer's certificate may match.
	wants []Fingerprint

	pc      *dtlsPacketConn
	cancel  context.CancelFunc
	ctx     context.Context
	started bool
	// ready is closed when a client may send its first flight: the peer's
	// address is known and, with ICE, a check has authenticated it.
	ready     chan struct{}
	readyOnce sync.Once
}

func newDTLSSession(c *Cert) *dtlsSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &dtlsSession{cert: c, local: c.fp, ctx: ctx, cancel: cancel, ready: make(chan struct{})}
}

func (d *dtlsSession) markReady() { d.readyOnce.Do(func() { close(d.ready) }) }

func (d *dtlsSession) close() {
	d.cancel()
	if d.pc != nil {
		_ = d.pc.Close()
	}
}

// keysFromState derives the leg's two SRTP keys from the finished handshake
// (RFC 5764 section 4.2).
func keysFromState(st *dtls.State, prof dtls.SRTPProtectionProfile, isClient bool) (local, remote Crypto, err error) {
	suite, ok := suiteOfProfile(prof)
	if !ok {
		return Crypto{}, Crypto{}, fmt.Errorf("media: DTLS selected SRTP profile %v that Hello does not use", prof)
	}
	p, _, _, _ := suite.profile()
	cfg := &srtp.Config{Profile: p}
	if err := cfg.ExtractSessionKeysFromDTLS(st, isClient); err != nil {
		return Crypto{}, Crypto{}, fmt.Errorf("media: export SRTP keys: %w", err)
	}
	k := cfg.Keys
	local = Crypto{Suite: suite, key: append(append([]byte(nil), k.LocalMasterKey...), k.LocalMasterSalt...)}
	remote = Crypto{Suite: suite, key: append(append([]byte(nil), k.RemoteMasterKey...), k.RemoteMasterSalt...)}
	return local, remote, nil
}

// dtlsOptions is the handshake configuration for a session whose peer must
// present a certificate matching one of the fingerprints in want.
func dtlsOptions(cert *Cert, want []Fingerprint) []dtls.Option {
	return []dtls.Option{
		dtls.WithCertificates(cert.tls),
		dtls.WithSRTPProtectionProfiles(dtlsProfiles...),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		// The peer's certificate is self-signed and carries no name: it is
		// trusted by fingerprint alone, checked here.
		dtls.WithInsecureSkipVerify(true),
		dtls.WithVerifyPeerCertificate(func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) > 0 {
				for _, w := range want {
					if w.Match(raw[0]) {
						return nil
					}
				}
			}
			return errFingerprintMismatch
		}),
		dtls.WithFlightInterval(400 * time.Millisecond),
	}
}

// newDTLSConn makes the DTLS connection of one session over pc, as the client
// or the server (which asks the client for its certificate, since DTLS-SRTP
// authenticates both ends by fingerprint).
func newDTLSConn(pc net.PacketConn, remote net.Addr, client bool, cert *Cert, want []Fingerprint) (*dtls.Conn, error) {
	opts := dtlsOptions(cert, want)
	if client {
		co := make([]dtls.ClientOption, 0, len(opts))
		for _, o := range opts {
			co = append(co, o)
		}
		return dtls.ClientWithOptions(pc, remote, co...)
	}
	so := []dtls.ServerOption{dtls.WithClientAuth(dtls.RequireAnyClientCert)}
	for _, o := range opts {
		so = append(so, o)
	}
	return dtls.ServerWithOptions(pc, remote, so...)
}

// errFingerprintMismatch fails a handshake whose peer certificate does not
// match the fingerprint in its SDP.
var errFingerprintMismatch = errors.New("media: DTLS peer certificate does not match the SDP fingerprint")

var dtlsActive atomic.Int64

// DTLSHandshakesRunning is the number of handshakes in flight on this node.
func DTLSHandshakesRunning() int64 { return dtlsActive.Load() }

// SetDTLSTimeout changes how long a handshake may take (default 8 s) and
// returns a function that restores the old value. It is for tests; call it
// before any handshake starts.
func SetDTLSTimeout(d time.Duration) (restore func()) {
	old := dtlsHandshakeTimeout
	dtlsHandshakeTimeout = d
	return func() { dtlsHandshakeTimeout = old }
}
