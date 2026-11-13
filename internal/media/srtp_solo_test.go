package media

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// soloPhone is a phone speaking SDES SRTP to one of Hello's own media objects
// (a voicemail session, a conference participant, a page): its socket and the
// keys it sends and receives with.
type soloPhone struct {
	conn  *net.UDPConn
	key   Crypto     // the key the phone sends with, offered in its SDP
	proto *Protector // set once Hello's answer is known
	seq   uint16
	ts    uint32
}

func newSoloPhone(t *testing.T) *soloPhone {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	key, err := NewCrypto(1, SuiteAES128SHA1_80)
	if err != nil {
		t.Fatal(err)
	}
	return &soloPhone{conn: conn, key: key}
}

func (p *soloPhone) port() int { return p.conn.LocalAddr().(*net.UDPAddr).Port }

// offer is the phone's SDP: PCMU and telephone-event, with its key, RTP/SAVP
// when savp.
func (p *soloPhone) offer(savp bool) []byte {
	base, err := ParseAudioSDP(BuildAudioSDP("127.0.0.1", p.port(), 0, 101, sampleRateHz))
	if err != nil {
		panic(err)
	}
	return base.WithCrypto(savp, p.key).Build("127.0.0.1", p.port(), "sendrecv")
}

// settle reads Hello's answer: its key, the media port, and builds the
// phone's protector.
func (p *soloPhone) settle(t *testing.T, answer []byte, savp bool) int {
	t.Helper()
	a, err := ParseAudioSDP(answer)
	if err != nil {
		t.Fatal(err)
	}
	if a.SRTPOnly != savp {
		t.Fatalf("answer profile savp = %v, want %v", a.SRTPOnly, savp)
	}
	if len(a.Crypto) != 1 {
		t.Fatalf("answer carries %d crypto lines, want 1", len(a.Crypto))
	}
	if p.proto, err = NewEndpoint(p.key, a.Crypto[0]); err != nil {
		t.Fatal(err)
	}
	return a.Port
}

func (p *soloPhone) packet(pt uint8, payload []byte, marker bool) []byte {
	p.seq++
	p.ts += frameSamples
	return BuildRTP(pt, p.seq, p.ts, 4242, marker, payload)
}

