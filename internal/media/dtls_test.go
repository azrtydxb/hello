package media

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/srtp/v3"
	"github.com/pion/stun/v3"
)

// dtlsPhone is a DTLS-SRTP endpoint built on pion/dtls and pion/srtp used
// directly (not Hello's key code): it owns a UDP socket, tells STUN, DTLS and
// RTP apart the way RFC 7983 says, runs the handshake over the DTLS records,
// and protects and unprotects media with the keys pion exports. It plays the
// part of a phone (or a browser) in front of the anchor.
type dtlsPhone struct {
	t     *testing.T
	conn  *net.UDPConn
	relay *net.UDPAddr
	cert  *Cert
	pc    *dtlsPacketConn
	rtp   chan []byte
	stun  chan []byte

	mu     sync.Mutex
	enc    *srtp.Context
	dec    *srtp.Context
	dc     *dtls.Conn
	seq    uint16
	ts     uint32
	suite  dtls.SRTPProtectionProfile
	client bool
}

func newDTLSPhone(t *testing.T, relayPort int) *dtlsPhone {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	cert, err := newCert()
	if err != nil {
		t.Fatal(err)
	}
	relay := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: relayPort}
	p := &dtlsPhone{t: t, conn: conn, relay: relay, cert: cert, rtp: make(chan []byte, 256), stun: make(chan []byte, 16)}
	p.pc = newDTLSPacketConn(conn.LocalAddr(), relay, func(b []byte) error {
		_, err := conn.WriteToUDP(b, relay)
		return err
	})
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			d := append([]byte(nil), buf[:n]...)
			switch {
			case isSTUN(d):
				select {
				case p.stun <- d:
				default:
				}
			case isDTLS(d):
				p.pc.push(d)
			default:
				select {
				case p.rtp <- d:
				default:
				}
			}
		}
	}()
	t.Cleanup(func() {
		if p.dc != nil {
			_ = p.dc.Close()
		}
		_ = p.pc.Close()
		_ = conn.Close()
	})
	return p
}

func (p *dtlsPhone) addr() *net.UDPAddr { return p.conn.LocalAddr().(*net.UDPAddr) }

// fingerprint is what the phone puts in its SDP.
func (p *dtlsPhone) fingerprint() Fingerprint { return p.cert.fp }

