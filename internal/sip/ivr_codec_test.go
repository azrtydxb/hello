package sip

import (
	"context"
	"encoding/binary"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// TestIVRVoicemailRecordsWidebandCaller: after the IVR, voicemail runs on the
// relay's caller leg. A G.722 caller gets a 16 kHz session whose recording
// decodes the caller's G.722 packets; a packet in another payload type is
// ignored rather than decoded as G.711. It fails if the session keeps the
// old fixed 8 kHz / G.711-only behaviour.
func TestIVRVoicemailRecordsWidebandCaller(t *testing.T) {
	relay, err := media.NewRelay(40000, 40100)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if _, err := relay.AddLeg(legCaller); err != nil {
		t.Fatal(err)
	}
	g722, _ := media.StaticCodec(9)
	relay.SetCodecs(legCaller, []media.Codec{g722}, 101)

	c := &call{ivr: newIVRState()}
	sess := &relaySession{c: c, relay: relay}
	if got := sess.SampleRate(); got != 16000 {
		t.Fatalf("session rate = %d, want 16000 for a G.722 caller", got)
	}

	// 20 ms of a 1 kHz tone at 16 kHz, encoded as the caller would send it.
	tone := make([]byte, 2*320)
	for i := range 320 {
		v := int16(8000 * math.Sin(2*math.Pi*1000*float64(i)/16000))
		binary.LittleEndian.PutUint16(tone[2*i:], uint16(v))
	}
	enc, err := media.NewEncoder(g722)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	payload := enc.Encode(tone)

	done := make(chan []byte, 1)
	go func() {
		pcm, _ := sess.Record(context.Background(), 300*time.Millisecond, make(chan byte, 4))
		done <- pcm
	}()
	var tap func(string, *media.RTPPacket)
	for range 100 {
		c.mu.Lock()
		tap = c.vmTap
		c.mu.Unlock()
		if tap != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if tap == nil {
		t.Fatal("recording installed no tap")
	}
	for range 5 {
		tap(legCaller, &media.RTPPacket{PayloadType: 9, Payload: payload})
	}
	// A µ-law packet is not the negotiated codec: decoding it as G.711
	// would add 320 bytes of noise.
	tap(legCaller, &media.RTPPacket{PayloadType: 0, Payload: make([]byte, 160)})
	tap(legCallee, &media.RTPPacket{PayloadType: 9, Payload: payload})

	pcm := <-done
	// At least the five frames, at 16 kHz (640 bytes each).
	if len(pcm) < 5*640 {
		t.Fatalf("recorded %d bytes, want at least %d", len(pcm), 5*640)
	}
	var energy float64
	for i := 0; i+1 < 5*640; i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		energy += v * v
	}
	if energy == 0 {
		t.Fatal("the G.722 frames decoded to silence")
	}
	// Padded to the elapsed time at 16 kHz: about 0.3 s, 9600 bytes.
	if len(pcm) < 9000 {
		t.Fatalf("recording is %d bytes; a 0.3 s recording at 16 kHz is about 9600", len(pcm))
	}
}

// TestIVRPromptKeepsWAVRate: a 16 kHz prompt is handed to the relay with its
// own rate (the relay resamples to the caller's codec). It fails if the
// prompt loader drops the WAV's rate, which played a 16 kHz prompt at half
// speed through the 8 kHz-only PlayTo.
func TestIVRPromptKeepsWAVRate(t *testing.T) {
	pcm := make([]byte, 2*1600) // 0.1 s at 16 kHz
	objs := &fakeObjects{objs: map[string][]byte{"ann/wide.wav": media.EncodeWAV(pcm, 16000)}}
	s := &Server{cfg: Config{StateTimeout: time.Second}, deps: Deps{Objects: objs}, log: slog.Default()}
	c := &call{s: s, ivr: newIVRState()}
	snap := snapshot.New(1, "example.com", nil).WithAnnouncements(map[string]string{"wide": "ann/wide.wav"})

	a := c.ivrPrompt(context.Background(), snap, "wide")
	if a.rate != 16000 || len(a.pcm) != len(pcm) {
		t.Fatalf("prompt = %d bytes at %d Hz, want %d bytes at 16000 Hz", len(a.pcm), a.rate, len(pcm))
	}
	// Served from the call's cache the second time, rate included.
	if b := c.ivrPrompt(context.Background(), snap, "wide"); b.rate != 16000 || len(objs.snapshotGets()) != 1 {
		t.Fatalf("cached prompt = %d Hz after %d fetches", b.rate, len(objs.snapshotGets()))
	}
}