// sendTo sends one RTP packet to port, protected when secure.
func (p *soloPhone) sendTo(t *testing.T, port int, secure bool, pkt []byte) {
	t.Helper()
	if secure {
		var err error
		if pkt, err = p.proto.Protect(pkt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.conn.WriteToUDP(pkt, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}); err != nil {
		t.Fatal(err)
	}
}

// read returns the next datagram sent to the phone.
func (p *soloPhone) read(within time.Duration) ([]byte, bool) {
	buf := make([]byte, rtpMaxPacket)
	_ = p.conn.SetReadDeadline(time.Now().Add(within))
	n, _, err := p.conn.ReadFromUDP(buf)
	if err != nil {
		return nil, false
	}
	return append([]byte(nil), buf[:n]...), true
}

// TestSRTPVoicemailSession fails if a voicemail session answers a caller's
// SDES offer in plain RTP: the answer must carry Hello's key, what the
// session plays must be SRTP that only the caller's key opens, what the
// caller records must be SRTP (a plain packet is ignored) and a policy that
// requires SRTP must refuse an offer without crypto instead of answering in
// clear.
func TestSRTPVoicemailSession(t *testing.T) {
	a := NewAnchor("127.0.0.1", testLogger(t))
	t.Run("play and record are encrypted", func(t *testing.T) {
		ph := newSoloPhone(t)
		ans, s, err := a.AnswerSecure(ph.offer(true), SecurePolicy{Allow: true, Require: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		port := ph.settle(t, ans, true)
		sec := s.(SecureSession).Secure()
		if _, on := sec.Suite(); !on {
			t.Fatal("the session is not encrypted after the answer")
		}

		// What the session plays is SRTP: it opens with the caller's key and
		// is not the plain packet.
		pcm := make([]byte, 2*160)
		for i := range pcm {
			pcm[i] = byte(i)
		}
		go func() { _ = s.Play(context.Background(), pcm) }()
		raw, ok := ph.read(2 * time.Second)
		if !ok {
			t.Fatal("nothing played")
		}
		plain, err := ph.proto.Unprotect(raw)
		if err != nil {
			t.Fatalf("the played packet is not SRTP for the caller's key: %v", err)
		}
		if pkt, ok := ParseRTP(plain); !ok || pkt.PayloadType != 0 {
			t.Fatalf("decrypted packet = %+v, want PCMU", pkt)
		}
		if bytes.Equal(raw, plain) {
			t.Fatal("the session sent plain RTP")
		}

		// A plain digit is dropped; the protected one ends the recording.
		dtmf := make(chan byte, 4)
		done := make(chan struct{})
		go func() {
			_, _ = s.Record(context.Background(), 5*time.Second, dtmf)
			close(done)
		}()
		ph.sendTo(t, port, false, ph.packet(101, []byte{'#', 0x80, 0, 0x40}, true))
		select {
		case <-done:
			t.Fatal("a plain RTP digit stopped an encrypted recording")
		case <-time.After(400 * time.Millisecond):
		}
		ph.sendTo(t, port, true, ph.packet(101, []byte{'#', 0x80, 0, 0x40}, true))
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("the protected digit did not end the recording")
		}
	})
	t.Run("required without crypto is refused", func(t *testing.T) {
		ph := newSoloPhone(t)
		plain := BuildAudioSDP("127.0.0.1", ph.port(), 0, 101, sampleRateHz)
		if _, s, err := a.AnswerSecure(plain, SecurePolicy{Allow: true, Require: true}); !errors.Is(err, ErrSRTPRequired) {
			if s != nil {
				_ = s.Close()
			}
			t.Fatalf("err = %v, want ErrSRTPRequired", err)
		}
		if _, s, err := a.AnswerSecure(ph.offer(true), SecurePolicy{Allow: false, Require: true}); !errors.Is(err, ErrSRTPRequired) {
			if s != nil {
				_ = s.Close()
			}
			t.Fatalf("a leg that may not carry crypto: err = %v, want ErrSRTPRequired", err)
		}
	})
	t.Run("optional falls back to plain", func(t *testing.T) {
		ph := newSoloPhone(t)
		plain := BuildAudioSDP("127.0.0.1", ph.port(), 0, 101, sampleRateHz)
		ans, s, err := a.AnswerSecure(plain, SecurePolicy{Allow: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if p, _ := ParseAudioSDP(ans); len(p.Crypto) != 0 || p.SRTPOnly {
			t.Fatal("a plain offer was answered with crypto")
		}
	})
}

// TestSRTPConferenceParty fails if a conference participant that offered SDES
// is mixed in plain RTP: the answer must carry a key, the participant's
// packets must be authenticated before they reach the mixer (a plain packet
// adds nothing), and what the mixer sends it must be SRTP.
func TestSRTPConferenceParty(t *testing.T) {
	ph := newSoloPhone(t)
	ans, party, err := NewPartySecure("127.0.0.1", 0, 0, ph.offer(false), SecurePolicy{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(party.Close)
	port := ph.settle(t, ans, false)
	if _, on := party.Secure().Suite(); !on {
		t.Fatal("the participant is not encrypted")
	}
	frame := bytes.Repeat([]byte{0x55}, 160)
	// Plain audio from an encrypted participant is not mixed.
	for range 3 {
		ph.sendTo(t, port, false, ph.packet(0, frame, false))
	}
	time.Sleep(150 * time.Millisecond)
	if rx, _, _ := party.Stats(); rx != 0 {
		t.Fatalf("%d plain packets reached the mixer of an encrypted participant", rx)
	}
	for range 3 {
		ph.sendTo(t, port, true, ph.packet(0, frame, false))
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rx, _, _ := party.Stats(); rx >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rx, _, _ := party.Stats(); rx != 3 {
		t.Fatalf("rx = %d, want the 3 protected packets", rx)
	}
	// The mix back to the participant is SRTP.
	if err := party.send(make([]int16, MixFrame)); err != nil {
		t.Fatal(err)
	}
	raw, ok := ph.read(2 * time.Second)
	if !ok {
		t.Fatal("nothing sent to the participant")
	}
	plain, err := ph.proto.Unprotect(raw)
	if err != nil {
		t.Fatalf("the mix is not SRTP for the participant's key: %v", err)
	}
	if bytes.Equal(raw, plain) {
		t.Fatal("the mix was sent in plain RTP")
	}
}

func TestSRTPConferencePartyRequired(t *testing.T) {
	ph := newSoloPhone(t)
	plain := BuildAudioSDP("127.0.0.1", ph.port(), 0, 101, sampleRateHz)
	if _, p, err := NewPartySecure("127.0.0.1", 0, 0, plain, SecurePolicy{Allow: true, Require: true}); !errors.Is(err, ErrSRTPRequired) {
		if p != nil {
			p.Close()
		}
		t.Fatalf("err = %v, want ErrSRTPRequired", err)
	}
}

// TestSRTPFanout fails if a page is fanned out in plain RTP to a member that
// negotiated SRTP, if a member without SRTP stops getting plain RTP, if the
// caller's own SRTP is not authenticated on the way in, or if the member
// stream shares a key across members.
func TestSRTPFanout(t *testing.T) {
	f, err := NewFanout(31200, 31300, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	caller := newSoloPhone(t)
	cans, err := f.CallerSecure().Accept([]Crypto{caller.key}, false)
	if err != nil {
		t.Fatal(err)
	}
	if caller.proto, err = NewEndpoint(caller.key, cans); err != nil {
		t.Fatal(err)
	}
	member := func(secure bool) (*soloPhone, *Secure) {
		m := newSoloPhone(t)
		if !secure {
			return m, nil
		}
		sec := &Secure{}
		offered, err := sec.Local()
		if err != nil {
			t.Fatal(err)
		}
		mk, err := NewCrypto(offered[0].Tag, offered[0].Suite)
		if err != nil {
			t.Fatal(err)
		}
		if !sec.ApplyAnswer([]Crypto{mk}, false) {
			t.Fatal("the member's answer was refused")
		}
		m.key = mk
		if m.proto, err = NewEndpoint(mk, offered[0]); err != nil {
			t.Fatal(err)
		}
		return m, sec
	}
	m1, s1 := member(true)
	m2, s2 := member(true)
	m3, _ := member(false)
	f.AddSecureTarget("m1", m1.conn.LocalAddr().(*net.UDPAddr), s1)
	f.AddSecureTarget("m2", m2.conn.LocalAddr().(*net.UDPAddr), s2)
	f.AddTarget("m3", m3.conn.LocalAddr().(*net.UDPAddr))

	payload := []byte{9, 8, 7, 6, 5, 4, 3, 2}
	// A plain packet from an encrypted caller is not fanned out.
	caller.sendTo(t, f.CallerPort(), false, caller.packet(0, payload, false))
	for i, m := range []*soloPhone{m1, m2, m3} {
		if raw, ok := m.read(300 * time.Millisecond); ok {
			t.Fatalf("member %d heard a plain packet from an encrypted caller (%d bytes)", i+1, len(raw))
		}
	}
	caller.sendTo(t, f.CallerPort(), true, caller.packet(0, payload, false))
	var wires [][]byte
	for i, m := range []*soloPhone{m1, m2} {
		raw, ok := m.read(2 * time.Second)
		if !ok {
			t.Fatalf("secure member %d got nothing", i+1)
		}
		plain, err := m.proto.Unprotect(raw)
		if err != nil {
			t.Fatalf("member %d: the page is not SRTP under its own key: %v", i+1, err)
		}
		if pkt, ok := ParseRTP(plain); !ok || !bytes.Equal(pkt.Payload, payload) {
			t.Fatalf("member %d decrypted %v, want the caller's audio", i+1, plain)
		}
		wires = append(wires, raw)
	}
	if bytes.Equal(wires[0], wires[1]) {
		t.Fatal("two members got the same ciphertext: they share a key")
	}
	if _, err := m2.proto.Unprotect(wires[0]); err == nil {
		t.Fatal("member 2's key opens member 1's stream")
	}
	raw, ok := m3.read(2 * time.Second)
	if !ok {
		t.Fatal("the plain member got nothing")
	}
	if pkt, ok := ParseRTP(raw); !ok || !bytes.Equal(pkt.Payload, payload) {
		t.Fatal("the plain member did not get plain RTP")
	}
}

// TestSecureSDPHelpers covers the shared SDP helpers: a required endpoint
// offers RTP/SAVP with its keys, a kept SDP repeats the active key, and an
// answer lacking crypto does not switch a plain endpoint on.
func TestSecureSDPHelpers(t *testing.T) {
	base, _ := ParseAudioSDP(BuildAudioSDP("127.0.0.1", 4000, 0, 101, sampleRateHz))
	s := &Secure{}
	off, err := s.OfferSDP(base, SecurePolicy{Allow: true, Require: true})
	if err != nil {
		t.Fatal(err)
	}
	body := off.Build("127.0.0.1", 4000, "sendrecv")
	parsed, _ := ParseAudioSDP(body)
	if !parsed.SRTPOnly || len(parsed.Crypto) != 2 {
		t.Fatalf("offer savp=%v crypto=%d, want RTP/SAVP with 2 suites", parsed.SRTPOnly, len(parsed.Crypto))
	}
	if _, err := (&Secure{}).OfferSDP(base, SecurePolicy{Require: true}); !errors.Is(err, ErrSRTPRequired) {
		t.Fatalf("a required endpoint that may not carry crypto: err = %v", err)
	}
	s.LearnSDP(base) // no crypto: nothing happens
	if _, on := s.Suite(); on {
		t.Fatal("an answer without crypto switched the endpoint on")
	}
	mk, _ := NewCrypto(parsed.Crypto[0].Tag, parsed.Crypto[0].Suite)
	s.LearnSDP(base.WithCrypto(true, mk).asAnswer(mk))
	if savp, on := s.Profile(); !on || !savp {
		t.Fatalf("after the answer: on=%v savp=%v, want an encrypted RTP/SAVP endpoint", on, savp)
	}
	kept := s.KeepSDP(base)
	if k, _ := ParseAudioSDP(kept.Build("127.0.0.1", 4000, "sendrecv")); len(k.Crypto) != 1 || !k.SRTPOnly {
		t.Fatalf("a kept SDP carries %d keys, savp=%v", len(k.Crypto), k.SRTPOnly)
	}
}

// asAnswer is a test helper: the far end's SDP as parsed after it answered
// with key.
func (a AudioSDP) asAnswer(key Crypto) AudioSDP {
	body := a.Build("127.0.0.1", 4000, "sendrecv")
	parsed, _ := ParseAudioSDP(body)
	parsed.SRTPOnly, parsed.Crypto = true, []Crypto{key}
	return parsed
}

// TestSRTPStreamStartsBelowWrap fails if an outbound stream may begin within
// reach of the 16-bit sequence wrap. SRTP's rollover counter is learned as 0
// by a receiver; one that misses the packets before the wrap never
// authenticates the stream again (the CI flake of TestSRTPConferenceRoom).
func TestSRTPStreamStartsBelowWrap(t *testing.T) {
	for range 4096 {
		ph := newSoloPhone(t)
		_, party, err := NewPartySecure("127.0.0.1", 0, 0, ph.offer(false), SecurePolicy{Allow: true})
		if err != nil {
			t.Fatal(err)
		}
		seq := party.seq
		party.Close()
		if seq >= 0x8000 {
			t.Fatalf("party stream starts at sequence %d, in the upper half", seq)
		}
	}
	var o rewriteState
	for range 4096 {
		if err := o.reset(); err != nil {
			t.Fatal(err)
		}
		if o.seq >= 0x8000 {
			t.Fatalf("relay stream starts at sequence %d, in the upper half", o.seq)
		}
	}
}

// TestSRTPLateReceiverAfterWrap documents why: a receiver whose first packet
// follows the wrap reads it under rollover 0 and the sender used 1.
func TestSRTPLateReceiverAfterWrap(t *testing.T) {
	ph := newSoloPhone(t)
	ans, party, err := NewPartySecure("127.0.0.1", 0, 0, ph.offer(false), SecurePolicy{Allow: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(party.Close)
	ph.settle(t, ans, false)
	party.seq = 65534
	for range 6 {
		if err := party.send(make([]int16, MixFrame)); err != nil {
			t.Fatal(err)
		}
	}
	var heard int
	for i := 0; ; i++ {
		raw, ok := ph.read(300 * time.Millisecond)
		if !ok {
			break
		}
		if i < 4 {
			continue // the receiver was not listening yet
		}
		if _, err := ph.proto.Unprotect(raw); err == nil {
			heard++
		}
	}
	if heard != 0 {
		t.Fatalf("a receiver that missed the packets before the wrap decrypted %d, the premise of startSeq is gone", heard)
	}
}