// handshake runs the DTLS handshake as the client or the server against
// Hello's fingerprint and sets up the SRTP contexts from the exported keys.
func (p *dtlsPhone) handshake(client bool, hello Fingerprint) error {
	p.t.Helper()
	dc, err := newDTLSConn(p.pc, p.relay, client, p.cert, []Fingerprint{hello})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := dc.HandshakeContext(ctx); err != nil {
		_ = dc.Close()
		return err
	}
	st, _ := dc.ConnectionState()
	prof, _ := dc.SelectedSRTPProtectionProfile()
	sp := srtp.ProtectionProfile(prof)
	scfg := &srtp.Config{Profile: sp}
	if err := scfg.ExtractSessionKeysFromDTLS(&st, client); err != nil {
		return err
	}
	k := scfg.Keys
	enc, err := srtp.CreateContext(k.LocalMasterKey, k.LocalMasterSalt, sp)
	if err != nil {
		return err
	}
	dec, err := srtp.CreateContext(k.RemoteMasterKey, k.RemoteMasterSalt, sp)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.dc, p.enc, p.dec, p.suite, p.client = dc, enc, dec, prof, client
	p.mu.Unlock()
	// A DTLS connection must be read for the handshake to settle (it answers
	// a retransmitted flight); application data is not used.
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

// sendMedia protects one RTP packet and sends it to the relay; it returns the
// bytes put on the wire.
func (p *dtlsPhone) sendMedia(pt uint8, payload []byte) []byte {
	p.t.Helper()
	p.mu.Lock()
	p.seq++
	p.ts += frameSamples
	plain := BuildRTP(pt, p.seq, p.ts, 4242, false, payload)
	enc := p.enc
	p.mu.Unlock()
	wire, err := enc.EncryptRTP(nil, plain, nil)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.conn.WriteToUDP(wire, p.relay); err != nil {
		p.t.Fatal(err)
	}
	return wire
}

// recvMedia waits for one RTP packet from the relay and decrypts it.
func (p *dtlsPhone) recvMedia(within time.Duration) (*RTPPacket, []byte, bool) {
	p.t.Helper()
	select {
	case wire := <-p.rtp:
		p.mu.Lock()
		dec := p.dec
		p.mu.Unlock()
		plain, err := dec.DecryptRTP(nil, wire, nil)
		if err != nil {
			p.t.Fatalf("phone cannot decrypt what the anchor sent: %v", err)
		}
		pkt, ok := ParseRTP(plain)
		if !ok {
			p.t.Fatal("decrypted media is not RTP")
		}
		return pkt, wire, true
	case <-time.After(within):
		return nil, nil, false
	}
}

// dtlsOffer builds the SDP a DTLS-SRTP phone sends.
func dtlsOffer(addr *net.UDPAddr, fp Fingerprint, setup, proto string, extra ...string) []byte {
	var b strings.Builder
	b.WriteString("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 " + addr.IP.String() + "\r\nt=0 0\r\n")
	b.WriteString("m=audio " + itoa(addr.Port) + " " + proto + " 0 101\r\n")
	b.WriteString("a=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\n")
	b.WriteString("a=fingerprint:" + fp.String() + "\r\na=setup:" + setup + "\r\na=rtcp-mux\r\n")
	for _, e := range extra {
		b.WriteString(e + "\r\n")
	}
	b.WriteString("a=sendrecv\r\n")
	return []byte(b.String())
}

func itoa(n int) string { return strconv.Itoa(n) }

// dtlsAnchor is a two-leg relay whose leg a faces a DTLS phone and whose leg
// b faces a plain RTP phone.
type dtlsAnchor struct {
	r       *Relay
	phone   *dtlsPhone
	plain   *relayPeer
	answer  AudioSDP
	failMu  sync.Mutex
	failure string
}

func (a *dtlsAnchor) failed() string {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	return a.failure
}

// newDTLSAnchor answers the phone's DTLS offer on leg a the way the SIP layer
// does (Relay.AnswerSDP) and starts the relay. setup is the phone's a=setup;
// phoneFP, when set, is the fingerprint the phone claims (a wrong one is the
// mismatch case).
func newDTLSAnchor(t *testing.T, setup string, claim *Fingerprint) *dtlsAnchor {
	t.Helper()
	r, err := NewRelay(testRelayMin, testRelayMax)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	portA, err := r.AddLeg("a")
	if err != nil {
		t.Fatal(err)
	}
	portB, err := r.AddLeg("b")
	if err != nil {
		t.Fatal(err)
	}
	r.SetPayloadTypes("a", 0, 101)
	r.SetPayloadTypes("b", 0, 101)
	d := &dtlsAnchor{r: r, phone: newDTLSPhone(t, portA)}
	d.plain = startPeer(t, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: portB})
	d.plain.only = portB
	r.OnSecureFail(func(leg, reason string) {
		d.failMu.Lock()
		d.failure = leg + ":" + reason
		d.failMu.Unlock()
	})
	fp := d.phone.fingerprint()
	if claim != nil {
		fp = *claim
	}
	offer, err := ParseAudioSDP(dtlsOffer(d.phone.addr(), fp, setup, "UDP/TLS/RTP/SAVP"))
	if err != nil {
		t.Fatal(err)
	}
	if !offer.OffersDTLS() || !offer.SRTPOnly {
		t.Fatalf("the DTLS offer was not recognised: %+v", offer)
	}
	r.SetTarget("a", d.phone.addr())
	r.SetTarget("b", d.plain.addr())
	ans, err := r.AnswerSDP("a", offer, offer.Narrow(0), SecurePolicy{Allow: true, Require: true})
	if err != nil {
		t.Fatalf("AnswerSDP: %v", err)
	}
	d.answer = ans
	r.Start()
	return d
}

