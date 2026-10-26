package media

import (
	"math"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// confPhone is a test endpoint: a UDP socket that sends PCMU to its party
// and collects what the room sends back.
type confPhone struct {
	t      *testing.T
	conn   *net.UDPConn
	party  *Party
	dst    *net.UDPAddr
	mu     sync.Mutex
	seq    uint16
	ts     uint32
	frames chan []int16
}

func newConfPhone(t *testing.T) *confPhone {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	port := conn.LocalAddr().(*net.UDPAddr).Port
	offer := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio " + strconv.Itoa(port) +
		" RTP/AVP 0 101\r\na=rtpmap:101 telephone-event/8000\r\n"
	ans, party, err := NewParty("127.0.0.1", 0, 0, []byte(offer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(party.Close)
	sdp, err := ParseAudioSDP(ans)
	if err != nil {
		t.Fatal(err)
	}
	p := &confPhone{t: t, conn: conn, party: party, dst: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: sdp.Port}, frames: make(chan []int16, 512)}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			pkt, ok := ParseRTP(buf[:n])
			if !ok || pkt.PayloadType != 0 {
				continue
			}
			select {
			case p.frames <- PCMToSamples(DecodePCMU(pkt.Payload)):
			default:
			}
		}
	}()
	return p
}

// talk sends frames of a sine for d, in real time.
func (p *confPhone) talk(hz, amp float64, d time.Duration) {
	tone := sine(MixFrame*50, hz, amp)
	end := time.Now().Add(d)
	for i := 0; time.Now().Before(end); i++ {
		off := (i % 50) * MixFrame
		p.mu.Lock()
		pkt := BuildRTP(0, p.seq, p.ts, 77, i == 0, EncodePCMU(SamplesToPCM(tone[off:off+MixFrame])))
		p.seq++
		p.ts += MixFrame
		p.mu.Unlock()
		_, _ = p.conn.WriteToUDP(pkt, p.dst)
		time.Sleep(frameDur)
	}
}

// heard reports the loudest sample received in the last d.
func (p *confPhone) heard(d time.Duration) int {
	deadline := time.After(d)
	best := 0
	for {
		select {
		case f := <-p.frames:
			best = max(best, peak(f))
		case <-deadline:
			return best
		}
	}
}

func (p *confPhone) drain() {
	for {
		select {
		case <-p.frames:
		default:
			return
		}
	}
}

