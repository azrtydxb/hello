package media

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// secureRelay builds a two-leg relay whose legs both run SRTP, negotiated the
// way the SIP layer does it: leg a answers the caller's offer (AcceptCrypto),
// leg b offers its own key and the callee answers with one
// (ApplyAnswerCrypto). The returned phones speak SRTP with the keys an
// endpoint would hold: send is the key it sends with, recv the key it
// receives with.
type secureSetup struct {
	r              *Relay
	phoneA, phoneB *relayPeer
	srtpA, srtpB   *Protector
	// localA and localB are Hello's keys on the two legs.
	localA, localB Crypto
}

func newSecureRelay(t *testing.T) *secureSetup {
	t.Helper()
	r, pa, pb := testRelay(t)
	callerKey, err := NewCrypto(1, SuiteAES128SHA1_80)
	if err != nil {
		t.Fatal(err)
	}
	// Leg a: the caller offered callerKey; Hello answers with its own.
	localA, err := r.AcceptCrypto("a", []Crypto{callerKey}, false)
	if err != nil {
		t.Fatalf("AcceptCrypto: %v", err)
	}
	// Leg b: Hello offers, the callee answers with its key on the same tag.
	offered, err := r.LocalCrypto("b")
	if err != nil {
		t.Fatal(err)
	}
	calleeKey, err := NewCrypto(offered[0].Tag, offered[0].Suite)
	if err != nil {
		t.Fatal(err)
	}
	if !r.ApplyAnswerCrypto("b", []Crypto{calleeKey}, false) {
		t.Fatal("ApplyAnswerCrypto refused a matching answer")
	}
	sa, err := NewEndpoint(callerKey, localA)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := NewEndpoint(calleeKey, offered[0])
	if err != nil {
		t.Fatal(err)
	}
	return &secureSetup{r: r, phoneA: pa, phoneB: pb, srtpA: sa, srtpB: sb, localA: localA, localB: offered[0]}
}

// sendSRTP protects a plain RTP packet with p and sends it from phone to the
// relay leg.
func sendSRTP(t *testing.T, phone *relayPeer, p *Protector, pt uint8, payload []byte) []byte {
	t.Helper()
	phone.seq++
	plain := BuildRTP(pt, phone.seq, phone.ts, phone.ssrc, false, payload)
	phone.ts += frameSamples
	enc, err := p.Protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phone.conn.WriteToUDP(enc, phone.to); err != nil {
		t.Fatal(err)
	}
	return enc
}

// readRaw reads the next datagram addressed to phone.
func readRaw(t *testing.T, phone *relayPeer, within time.Duration) ([]byte, bool) {
	t.Helper()
	buf := make([]byte, rtpMaxPacket)
	_ = phone.conn.SetReadDeadline(time.Now().Add(within))
	n, _, err := phone.conn.ReadFromUDP(buf)
	if err != nil {
		return nil, false
	}
	return append([]byte(nil), buf[:n]...), true
}