// answerFingerprint is the fingerprint in the answer Hello built.
func answerFingerprint(t *testing.T, ans AudioSDP) (Fingerprint, string) {
	t.Helper()
	body := ans.Build("127.0.0.1", 4000, "sendrecv")
	parsed, err := ParseAudioSDP(body)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.OffersDTLS() {
		t.Fatalf("the answer carries no DTLS attributes:\n%s", body)
	}
	return parsed.DTLS.Fingerprints[0], parsed.DTLS.Setup
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestDTLSSRTPAnchor fails if a DTLS-SRTP phone cannot complete a handshake
// with the anchor and exchange SRTP through it with a plain RTP phone. The
// phone is built on pion/dtls and pion/srtp directly. Mutations: installing
// no keys, protecting leg a with the wrong key, or relaying before the
// handshake ends, fail one of the sub-tests.
func TestDTLSSRTPAnchor(t *testing.T) {
	for _, tc := range []struct {
		name        string
		phoneSetup  string
		wantSetup   string
		phoneClient bool
	}{
		// The phone offers actpass or passive: Hello is the DTLS client.
		{"hello is client", SetupActPass, SetupActive, false},
		{"hello is client against passive", SetupPassive, SetupActive, false},
		// The phone offers active: Hello is the DTLS server.
		{"hello is server", SetupActive, SetupPassive, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDTLSAnchor(t, tc.phoneSetup, nil)
			fp, setup := answerFingerprint(t, d.answer)
			if setup != tc.wantSetup {
				t.Fatalf("answer setup = %q, want %q", setup, tc.wantSetup)
			}
			if fp.Hash != "sha-256" {
				t.Fatalf("answer fingerprint hash = %q", fp.Hash)
			}
			if !strings.Contains(string(d.answer.Build("127.0.0.1", 4000, "sendrecv")), "m=audio 4000 UDP/TLS/RTP/SAVP ") {
				t.Fatal("the answer is not on the DTLS transport")
			}
			if tc.phoneClient {
				// Hello waits for the ClientHello.
				if err := d.phone.handshake(true, fp); err != nil {
					t.Fatalf("handshake: %v", err)
				}
			} else {
				// Hello sends the ClientHello once it knows the phone; the
				// phone, as server, answers it.
				if err := d.phone.handshake(false, fp); err != nil {
					t.Fatalf("handshake: %v", err)
				}
			}
			waitFor(t, "the leg to be encrypted", func() bool { return d.r.KeyExchange("a") == "dtls" })
			if got := d.r.SecureLegs(); got != 1 {
				t.Fatalf("secure legs = %d, want 1 (the plain phone's leg stays plain)", got)
			}
			if got := d.r.SecureMode("a"); got != "dtls" {
				t.Fatalf("mode = %q, want dtls", got)
			}
			if d.r.KeyExchange("b") != "" {
				t.Fatal("the plain leg reports a key exchange")
			}

			// SRTP from the DTLS phone arrives as plain RTP at the other phone.
			payload := bytes.Repeat([]byte{0x11, 0x77, 0xc3}, 53)
			wire := d.phone.sendMedia(0, payload)
			if bytes.Contains(wire, payload) {
				t.Fatal("the phone's media is not encrypted on the wire")
			}
			got := d.plain.recv(t)
			if !bytes.Equal(got.Payload, payload) {
				t.Fatal("audio through the anchor changed")
			}

			// Plain RTP from the other phone reaches the DTLS phone as SRTP.
			back := bytes.Repeat([]byte{0x2a, 0x91}, 80)
			d.plain.send(0, back, false)
			pkt, wireBack, ok := d.phone.recvMedia(3 * time.Second)
			if !ok {
				t.Fatal("the DTLS phone received nothing")
			}
			if bytes.Contains(wireBack, back) {
				t.Fatal("the anchor sent plain RTP to the DTLS phone")
			}
			if !bytes.Equal(pkt.Payload, back) {
				t.Fatal("audio towards the DTLS phone changed")
			}
			if d.failed() != "" {
				t.Fatalf("unexpected failure %q", d.failed())
			}
		})
	}

	t.Run("no media before the handshake ends", func(t *testing.T) {
		d := newDTLSAnchor(t, SetupActive, nil)
		// Plain RTP from the phone's address, before any handshake: dropped,
		// never relayed, never latched as an unencrypted stream.
		plain := BuildRTP(0, 1, 160, 9, false, bytes.Repeat([]byte{7}, 160))
		_, _ = d.phone.conn.WriteToUDP(plain, d.phone.relay)
		// And nothing flows the other way either.
		d.plain.send(0, bytes.Repeat([]byte{8}, 160), false)
		_ = d.plain.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, 2048)
		if _, _, err := d.plain.conn.ReadFromUDP(buf); err == nil {
			t.Fatal("plain media crossed the anchor before DTLS finished")
		}
		select {
		case <-d.phone.rtp:
			t.Fatal("the anchor sent media to a phone with no keys")
		case <-time.After(300 * time.Millisecond):
		}
		if got := d.r.SecureMode("a"); got != "dtls" {
			t.Fatalf("mode = %q, want dtls while the handshake is pending", got)
		}
	})

	t.Run("fingerprint mismatch fails the handshake", func(t *testing.T) {
		wrong := Fingerprint{Hash: "sha-256", Sum: bytes.Repeat([]byte{0xab}, 32)}
		d := newDTLSAnchor(t, SetupActive, &wrong)
		fp, _ := answerFingerprint(t, d.answer)
		_ = d.phone.handshake(true, fp) // the phone is fine with Hello; Hello is not with the phone
		waitFor(t, "the failure callback", func() bool { return d.failed() != "" })
		if d.failed() != "a:fingerprint" {
			t.Fatalf("failure = %q, want a:fingerprint", d.failed())
		}
		if d.r.KeyExchange("a") != "" {
			t.Fatal("a leg with a wrong certificate got keys")
		}
	})

	t.Run("a phone that checks Hello's fingerprint rejects another certificate", func(t *testing.T) {
		d := newDTLSAnchor(t, SetupActive, nil)
		wrong := Fingerprint{Hash: "sha-256", Sum: bytes.Repeat([]byte{0x01}, 32)}
		if err := d.phone.handshake(true, wrong); err == nil {
			t.Fatal("the phone accepted a certificate that does not match the answer's fingerprint")
		}
	})
}