// TestRoomMixesParties fails if participants cannot hear each other, if a
// talker hears itself, if a muted party is heard, or if a third party does
// not hear two talkers at once.
func TestRoomMixesParties(t *testing.T) {
	r := NewRoom()
	defer r.Close()
	a, b, c := newConfPhone(t), newConfPhone(t), newConfPhone(t)
	r.Add("a", a.party, false, false)
	r.Add("b", b.party, false, false)
	r.Add("c", c.party, false, false)
	if r.Len() != 3 {
		t.Fatalf("room holds %d parties", r.Len())
	}

	go a.talk(500, 8000, 700*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	b.drain()
	c.drain()
	a.drain()
	hb, hc, ha := make(chan int, 1), make(chan int, 1), make(chan int, 1)
	go func() { hb <- b.heard(400 * time.Millisecond) }()
	go func() { hc <- c.heard(400 * time.Millisecond) }()
	go func() { ha <- a.heard(400 * time.Millisecond) }()
	if got := <-hb; got < 4000 {
		t.Fatalf("b heard peak %d: participants cannot hear each other", got)
	}
	if got := <-hc; got < 4000 {
		t.Fatalf("c heard peak %d", got)
	}
	if got := <-ha; got > 500 {
		t.Fatalf("a heard its own voice (peak %d)", got)
	}
	time.Sleep(300 * time.Millisecond)

	// Muted: b talks, nobody hears it.
	r.SetMuted("b", true)
	go b.talk(700, 8000, 600*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	a.drain()
	c.drain()
	if got := a.heard(300 * time.Millisecond); got > 500 {
		t.Fatalf("a heard a muted party (peak %d)", got)
	}
	time.Sleep(300 * time.Millisecond)

	// Unmuted again.
	r.SetMuted("b", false)
	go b.talk(700, 8000, 600*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	a.drain()
	if got := a.heard(300 * time.Millisecond); got < 4000 {
		t.Fatalf("a did not hear the unmuted party (peak %d)", got)
	}
}

// TestRoomWaitingAndSounds fails if an isolated (waiting) party hears the
// room or is heard by it, if it does not hear its hold source, if a room
// announcement misses an admitted party or reaches a waiting one, or if
// Admit does not bring the waiting party in.
func TestRoomWaitingAndSounds(t *testing.T) {
	r := NewRoom()
	defer r.Close()
	a, w := newConfPhone(t), newConfPhone(t)
	r.Add("a", a.party, false, false)
	r.Add("w", w.party, false, true)
	r.SetHold("w", sine(MixRate, 300, 3000))

	go a.talk(500, 8000, 500*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	w.drain()
	got := w.heard(300 * time.Millisecond)
	if got < 2000 || got > 3500 {
		t.Fatalf("waiting party heard peak %d, want only its hold tone (about 3000)", got)
	}
	time.Sleep(200 * time.Millisecond)

	go w.talk(900, 8000, 500*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	a.drain()
	if got := a.heard(300 * time.Millisecond); got > 500 {
		t.Fatalf("the room heard a waiting party (peak %d)", got)
	}
	time.Sleep(200 * time.Millisecond)

	r.SayAll(ConfToneSamples(ToneJoin))
	a.drain()
	w.drain()
	if got := a.heard(400 * time.Millisecond); got < 3000 {
		t.Fatalf("the admitted party missed the room cue (peak %d)", got)
	}

	r.Admit("w")
	r.SetHold("w", nil)
	go w.talk(900, 8000, 500*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	a.drain()
	if got := a.heard(300 * time.Millisecond); got < 4000 {
		t.Fatalf("the admitted party is not heard (peak %d)", got)
	}
}

// TestRoomTapAndClose fails if the recording tap does not see the full mix,
// or if Close leaves the clock running or breaks a restart.
func TestRoomTapAndClose(t *testing.T) {
	r := NewRoom()
	a, b := newConfPhone(t), newConfPhone(t)
	rec := NewConfRecorder(0)
	r.SetTap(rec.Append)
	r.Add("a", a.party, false, false)
	r.Add("b", b.party, false, false)
	a.talk(500, 8000, 400*time.Millisecond)
	r.Close()
	wav, ms, err := rec.Result()
	if err != nil || ms < 200 {
		t.Fatalf("recording = %d ms, %v", ms, err)
	}
	pcm, _, _ := DecodeWAV(wav)
	if peak(PCMToSamples(pcm)) < 4000 {
		t.Fatal("the recording does not hold the talker")
	}
	ticks, _, _ := r.Stats()
	time.Sleep(100 * time.Millisecond)
	if t2, _, _ := r.Stats(); t2 != ticks {
		t.Fatal("the clock kept running after Close")
	}
	r.Close() // twice is fine
}

// TestPartyDTMF fails if an RFC 2833 digit, in the event-code form real
// phones send or the ASCII form the rest of Hello's tests use, does not
// reach the party's DTMF channel once.
func TestPartyDTMF(t *testing.T) {
	p := newConfPhone(t)
	send := func(code byte, marker bool, end bool, seq uint16) {
		flags := byte(10)
		if end {
			flags |= 0x80
		}
		pkt := BuildRTP(101, seq, 1000, 77, marker, []byte{code, flags, 0x00, 0xa0})
		_, _ = p.conn.WriteToUDP(pkt, p.dst)
	}
	send(11, true, false, 1) // '#' as event 11
	send(11, false, false, 2)
	send(11, false, true, 3)
	send('5', true, false, 4) // ASCII
	send('5', false, true, 5)
	want := []byte{'#', '5'}
	for _, w := range want {
		select {
		case got := <-p.party.DTMF():
			if got != w {
				t.Fatalf("digit = %q, want %q", got, w)
			}
		case <-time.After(time.Second):
			t.Fatalf("digit %q never arrived", w)
		}
	}
	select {
	case extra := <-p.party.DTMF():
		t.Fatalf("a duplicate digit %q was delivered", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

// g722Phone is a wideband test endpoint: it sends and receives G.722.
type g722Phone struct {
	conn   *net.UDPConn
	party  *Party
	dst    *net.UDPAddr
	enc    *G722Encoder
	dec    *G722Decoder
	seq    uint16
	ts     uint32
	frames chan []int16
}

func newG722Phone(t *testing.T) *g722Phone {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	port := conn.LocalAddr().(*net.UDPAddr).Port
	// G.722 first, then G.711: the room must pick the wideband codec.
	offer := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio " + strconv.Itoa(port) +
		" RTP/AVP 9 0 101\r\na=rtpmap:101 telephone-event/8000\r\n"
	ans, party, err := NewParty("127.0.0.1", 0, 0, []byte(offer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(party.Close)
	sdp, err := ParseAudioSDP(ans)
	if err != nil || sdp.PayloadType != 9 {
		t.Fatalf("answer = %q (%v), want G.722", ans, err)
	}
	p := &g722Phone{conn: conn, party: party, dst: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: sdp.Port},
		enc: NewG722Encoder(), dec: NewG722Decoder(), frames: make(chan []int16, 512)}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			pkt, ok := ParseRTP(buf[:n])
			if !ok || pkt.PayloadType != 9 {
				continue
			}
			select {
			case p.frames <- p.dec.Decode(pkt.Payload):
			default:
			}
		}
	}()
	return p
}

func (p *g722Phone) talk(hz, amp float64, d time.Duration) {
	end := time.Now().Add(d)
	for i := 0; time.Now().Before(end); i++ {
		wide := make([]int16, 320)
		for j := range wide {
			wide[j] = int16(amp * math.Sin(2*math.Pi*hz*float64(i*320+j)/16000))
		}
		pkt := BuildRTP(9, p.seq, p.ts, 99, i == 0, p.enc.Encode(wide))
		p.seq++
		p.ts += 160
		_, _ = p.conn.WriteToUDP(pkt, p.dst)
		time.Sleep(frameDur)
	}
}

func (p *g722Phone) heard(d time.Duration) int {
	deadline := time.After(d)
	best := 0
	for {
		select {
		case f := <-p.frames:
			best = max(best, peak(f))
		case <-deadline:
			return best
		}
	}
}

// TestRoomWidebandAndNarrowband fails if a G.722 participant is not answered
// with G.722, if a G.711 participant does not hear a G.722 one (through the
// 16 kHz to 8 kHz conversion), or if the G.722 participant does not hear the
// G.711 one (through the conversion back up).
func TestRoomWidebandAndNarrowband(t *testing.T) {
	r := NewRoom()
	defer r.Close()
	narrow, wide := newConfPhone(t), newG722Phone(t)
	r.Add("n", narrow.party, false, false)
	r.Add("w", wide.party, false, false)

	go wide.talk(500, 8000, 800*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	narrow.drain()
	if got := narrow.heard(400 * time.Millisecond); got < 3000 {
		t.Fatalf("the G.711 participant heard peak %d from the G.722 one", got)
	}
	time.Sleep(300 * time.Millisecond)
	go narrow.talk(500, 8000, 800*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	for len(wide.frames) > 0 {
		<-wide.frames
	}
	if got := wide.heard(400 * time.Millisecond); got < 3000 {
		t.Fatalf("the G.722 participant heard peak %d from the G.711 one", got)
	}
}
