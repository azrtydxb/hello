package media

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testAnchors pairs two anchors on 127.0.0.1 the way the B2BUA and a
// software phone meet: B answers a bootstrap offer first (so its socket
// exists), then A answers B's answer, which points A at B.
func testAnchors(t *testing.T) (a, b Session) {
	t.Helper()
	anchorA := NewAnchor("127.0.0.1", testLogger(t))
	anchorB := NewAnchor("127.0.0.1", testLogger(t))
	boot := BuildAudioSDP("127.0.0.1", 9, 0, 101, sampleRateHz)
	answerB, sB, err := anchorB.Answer(boot)
	if err != nil {
		t.Fatalf("B answer: %v", err)
	}
	t.Cleanup(func() { _ = sB.Close() })
	_, sA, err := anchorA.Answer(answerB)
	if err != nil {
		t.Fatalf("A answer: %v", err)
	}
	t.Cleanup(func() { _ = sA.Close() })
	return sA, sB
}

// tonePCM is 20 frames (400 ms) of a mid-frequency sine, loud enough that
// a recording of it is clearly not silence.
func tonePCM() []byte {
	return sineSweepPCM([]float64{440}, 20*frameSamples)
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func TestSessionLoopback(t *testing.T) {
	a, b := testAnchors(t)
	pcm := tonePCM()
	start := time.Now()
	// Absolute deadline from the onset, so a slow machine fails the test
	// instead of passing by accident.
	deadline := start.Add(5 * time.Second)
	done := make(chan []byte, 1)
	errCh := make(chan error, 1)
	dtmf := make(chan byte, 8)
	go func() {
		data, err := b.Record(context.Background(), 600*time.Millisecond, dtmf)
		done <- data
		errCh <- err
	}()
	if err := a.Play(context.Background(), pcm); err != nil {
		t.Fatalf("play: %v", err)
	}
	var rec []byte
	select {
	case rec = <-done:
	case <-time.After(time.Until(deadline)):
		t.Fatalf("Record did not return within %v", deadline.Sub(start))
	}
	if err := <-errCh; err != nil {
		t.Fatalf("record: %v", err)
	}
	// 600 ms is 30 frames; allow one frame of padding either way for
	// tick alignment.
	want := 30 * frameBytes
	if len(rec) < want-frameBytes || len(rec) > want+frameBytes {
		t.Errorf("recording is %d bytes, want about %d", len(rec), want)
	}
	if allZero(rec) {
		t.Error("recording is pure silence; the played audio never arrived")
	}
}

// sdpAudioPort pulls the m=audio port out of an answer the anchor built,
// so a test can send datagrams at the session's socket.
func sdpAudioPort(t *testing.T, answer []byte) int {
	t.Helper()
	for _, line := range bytes.Split(answer, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if rest, ok := bytes.CutPrefix(line, []byte("m=audio ")); ok {
			fields := bytes.Fields(rest)
			if len(fields) < 1 {
				t.Fatalf("malformed m= line %q", line)
			}
			port := 0
			for _, d := range fields[0] {
				port = port*10 + int(d-'0')
			}
			return port
		}
	}
	t.Fatal("answer has no m=audio line")
	return 0
}

// answeredSession answers a test-owned caller socket, returning the
// session, the port of its RTP socket and the negotiated DTMF payload
// type. Datagrams the test sends from caller reach the session; anything
// the session plays lands back on caller.
func answeredSession(t *testing.T) (s Session, caller *net.UDPConn, answer []byte, dtmfPT uint8) {
	t.Helper()
	caller, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("bind caller: %v", err)
	}
	t.Cleanup(func() { _ = caller.Close() })
	port := caller.LocalAddr().(*net.UDPAddr).Port
	offer := BuildAudioSDP("127.0.0.1", port, 0, 101, sampleRateHz)
	ans, s, err := NewAnchor("127.0.0.1", testLogger(t)).Answer(offer)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, caller, ans, 101
}

// sendEventTo is one RFC 2833 event datagram from the test's caller socket
// to the session's socket.
func sendEventTo(t *testing.T, caller *net.UDPConn, port int, pt uint8, seq *uint16, ts *uint32, code byte, marker, end bool) {
	t.Helper()
	payload := []byte{code, 0x00, 0x00, 0x40}
	if end {
		payload[1] |= 0x80
	}
	*seq++
	*ts += frameSamples
	wire := BuildRTP(pt, *seq, *ts, 0x01020304, marker, payload)
	if _, err := caller.WriteToUDP(wire, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}); err != nil {
		t.Fatalf("send event: %v", err)
	}
}