// TestDTLSRekeyOnNewFingerprint fails if a re-INVITE that carries a new
// fingerprint does not start a new handshake and replace the keys, or if one
// that repeats the fingerprint disturbs the running session.
func TestDTLSRekeyOnNewFingerprint(t *testing.T) {
	d := newDTLSAnchor(t, SetupActive, nil)
	fp, _ := answerFingerprint(t, d.answer)
	if err := d.phone.handshake(true, fp); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "encryption", func() bool { return d.r.KeyExchange("a") == "dtls" })

	// A re-INVITE repeating the fingerprint keeps the session and its
	// fingerprint in the answer.
	same, err := ParseAudioSDP(dtlsOffer(d.phone.addr(), d.phone.fingerprint(), SetupActive, "UDP/TLS/RTP/SAVP"))
	if err != nil {
		t.Fatal(err)
	}
	ans, err := d.r.AnswerSDP("a", same, same.Narrow(0), SecurePolicy{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	fp2, _ := answerFingerprint(t, ans)
	if string(fp2.Sum) != string(fp.Sum) {
		t.Fatal("a repeated offer changed Hello's fingerprint")
	}
	d.phone.sendMedia(0, bytes.Repeat([]byte{3}, 160))
	d.plain.recv(t)

	// A new phone certificate: the old keys go, media stops until the new
	// handshake ends.
	p2 := newDTLSPhone(t, d.r.LegPort("a"))
	moved, err := ParseAudioSDP(dtlsOffer(p2.addr(), p2.fingerprint(), SetupActive, "UDP/TLS/RTP/SAVP"))
	if err != nil {
		t.Fatal(err)
	}
	d.r.SetTarget("a", p2.addr())
	ans2, err := d.r.AnswerSDP("a", moved, moved.Narrow(0), SecurePolicy{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.r.KeyExchange("a") == "dtls" {
		// Keys of the old session must be gone the moment the offer is taken.
		t.Fatal("the old session's keys survived a new fingerprint")
	}
	fp3, _ := answerFingerprint(t, ans2)
	if err := p2.handshake(true, fp3); err != nil {
		t.Fatalf("second handshake: %v", err)
	}
	waitFor(t, "encryption after the new handshake", func() bool { return d.r.KeyExchange("a") == "dtls" })
	p2.sendMedia(0, bytes.Repeat([]byte{5}, 160))
	if got := d.plain.recv(t); !bytes.Equal(got.Payload, bytes.Repeat([]byte{5}, 160)) {
		t.Fatal("audio after the rekey changed")
	}
}

// TestDTLSOfferedByHello fails if Hello, the offerer, does not offer
// actpass with a fingerprint on UDP/TLS/RTP/SAVP and then handshake in the
// role the phone's answer settles.
func TestDTLSOfferedByHello(t *testing.T) {
	for _, phoneSetup := range []string{SetupActive, SetupPassive} {
		t.Run(phoneSetup, func(t *testing.T) {
			r, err := NewRelay(testRelayMin, testRelayMax)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(r.Close)
			portA, _ := r.AddLeg("a")
			portB, _ := r.AddLeg("b")
			r.SetPayloadTypes("a", 0, 101)
			r.SetPayloadTypes("b", 0, 101)
			phone := newDTLSPhone(t, portA)
			plain := startPeer(t, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: portB})
			plain.only = portB
			r.SetTarget("a", phone.addr())
			r.SetTarget("b", plain.addr())
			base, err := ParseAudioSDP(BuildAudioSDP("127.0.0.1", portA, 0, 101, 8000))
			if err != nil {
				t.Fatal(err)
			}
			off, err := r.OfferSDP("a", base, SecurePolicy{Allow: true, Require: true, DTLS: true})
			if err != nil {
				t.Fatal(err)
			}
			body := string(off.Build("127.0.0.1", portA, "sendrecv"))
			for _, want := range []string{"m=audio " + itoa(portA) + " UDP/TLS/RTP/SAVP ", "a=fingerprint:sha-256 ", "a=setup:actpass", "a=rtcp-mux"} {
				if !strings.Contains(body, want) {
					t.Fatalf("offer lacks %q:\n%s", want, body)
				}
			}
			if strings.Contains(body, "a=crypto") {
				t.Fatal("a DTLS offer carries SDES keys")
			}
			offered, err := ParseAudioSDP([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			// The phone answers with its fingerprint and role.
			ans, err := ParseAudioSDP(dtlsOffer(phone.addr(), phone.fingerprint(), phoneSetup, "UDP/TLS/RTP/SAVP"))
			if err != nil {
				t.Fatal(err)
			}
			if !r.LearnAnswer("a", ans, true) {
				t.Fatal("the leg did not take the DTLS answer")
			}
			r.Start()
			phoneClient := phoneSetup == SetupActive
			if err := phone.handshake(phoneClient, offered.DTLS.Fingerprints[0]); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			waitFor(t, "encryption", func() bool { return r.KeyExchange("a") == "dtls" })
			phone.sendMedia(0, bytes.Repeat([]byte{9}, 160))
			if got := plain.recv(t); len(got.Payload) != 160 {
				t.Fatal("media did not cross")
			}
		})
	}

	t.Run("an answer without DTLS leaves the leg plain", func(t *testing.T) {
		r, err := NewRelay(testRelayMin, testRelayMax)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.Close)
		portA, _ := r.AddLeg("a")
		_, _ = r.AddLeg("b")
		base, _ := ParseAudioSDP(BuildAudioSDP("127.0.0.1", portA, 0, 101, 8000))
		if _, err := r.OfferSDP("a", base, SecurePolicy{Allow: true, DTLS: true}); err != nil {
			t.Fatal(err)
		}
		plainAns, _ := ParseAudioSDP(BuildAudioSDP("127.0.0.1", 4000, 0, 101, 8000))
		if r.LearnAnswer("a", plainAns, false) {
			t.Fatal("a plain answer counted as encrypted")
		}
		if r.SecureMode("a") != "" {
			t.Fatal("the leg is not plain")
		}
		// A later SDP of the dialog must not repeat the unanswered offer.
		again := r.KeepSDP("a", base).Build("127.0.0.1", portA, "sendrecv")
		if strings.Contains(string(again), "fingerprint") {
			t.Fatal("a plain leg keeps offering DTLS")
		}
	})
}

// TestDTLSRefusals fails if a DTLS offer is answered where it cannot be
// carried: on a node with DTLS off, or by an endpoint whose policy does not
// allow crypto.
func TestDTLSRefusals(t *testing.T) {
	var s Secure
	s.Bind(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, func([]byte, *net.UDPAddr) {}, nil)
	cert, err := newCert()
	if err != nil {
		t.Fatal(err)
	}
	offer, err := ParseAudioSDP(dtlsOffer(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4000}, cert.fp, SetupActPass, "UDP/TLS/RTP/SAVP"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerSDP(offer, offer.Narrow(0), SecurePolicy{}); err == nil {
		t.Fatal("a DTLS offer was answered by an endpoint that does not allow crypto")
	}
	SetFeatures(false, true)
	defer SetFeatures(true, true)
	if _, err := s.AnswerSDP(offer, offer.Narrow(0), SecurePolicy{Allow: true}); err == nil {
		t.Fatal("a DTLS offer was answered with DTLS off")
	}
	if _, err := (&Secure{}).AnswerSDP(offer, offer.Narrow(0), SecurePolicy{Allow: true}); err == nil {
		t.Fatal("a DTLS offer was answered on an endpoint with no socket")
	}
}

// TestFingerprint fails if a fingerprint does not round-trip, matches the
// wrong certificate, or an unsupported hash is accepted.
func TestFingerprint(t *testing.T) {
	c, err := NodeCertificate()
	if err != nil {
		t.Fatal(err)
	}
	f := c.Fingerprint()
	back, err := ParseFingerprint(f.String())
	if err != nil {
		t.Fatal(err)
	}
	if !back.Match(c.tls.Certificate[0]) {
		t.Fatal("the node certificate does not match its own fingerprint")
	}
	other, _ := newCert()
	if back.Match(other.tls.Certificate[0]) {
		t.Fatal("another certificate matched")
	}
	if c2, _ := NodeCertificate(); c2 != c {
		t.Fatal("the node certificate changes between calls")
	}
	for _, bad := range []string{"", "sha-256", "md5 AA:BB", "sha-256 AA:BB", "sha-256 ZZ", strings.Replace(f.String(), "sha-256", "sha-224", 1)} {
		if _, err := ParseFingerprint(bad); err == nil {
			t.Errorf("ParseFingerprint(%q) succeeded", bad)
		}
	}
	if got, err := ParseFingerprint(strings.ToLower(f.String())); err != nil || !got.Match(c.tls.Certificate[0]) {
		t.Error("lower-case digest not accepted")
	}
}

// ICE-lite -----------------------------------------------------------------

func stunCheck(t *testing.T, user, pwd string, controlled, useCand bool) *stun.Message {
	t.Helper()
	role := stun.AttrICEControlling
	if controlled {
		role = stun.AttrICEControlled
	}
	setters := []stun.Setter{stun.TransactionID, stun.BindingRequest, stun.NewUsername(user),
		stun.RawAttribute{Type: role, Value: []byte{1, 2, 3, 4, 5, 6, 7, 8}}}
	if useCand {
		setters = append(setters, stun.RawAttribute{Type: stun.AttrUseCandidate})
	}
	m, err := stun.Build(append(setters, stun.NewShortTermIntegrity(pwd), stun.Fingerprint)...)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func iceOffer(addr *net.UDPAddr, ufrag, pwd string, cands ...string) []byte {
	var b strings.Builder
	b.WriteString("v=0\r\no=- 1 1 IN IP4 " + addr.IP.String() + "\r\ns=-\r\nc=IN IP4 " + addr.IP.String() + "\r\nt=0 0\r\n")
	b.WriteString("m=audio " + itoa(addr.Port) + " RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\n")
	b.WriteString("a=ice-ufrag:" + ufrag + "\r\na=ice-pwd:" + pwd + "\r\na=rtcp-mux\r\n")
	for _, c := range cands {
		b.WriteString("a=candidate:" + c + "\r\n")
	}
	b.WriteString("a=sendrecv\r\n")
	return []byte(b.String())
}

// TestICELite fails if the anchor does not answer an ICE offer as ICE-lite
// (a=ice-lite, its credentials, one host candidate, rtcp-mux), does not
// answer authenticated STUN binding requests with the sender's address, or
// answers a request that does not authenticate. A source address the SDP does
// not name is accepted only after a valid check from it.
func TestICELite(t *testing.T) {
	r, err := NewRelay(testRelayMin, testRelayMax)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	portA, _ := r.AddLeg("a")
	portB, _ := r.AddLeg("b")
	r.SetPayloadTypes("a", 0, 101)
	r.SetPayloadTypes("b", 0, 101)
	relayAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: portA}
	plain := startPeer(t, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: portB})
	plain.only = portB
	r.SetTarget("b", plain.addr())

	// The phone's SDP names a public address its packets will not come from
	// (a NAT): only an authenticated check can vouch for the real source.
	named := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 40000}
	phoneUfrag, phonePwd := "phoneUfr", "phonePasswordAtLeast22chars"
	offer, err := ParseAudioSDP(iceOffer(named, phoneUfrag, phonePwd,
		"1 1 udp 2130706431 192.168.1.20 40000 typ host", "2 1 udp 1694498815 203.0.113.9 40000 typ srflx raddr 192.168.1.20 rport 40000"))
	if err != nil {
		t.Fatal(err)
	}
	if offer.ICE == nil || offer.ICE.Ufrag != phoneUfrag || len(offer.ICE.Candidates) != 2 || !offer.RTCPMux {
		t.Fatalf("ICE attributes not parsed: %+v", offer.ICE)
	}
	if offer.ICE.Candidates[1].Type != "srflx" || offer.ICE.Candidates[0].IP != "192.168.1.20" {
		t.Fatalf("candidates parsed wrongly: %+v", offer.ICE.Candidates)
	}
	r.SetTarget("a", named)
	ans, err := r.AnswerSDP("a", offer, offer.Narrow(0), SecurePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	body := string(ans.Build("127.0.0.1", portA, "sendrecv"))
	for _, want := range []string{"a=ice-lite", "a=ice-ufrag:", "a=ice-pwd:", "typ host", "a=rtcp-mux", "m=audio " + itoa(portA) + " RTP/AVP "} {
		if !strings.Contains(body, want) {
			t.Fatalf("answer lacks %q:\n%s", want, body)
		}
	}
	if strings.Count(body, "a=candidate:") != 1 || strings.Contains(body, "srflx") || strings.Contains(body, "relay") {
		t.Fatalf("an ICE-lite answer must carry exactly one host candidate:\n%s", body)
	}
	if !strings.Contains(body, "127.0.0.1 "+itoa(portA)+" typ host") {
		t.Fatalf("the candidate is not the relay address:\n%s", body)
	}
	parsed, err := ParseAudioSDP([]byte(body))
	if err != nil || parsed.ICE == nil || !parsed.ICE.Lite || !parsed.ICE.usable() {
		t.Fatalf("answer ICE = %+v, %v", parsed.ICE, err)
	}
	r.Start()

	phone, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	read := func(within time.Duration) (*stun.Message, bool) {
		buf := make([]byte, 1500)
		_ = phone.SetReadDeadline(time.Now().Add(within))
		n, _, err := phone.ReadFromUDP(buf)
		if err != nil {
			return nil, false
		}
		m := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
		if err := m.Decode(); err != nil {
			t.Fatalf("response is not STUN: %v", err)
		}
		return m, true
	}
	send := func(m *stun.Message) { _, _ = phone.WriteToUDP(m.Raw, relayAddr) }

	// Media from an address nothing vouches for is refused.
	rtp := func(seq uint16) {
		_, _ = phone.WriteToUDP(BuildRTP(0, seq, uint32(seq)*160, 55, false, bytes.Repeat([]byte{4}, 160)), relayAddr)
	}
	rtp(1)
	_ = plain.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := plain.conn.ReadFromUDP(make([]byte, 2048)); err == nil {
		t.Fatal("media from an unvouched address was relayed")
	}

	// Wrong password, wrong user, wrong ufrag: no answer.
	for name, m := range map[string]*stun.Message{
		"wrong password": stunCheck(t, parsed.ICE.Ufrag+":"+phoneUfrag, "not-the-password-at-all-22", false, false),
		"wrong ufrag":    stunCheck(t, "zzzzzzzz:"+phoneUfrag, parsed.ICE.Pwd, false, false),
		"wrong remote":   stunCheck(t, parsed.ICE.Ufrag+":otherone", parsed.ICE.Pwd, false, false),
	} {
		send(m)
		if resp, ok := read(300 * time.Millisecond); ok {
			t.Fatalf("%s was answered: %v", name, resp)
		}
	}
	rtp(2)
	_ = plain.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := plain.conn.ReadFromUDP(make([]byte, 2048)); err == nil {
		t.Fatal("a failed check vouched for its source")
	}

	// A peer that is also controlled gets a role conflict.
	send(stunCheck(t, parsed.ICE.Ufrag+":"+phoneUfrag, parsed.ICE.Pwd, true, false))
	resp, ok := read(2 * time.Second)
	if !ok || resp.Type.Class != stun.ClassErrorResponse {
		t.Fatalf("role conflict not reported: %v %v", resp, ok)
	}

	// A valid check is answered with the sender's address and authenticated
	// with the answer's password.
	check := stunCheck(t, parsed.ICE.Ufrag+":"+phoneUfrag, parsed.ICE.Pwd, false, true)
	send(check)
	resp, ok = read(2 * time.Second)
	if !ok {
		t.Fatal("no binding response")
	}
	if resp.Type != stun.BindingSuccess || string(resp.TransactionID[:]) != string(check.TransactionID[:]) {
		t.Fatalf("bad response %v", resp.Type)
	}
	var xor stun.XORMappedAddress
	if err := xor.GetFrom(resp); err != nil {
		t.Fatal(err)
	}
	want := phone.LocalAddr().(*net.UDPAddr)
	if !xor.IP.Equal(want.IP) || xor.Port != want.Port {
		t.Fatalf("mapped address %v:%d, want %v", xor.IP, xor.Port, want)
	}
	if err := stun.NewShortTermIntegrity(parsed.ICE.Pwd).Check(resp); err != nil {
		t.Fatalf("response integrity: %v", err)
	}
	if err := stun.Fingerprint.Check(resp); err != nil {
		t.Fatalf("response fingerprint: %v", err)
	}

	// The checked address is now a media source, and what the anchor sends
	// the phone goes there (the nominated pair).
	rtp(3)
	got := plain.recv(t)
	if len(got.Payload) != 160 {
		t.Fatal("media after the check did not cross")
	}
	plain.send(0, bytes.Repeat([]byte{6}, 160), false)
	buf := make([]byte, 2048)
	_ = phone.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := phone.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("the nominated address got no media: %v", err)
	}
	if p, ok := ParseRTP(buf[:n]); !ok || len(p.Payload) != 160 {
		t.Fatal("bad media towards the nominated address")
	}
	if !r.ICEActive("a") || r.ICEActive("b") {
		t.Fatal("ICE state is on the wrong legs")
	}
}

// TestICEOffNode fails if a node with ICE off still answers ICE attributes.
func TestICEOffNode(t *testing.T) {
	SetFeatures(true, false)
	defer SetFeatures(true, true)
	var s Secure
	s.Bind(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, func([]byte, *net.UDPAddr) {}, nil)
	offer, err := ParseAudioSDP(iceOffer(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 4000}, "abcdefgh", "abcdefghijklmnopqrstuvwx"))
	if err != nil {
		t.Fatal(err)
	}
	ans, err := s.AnswerSDP(offer, offer.Narrow(0), SecurePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ans.Build("127.0.0.1", 4000, "sendrecv")), "ice-") {
		t.Fatal("ICE is answered on a node with ICE off")
	}
}