// TestSRTPAnchor fails if an anchored SDES call does not protect both legs
// with their own keys: audio sent SRTP into leg a must reach leg b as SRTP
// under leg b's key (and not under leg a's), a packet that fails
// authentication or replays must not be relayed, and no key may appear in any
// formatted value. Mutation: encrypting leg b's output with leg a's key, or
// skipping the unprotect on receive, fails the round trip; relaying a packet
// that failed authentication fails the injection cases.
func TestSRTPAnchor(t *testing.T) {
	t.Run("round trip with per-leg keys", func(t *testing.T) {
		s := newSecureRelay(t)
		if s.localA.sameKey(s.localB) {
			t.Fatal("both legs use the same key")
		}
		if got := s.r.SecureLegs(); got != 2 {
			t.Fatalf("secure legs = %d, want 2", got)
		}
		payload := bytes.Repeat([]byte{0x55, 0xaa, 0x12, 0x7e}, 40)
		wire := sendSRTP(t, s.phoneA, s.srtpA, 0, payload)
		if bytes.Contains(wire, payload[:32]) {
			t.Fatal("the caller's packet is not encrypted on the wire")
		}
		raw, ok := readRaw(t, s.phoneB, 3*time.Second)
		if !ok {
			t.Fatal("nothing reached the callee")
		}
		if bytes.Contains(raw, payload[:32]) {
			t.Fatal("the relay sent the callee clear audio on an SRTP leg")
		}
		plain, err := s.srtpB.Unprotect(raw)
		if err != nil {
			t.Fatalf("the callee cannot decrypt leg b's output with its key: %v", err)
		}
		pkt, ok := ParseRTP(plain)
		if !ok || !bytes.Equal(pkt.Payload, payload) {
			t.Fatal("the decrypted audio differs from what the caller sent")
		}
		if pkt.SSRC == s.phoneA.ssrc {
			t.Error("the callee saw the caller's SSRC: the relay did not rewrite")
		}
		// The other direction, under the same two keys.
		back := bytes.Repeat([]byte{0x7f}, 160)
		sendSRTP(t, s.phoneB, s.srtpB, 0, back)
		raw, ok = readRaw(t, s.phoneA, 3*time.Second)
		if !ok {
			t.Fatal("nothing reached the caller")
		}
		plain, err = s.srtpA.Unprotect(raw)
		if err != nil {
			t.Fatalf("the caller cannot decrypt leg a's output: %v", err)
		}
		if pkt, ok := ParseRTP(plain); !ok || !bytes.Equal(pkt.Payload, back) {
			t.Fatal("the audio toward the caller differs")
		}
		// Per-leg keys: what leg b sends is not valid under leg a's key.
		if _, err := s.srtpA.Unprotect(func() []byte {
			sendSRTP(t, s.phoneA, s.srtpA, 0, payload)
			r, _ := readRaw(t, s.phoneB, 3*time.Second)
			return r
		}()); err == nil {
			t.Fatal("a packet from leg b decrypts under leg a's key: the legs share a key")
		}
	})

	t.Run("the recording tap and DTMF see clear audio", func(t *testing.T) {
		s := newSecureRelay(t)
		got := make(chan []byte, 1)
		s.r.OnPacket(func(leg string, pkt *RTPPacket) {
			if leg == "a" {
				select {
				case got <- append([]byte(nil), pkt.Payload...):
				default:
				}
			}
		})
		payload := bytes.Repeat([]byte{9}, 160)
		sendSRTP(t, s.phoneA, s.srtpA, 0, payload)
		select {
		case p := <-got:
			if !bytes.Equal(p, payload) {
				t.Fatal("the tap saw ciphertext")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the tap saw nothing")
		}
	})

	t.Run("injected packets are not relayed", func(t *testing.T) {
		s := newSecureRelay(t)
		// A forged packet: protected with a key the relay does not hold.
		other, _ := NewCrypto(1, SuiteAES128SHA1_80)
		forger, err := NewEndpoint(other, s.localA)
		if err != nil {
			t.Fatal(err)
		}
		sendSRTP(t, s.phoneA, forger, 0, make([]byte, 160))
		// A plain packet on an SRTP leg.
		s.phoneA.send(0, make([]byte, 160), false)
		if raw, ok := readRaw(t, s.phoneB, 400*time.Millisecond); ok {
			t.Fatalf("a packet that failed authentication was relayed (%d bytes)", len(raw))
		}
		// A genuine packet still gets through, and the refusals were counted.
		sendSRTP(t, s.phoneA, s.srtpA, 0, make([]byte, 160))
		if _, ok := readRaw(t, s.phoneB, 3*time.Second); !ok {
			t.Fatal("a genuine packet did not get through after the forgeries")
		}
		st := s.r.Metrics().Directions["a>b"]
		if st.AuthFailed < 2 {
			t.Fatalf("auth failures counted = %d, want at least 2", st.AuthFailed)
		}
	})

	t.Run("a replayed packet is dropped", func(t *testing.T) {
		s := newSecureRelay(t)
		wire := sendSRTP(t, s.phoneA, s.srtpA, 0, make([]byte, 160))
		if _, ok := readRaw(t, s.phoneB, 3*time.Second); !ok {
			t.Fatal("the first copy was not relayed")
		}
		_, _ = s.phoneA.conn.WriteToUDP(wire, s.phoneA.to)
		if raw, ok := readRaw(t, s.phoneB, 400*time.Millisecond); ok {
			t.Fatalf("a replayed packet was relayed (%d bytes)", len(raw))
		}
	})

	t.Run("SRTCP on the RTP port is checked and consumed", func(t *testing.T) {
		s := newSecureRelay(t)
		// A sender report (PT 200) protected as SRTCP under the caller's key.
		sr := []byte{0x80, 200, 0, 6, 0, 0, 3, 9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		enc, err := s.srtpA.ProtectRTCP(sr)
		if err != nil {
			t.Fatal(err)
		}
		// The same protection round-trips between two standalone endpoints.
		k1, _ := NewCrypto(1, SuiteAES128SHA1_80)
		k2, _ := NewCrypto(1, SuiteAES128SHA1_80)
		tx, _ := NewEndpoint(k1, k2)
		rx, _ := NewEndpoint(k2, k1)
		wire, err := tx.ProtectRTCP(sr)
		if err != nil {
			t.Fatal(err)
		}
		if plain, err := rx.UnprotectRTCP(wire); err != nil || !bytes.Equal(plain, sr) {
			t.Fatalf("SRTCP round trip: %v", err)
		}
		_, _ = s.phoneA.conn.WriteToUDP(enc, s.phoneA.to)
		if raw, ok := readRaw(t, s.phoneB, 400*time.Millisecond); ok {
			t.Fatalf("RTCP was relayed as media (%d bytes)", len(raw))
		}
		if st := s.r.Metrics().Directions["a>b"]; st.AuthFailed != 0 {
			t.Fatalf("a valid SRTCP packet counted as an auth failure")
		}
	})

	t.Run("announcements into an SRTP leg are encrypted", func(t *testing.T) {
		s := newSecureRelay(t)
		pcm := make([]byte, 2*160*3)
		for i := range pcm {
			pcm[i] = byte(i * 7)
		}
		done := make(chan error, 1)
		go func() { done <- s.r.PlayTo(t.Context(), "b", pcm) }()
		raw, ok := readRaw(t, s.phoneB, 3*time.Second)
		if !ok {
			t.Fatal("no announcement packet")
		}
		if _, ok := ParseRTP(raw); !ok {
			t.Fatal("not RTP")
		}
		if _, err := s.srtpB.Unprotect(raw); err != nil {
			t.Fatalf("announcement packet is not SRTP under leg b's key: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatalf("PlayTo: %v", err)
		}
	})

	t.Run("a leg without crypto stays plain RTP", func(t *testing.T) {
		r, pa, pb := testRelay(t)
		if r.SecureLegs() != 0 {
			t.Fatal("a fresh relay claims SRTP legs")
		}
		pa.send(0, make([]byte, 160), false)
		if got := pb.recv(t); got == nil {
			t.Fatal("plain audio did not flow")
		}
	})

	t.Run("a rekey on a re-INVITE keeps the call going", func(t *testing.T) {
		s := newSecureRelay(t)
		newKey, _ := NewCrypto(1, SuiteAES128SHA1_80)
		if !s.r.UpdateRemoteCrypto("a", []Crypto{newKey}) {
			t.Fatal("rekey refused")
		}
		p2, err := NewEndpoint(newKey, s.localA)
		if err != nil {
			t.Fatal(err)
		}
		sendSRTP(t, s.phoneA, p2, 0, make([]byte, 160))
		raw, ok := readRaw(t, s.phoneB, 3*time.Second)
		if !ok {
			t.Fatal("nothing after the rekey")
		}
		if _, err := s.srtpB.Unprotect(raw); err != nil {
			t.Fatalf("leg b's send key changed with leg a's rekey: %v", err)
		}
		// The old key no longer authenticates.
		sendSRTP(t, s.phoneA, s.srtpA, 0, make([]byte, 160))
		if _, ok := readRaw(t, s.phoneB, 400*time.Millisecond); ok {
			t.Fatal("the old key still works after the rekey")
		}
	})

	t.Run("ResetCrypto returns a leg to plain RTP", func(t *testing.T) {
		s := newSecureRelay(t)
		s.r.ResetCrypto("b")
		if _, on := s.r.Secure("b"); on {
			t.Fatal("leg b still secure after the reset")
		}
		if _, on := s.r.Secure("a"); !on {
			t.Fatal("leg a lost its keys with leg b's reset")
		}
	})
}

// TestSRTPKeysNeverFormatted fails if a key can be read out of any value that
// ends up in a log line, an error or a trace: Crypto redacts itself under
// every formatting verb and in slog. Mutation: dropping String, GoString or
// LogValue from Crypto fails the matching case.
func TestSRTPKeysNeverFormatted(t *testing.T) {
	c, err := NewCrypto(1, SuiteAES128SHA1_80)
	if err != nil {
		t.Fatal(err)
	}
	line := c.Attribute()
	b64 := line[strings.Index(line, "inline:")+len("inline:"):]
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("negotiated", "crypto", c, "list", []Crypto{c})
	outs := []string{
		fmt.Sprint(c), fmt.Sprintf("%v %+v %#v %s %q", c, c, c, c, c), fmt.Sprint([]Crypto{c}),
		fmt.Sprint(map[string]Crypto{"a": c}), buf.String(),
	}
	_, perr := ParseCrypto("crypto:1 AES_CM_128_HMAC_SHA1_80 inline:" + b64 + "|2^31|1:4")
	if perr != nil {
		outs = append(outs, perr.Error())
	}
	_, perr = ParseCrypto("crypto:1 AES_CM_128_HMAC_SHA1_80 inline:" + b64[:10])
	if perr != nil {
		outs = append(outs, perr.Error())
	}
	for i, o := range outs {
		if strings.Contains(o, b64) || strings.Contains(o, b64[:20]) {
			t.Errorf("output %d contains key material: %q", i, o)
		}
	}
	// And the JSON encoding of a Crypto (an accidental struct dump) has no key.
	j, _ := json.Marshal(c)
	if strings.Contains(string(j), b64[:20]) {
		t.Errorf("JSON of a Crypto contains key material: %s", j)
	}
}

func newTestRelayFor(t *testing.T) *Relay {
	t.Helper()
	r, _, _ := testRelay(t)
	return r
}

// TestSDESNegotiation fails if the SDP layer does not carry RFC 4568 crypto
// faithfully: Hello's keys are written on the right profile, the far end's
// lines are parsed with the supported suites, and a line Hello cannot honour
// is skipped rather than half-used. Mutation: Build writing the far end's
// lines back, or ParseAudioSDP accepting an MKI line, fails.
func TestSDESNegotiation(t *testing.T) {
	k80, _ := NewCrypto(1, SuiteAES128SHA1_80)
	k32, _ := NewCrypto(2, SuiteAES128SHA1_32)
	base := BuildAudioSDP("10.0.0.1", 4000, 0, 101, 8000)
	off, err := ParseAudioSDP(base)
	if err != nil {
		t.Fatal(err)
	}
	if off.OffersCrypto() || off.SRTPOnly {
		t.Fatal("a plain SDP parses as SRTP")
	}

	t.Run("optional is RTP/AVP with crypto lines", func(t *testing.T) {
		body := string(off.WithCrypto(false, k80, k32).Build("10.0.0.2", 5000, "sendrecv"))
		if !strings.Contains(body, "m=audio 5000 RTP/AVP 0 101") {
			t.Fatalf("optional offer must stay RTP/AVP:\n%s", body)
		}
		if strings.Count(body, "a=crypto:") != 2 {
			t.Fatalf("want two crypto lines:\n%s", body)
		}
		back, err := ParseAudioSDP([]byte(body))
		if err != nil || len(back.Crypto) != 2 || back.SRTPOnly {
			t.Fatalf("parse back = %+v, %v", back, err)
		}
		if back.Crypto[0].Suite != SuiteAES128SHA1_80 || back.Crypto[1].Suite != SuiteAES128SHA1_32 || !back.Crypto[0].sameKey(k80) {
			t.Fatal("the crypto lines did not round trip")
		}
	})

	t.Run("required is RTP/SAVP", func(t *testing.T) {
		body := string(off.WithCrypto(true, k80).Build("10.0.0.2", 5000, "sendrecv"))
		if !strings.Contains(body, "m=audio 5000 RTP/SAVP 0 101") {
			t.Fatalf("required offer must be RTP/SAVP:\n%s", body)
		}
		back, err := ParseAudioSDP([]byte(body))
		if err != nil || !back.SRTPOnly || len(back.Crypto) != 1 {
			t.Fatalf("parse back = %+v, %v", back, err)
		}
	})

	t.Run("a far end's crypto is never written back", func(t *testing.T) {
		withCrypto, _ := ParseAudioSDP([]byte(string(off.WithCrypto(true, k80).Build("10.0.0.2", 5000, "sendrecv"))))
		again := string(withCrypto.Build("10.0.0.3", 6000, "sendrecv"))
		if strings.Contains(again, "a=crypto") || strings.Contains(again, "SAVP") {
			t.Fatalf("Build echoed the far end's SRTP:\n%s", again)
		}
		if string(withCrypto.PlainOffer().WithCrypto(false).Build("1.1.1.1", 1, "")) == "" {
			t.Fatal("empty build")
		}
	})

	t.Run("lines Hello cannot honour are skipped", func(t *testing.T) {
		key := k80.Attribute()
		b64 := key[strings.Index(key, "inline:")+7:]
		sdp := "v=0\r\no=- 1 1 IN IP4 10.0.0.9\r\ns=-\r\nc=IN IP4 10.0.0.9\r\nt=0 0\r\nm=audio 4000 RTP/SAVP 0\r\n" +
			"a=crypto:1 F8_128_HMAC_SHA1_80 inline:" + b64 + "\r\n" + // unknown suite
			"a=crypto:2 AES_CM_128_HMAC_SHA1_80 inline:" + b64 + "|2^31|1:4\r\n" + // MKI
			"a=crypto:3 AES_CM_128_HMAC_SHA1_80 inline:" + b64 + " UNENCRYPTED_SRTP\r\n" + // unsupported parameter
			"a=crypto:4 AES_CM_128_HMAC_SHA1_80 inline:AAAA\r\n" + // short key
			"a=crypto:5 AES_CM_128_HMAC_SHA1_80 inline:" + b64 + "|2^31\r\n" // fine, with a lifetime
		got, err := ParseAudioSDP([]byte(sdp))
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Crypto) != 1 || got.Crypto[0].Tag != 5 || got.CryptoSkipped != 4 {
			t.Fatalf("crypto = %d lines (tag %d), skipped %d; want only tag 5 and 4 skipped", len(got.Crypto), got.Crypto[0].Tag, got.CryptoSkipped)
		}
	})

	t.Run("AES_256 is accepted when asked for, never offered", func(t *testing.T) {
		if slices.Contains(OfferedSuites, SuiteAES256SHA1_80) {
			t.Fatal("AES_256 is offered")
		}
		k256, err := NewCrypto(1, SuiteAES256SHA1_80)
		if err != nil {
			t.Fatal(err)
		}
		r := newTestRelayFor(t)
		local, err := r.AcceptCrypto("a", []Crypto{k256}, true)
		if err != nil || local.Suite != SuiteAES256SHA1_80 {
			t.Fatalf("accept 256: %v %v", local.Suite, err)
		}
	})

	t.Run("an offer with several suites is answered with the first supported", func(t *testing.T) {
		r := newTestRelayFor(t)
		unknown := Crypto{Tag: 1, Suite: "F8_128_HMAC_SHA1_80", key: make([]byte, 30)}
		local, err := r.AcceptCrypto("a", []Crypto{unknown, k32, k80}, false)
		if err != nil || local.Tag != 2 || local.Suite != SuiteAES128SHA1_32 {
			t.Fatalf("answer = tag %d suite %s err %v", local.Tag, local.Suite, err)
		}
	})

	t.Run("an answer that does not match the offer leaves the leg plain", func(t *testing.T) {
		r := newTestRelayFor(t)
		if _, err := r.LocalCrypto("b"); err != nil {
			t.Fatal(err)
		}
		stranger, _ := NewCrypto(9, SuiteAES128SHA1_80)
		if r.ApplyAnswerCrypto("b", []Crypto{stranger}, false) {
			t.Fatal("an answer with an unknown tag activated the leg")
		}
		if r.ApplyAnswerCrypto("b", nil, false) {
			t.Fatal("an answer without crypto activated the leg")
		}
		if _, on := r.Secure("b"); on {
			t.Fatal("leg b is secure")
		}
	})

	t.Run("replicated SDP carries no key", func(t *testing.T) {
		body := string(off.WithCrypto(true, k80).Build("10.0.0.2", 5000, "sendrecv"))
		if got := StripCrypto(body); strings.Contains(got, "crypto") || !strings.Contains(got, "m=audio") {
			t.Fatalf("StripCrypto = %q", got)
		}
	})
}

// TestSRTPPacketP99 fails if protecting and unprotecting one packet costs
// more than 50 microseconds at the 99th percentile (S-15's benchmark bound).
// The race detector slows everything down, so the bound is checked only on
// normal builds.
func TestSRTPPacketP99(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing bound is checked on a normal, non-short run")
	}
	send, _ := NewCrypto(1, SuiteAES128SHA1_80)
	recv, _ := NewCrypto(1, SuiteAES128SHA1_80)
	tx, err := NewEndpoint(send, recv)
	if err != nil {
		t.Fatal(err)
	}
	rx, err := NewEndpoint(recv, send)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 160)
	const n = 4000
	d := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		pkt := BuildRTP(0, uint16(i), uint32(i)*160, 7, false, payload)
		start := time.Now()
		enc, err := tx.Protect(pkt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rx.k.unprotect(enc); err != nil {
			t.Fatal(err)
		}
		d = append(d, time.Since(start))
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	if p99 := d[n*99/100]; p99 > 50*time.Microsecond {
		t.Fatalf("protect+unprotect p99 = %v, want at most 50µs", p99)
	}
}

func BenchmarkSRTPProtectUnprotect(b *testing.B) {
	send, _ := NewCrypto(1, SuiteAES128SHA1_80)
	recv, _ := NewCrypto(1, SuiteAES128SHA1_80)
	tx, _ := NewEndpoint(send, recv)
	rx, _ := NewEndpoint(recv, send)
	payload := make([]byte, 160)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		pkt := BuildRTP(0, uint16(i), uint32(i)*160, 7, false, payload)
		enc, err := tx.Protect(pkt)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := rx.k.unprotect(enc); err != nil {
			b.Fatal(err)
		}
	}
}