// TestSessionDTMFEndToEnd pushes an RFC 2833 event stream through the UDP
// socket of a live session: '1' then '#', with Record returning on '#'.
func TestSessionDTMFEndToEnd(t *testing.T) {
	s, caller, answer, dtmfPT := answeredSession(t)
	port := sdpAudioPort(t, answer)
	seq, ts := uint16(0), uint32(0)
	start := time.Now()
	deadline := start.Add(5 * time.Second)
	dtmf := make(chan byte, 8)
	done := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		data, err := s.Record(context.Background(), 10*time.Second, dtmf)
		done <- data
		errCh <- err
	}()
	// Give Record a moment to start, then press '1' and '#'.
	sendEventTo(t, caller, port, uint8(dtmfPT), &seq, &ts, '1', true, false)
	sendEventTo(t, caller, port, uint8(dtmfPT), &seq, &ts, '1', false, true)
	sendEventTo(t, caller, port, uint8(dtmfPT), &seq, &ts, '#', true, false)
	sendEventTo(t, caller, port, uint8(dtmfPT), &seq, &ts, '#', false, true)
	var rec []byte
	select {
	case rec = <-done:
	case <-time.After(time.Until(deadline)):
		t.Fatalf("Record did not return on '#' within %v", deadline.Sub(start))
	}
	if err := <-errCh; err != nil {
		t.Fatalf("record: %v", err)
	}
	_ = rec
	// Every press, including the '#', must have reached the channel.
	close(dtmf)
	var got []byte
	for d := range dtmf {
		got = append(got, d)
	}
	if string(got) != "1#" {
		t.Errorf("dtmf channel saw %q, want %q", got, "1#")
	}
}

func TestRecordDeliversEveryDTMF(t *testing.T) {
	s, caller, answer, dtmfPT := answeredSession(t)
	port := sdpAudioPort(t, answer)
	seq, ts := uint16(0), uint32(0)
	dtmf := make(chan byte, 8)
	done := make(chan error, 1)
	go func() {
		_, err := s.Record(context.Background(), 10*time.Second, dtmf)
		done <- err
	}()
	start := time.Now()
	deadline := start.Add(5 * time.Second)
	// Two digits then '#': the channel must see all three.
	for _, code := range []byte{'2', '5', '#'} {
		sendEventTo(t, caller, port, uint8(dtmfPT), &seq, &ts, code, true, false)
		sendEventTo(t, caller, port, uint8(dtmfPT), &seq, &ts, code, false, true)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("record: %v", err)
		}
	case <-time.After(time.Until(deadline)):
		t.Fatalf("Record did not return on '#' within %v", deadline.Sub(start))
	}
	close(dtmf)
	var got []byte
	for d := range dtmf {
		got = append(got, d)
	}
	if string(got) != "25#" {
		t.Errorf("dtmf channel saw %q, want %q", got, "25#")
	}
}

func TestRecordStopsOnMaxDur(t *testing.T) {
	// No media is sent at all: every tick is silence, which the guarantee
	// says must come back as PCM of zeros.
	s, _, _, _ := answeredSession(t)
	start := time.Now()
	dtmf := make(chan byte, 8)
	rec, err := s.Record(context.Background(), 300*time.Millisecond, dtmf)
	if time.Since(start) > 3*time.Second {
		t.Errorf("Record took %v, far past maxDur", time.Since(start))
	}
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if want := 15 * frameBytes; len(rec) != want {
		t.Errorf("recording is %d bytes, want %d (15 frames of 300 ms)", len(rec), want)
	}
	if !allZero(rec) {
		t.Error("silence must record as PCM of zeros")
	}
	// A zero maxDur records nothing and must not wedge.
	if rec, err := s.Record(context.Background(), 0, dtmf); err != nil || len(rec) != 0 {
		t.Errorf("maxDur 0: %d bytes, err %v", len(rec), err)
	}
}

func TestPlayStopsOnContextCancel(t *testing.T) {
	s, _, _, _ := answeredSession(t)
	// Two seconds of audio; the context fires after 50 ms, so Play must
	// return well before the audio would have run out.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := s.Play(ctx, make([]byte, 100*frameBytes))
	if err == nil {
		t.Fatal("Play returned nil after ctx cancellation, want an error")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("Play ran %v after cancellation", time.Since(start))
	}
}