// TestSDPSecurityAttributes fails if the SDP model loses or mis-places the ICE
// and DTLS attributes: session-level ones apply to the media section,
// media-level ones win, and a DTLS fingerprint on an RTP/AVP line is not a
// DTLS offer.
func TestSDPSecurityAttributes(t *testing.T) {
	fp := "sha-256 " + strings.Repeat("AB:", 31) + "AB"
	body := "v=0\r\no=- 1 1 IN IP4 10.0.0.1\r\ns=-\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\n" +
		"a=ice-lite\r\na=ice-ufrag:sessufrag\r\na=ice-pwd:sessionpasswordlongenough1\r\na=fingerprint:" + fp + "\r\na=setup:actpass\r\n" +
		"m=audio 4000 UDP/TLS/RTP/SAVPF 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\n" +
		"a=ice-ufrag:mediaufrag\r\na=rtcp-mux\r\na=candidate:1 1 udp 2130706431 10.0.0.1 4000 typ host\r\n"
	a, err := ParseAudioSDP([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if a.Proto != "UDP/TLS/RTP/SAVPF" || !a.SRTPOnly || !a.OffersDTLS() || !a.OffersCrypto() || a.OffersSDES() {
		t.Fatalf("transport not recognised: %+v", a)
	}
	if a.DTLS.Setup != "actpass" || len(a.DTLS.Fingerprints) != 1 {
		t.Fatalf("DTLS = %+v", a.DTLS)
	}
	if a.ICE == nil || a.ICE.Ufrag != "mediaufrag" || a.ICE.Pwd != "sessionpasswordlongenough1" || !a.ICE.Lite || len(a.ICE.Candidates) != 1 {
		t.Fatalf("ICE = %+v", a.ICE)
	}
	if !a.RTCPMux {
		t.Fatal("rtcp-mux lost")
	}
	// An answer to a SAVPF offer stays SAVPF.
	out := string(a.WithDTLS(a.Proto, DTLSInfo{Fingerprints: []Fingerprint{mustFP(t, fp)}, Setup: SetupActive}).Build("1.2.3.4", 5000, "sendrecv"))
	if !strings.Contains(out, "m=audio 5000 UDP/TLS/RTP/SAVPF 0 101") || !strings.Contains(out, "a=setup:active") {
		t.Fatalf("answer:\n%s", out)
	}
	if !strings.Contains(a.PlainOffer().String2(), "RTP/AVP") {
		t.Fatal("PlainOffer kept the DTLS transport")
	}
	// A fingerprint on an RTP/AVP line is not an offer of DTLS-SRTP.
	hyb := strings.Replace(body, "UDP/TLS/RTP/SAVPF", "RTP/AVP", 1)
	h, err := ParseAudioSDP([]byte(hyb))
	if err != nil {
		t.Fatal(err)
	}
	if h.OffersDTLS() || h.SRTPOnly {
		t.Fatal("a fingerprint on RTP/AVP counted as DTLS")
	}
	// Replicated state keeps no secret.
	stripped := StripCrypto("a=crypto:1 AES_CM_128_HMAC_SHA1_80 inline:xx\r\na=ice-pwd:secret\r\na=fingerprint:" + fp + "\r\n")
	if strings.Contains(stripped, "crypto") || strings.Contains(stripped, "ice-pwd") || !strings.Contains(stripped, "fingerprint") {
		t.Fatalf("StripCrypto = %q", stripped)
	}
}

func mustFP(t *testing.T, v string) Fingerprint {
	t.Helper()
	f, err := ParseFingerprint(v)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// String2 is a test aid: the SDP the value would build.
func (a AudioSDP) String2() string { return string(a.Build("1.1.1.1", 1000, "sendrecv")) }