func TestAnchorAnswerRejectsPortZero(t *testing.T) {
	offer := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"t=0 0\r\n" +
		"m=audio 0 RTP/AVP 0\r\n")
	_, _, err := NewAnchor("127.0.0.1", testLogger(t)).Answer(offer)
	if err == nil {
		t.Fatal("Answer accepted an offer that refused audio (port 0)")
	}
}

func TestAnchorCloseTwiceSafe(t *testing.T) {
	anchor := NewAnchor("127.0.0.1", testLogger(t))
	caller, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("bind caller: %v", err)
	}
	defer func() { _ = caller.Close() }()
	offer := BuildAudioSDP("127.0.0.1", caller.LocalAddr().(*net.UDPAddr).Port, 0, 0, sampleRateHz)
	_, s, err := anchor.Answer(offer)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// The second close runs the same sync.Once path; it must not panic or
	// report an error, and Play and Record must now report the closed
	// session instead of hanging.
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := s.Play(context.Background(), []byte{0, 0}); err == nil {
		t.Error("Play after Close returned nil, want an error")
	}
	if _, err := s.Record(context.Background(), 100*time.Millisecond, nil); err == nil {
		t.Error("Record after Close returned nil error, want one")
	}
}

func TestRecordReturnsOnContextCancel(t *testing.T) {
	s, _, _, _ := answeredSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var rec []byte
	var err error
	go func() {
		rec, err = s.Record(ctx, 10*time.Second, nil)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond) // a few ticks of silence first
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record ignored ctx cancellation")
	}
	if err == nil {
		t.Fatal("Record returned nil error on cancellation, want ctx.Err()")
	}
	// Whatever was recorded before the cancel comes back, never nothing.
	if want := 5 * frameBytes; len(rec) < want-frameBytes || len(rec) > want+frameBytes {
		t.Errorf("recording is %d bytes after ~100 ms, want about %d", len(rec), want)
	}
}

// The Answer comment pins symmetric RTP: the offer's address is only a
// bootstrap; the first inbound packet locks the peer. This test offers a
// decoy socket, sends DTMF from the real one, and checks Play follows the
// lock.
func TestPlayFollowsLockedPeer(t *testing.T) {
	decoy, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("bind decoy: %v", err)
	}
	defer func() { _ = decoy.Close() }()
	decoyPort := decoy.LocalAddr().(*net.UDPAddr).Port
	real, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("bind real: %v", err)
	}
	defer func() { _ = real.Close() }()
	// The offer points at the decoy; the session must not trust it once
	// real packets arrive.
	offer := BuildAudioSDP("127.0.0.1", decoyPort, 0, 101, sampleRateHz)
	answer, s, err := NewAnchor("127.0.0.1", testLogger(t)).Answer(offer)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	port := sdpAudioPort(t, answer)
	seq, ts := uint16(0), uint32(0)
	sendEventTo(t, real, port, 101, &seq, &ts, '1', true, false)
	// A short beep, long enough for the session to send frames while the
	// lock is in place.
	go func() {
		_ = s.Play(context.Background(), BeepPCM())
	}()
	// The decoy must stay silent; the real socket must receive RTP.
	got := make([]byte, rtpMaxPacket)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = real.SetReadDeadline(deadline)
		n, _, err := real.ReadFromUDP(got)
		if err == nil && n >= rtpHeaderLen && got[0]>>6 == 2 {
			return // RTP arrived on the locked peer, not the decoy
		}
	}
	t.Error("no RTP arrived on the real peer socket; the session kept sending to the offer's address")
}

// TestAnchorPortNotShadowed fails if an anchor session's port is one that
// another socket already holds on a specific address: datagrams to that
// address would reach the other socket and never the session (on macOS a
// wildcard bind to port 0 can be handed such a port; this flaked the
// in-process voicemail takeover test, whose phones bind 127.0.0.1).
func TestAnchorPortNotShadowed(t *testing.T) {
	held := map[int]bool{}
	for range 1000 {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		held[c.LocalAddr().(*net.UDPAddr).Port] = true
	}
	a := NewAnchor("127.0.0.1", nil)
	offer := BuildAudioSDP("127.0.0.1", 40000, 0, 101, 8000)
	for range 300 {
		ans, sess, err := a.Answer(offer)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		got, err := ParseAudioSDP(ans)
		if err != nil {
			t.Fatal(err)
		}
		if held[got.Port] {
			t.Fatalf("anchor port %d is already held on 127.0.0.1: its RTP would go elsewhere", got.Port)
		}
	}
}
