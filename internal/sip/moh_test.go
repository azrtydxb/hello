package sip

import (
	"context"
	"encoding/binary"
	"math"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

func holdBody(dir string) string {
	return "v=0\r\no=a 2 2 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4970 RTP/AVP 0 101\r\na=rtpmap:101 telephone-event/8000\r\na=" + dir + "\r\n"
}

// rtpCount reads RTP from conn for d and returns how many audio packets
// arrived and the loudest decoded sample among them.
func rtpCount(conn net.PacketConn, d time.Duration) (n int, peak int) {
	buf := make([]byte, 1500)
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		_ = conn.SetReadDeadline(end)
		k, _, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		pkt, ok := media.ParseRTP(buf[:k])
		if !ok || pkt.PayloadType != 0 {
			continue
		}
		n++
		pcm := media.DecodePCMU(pkt.Payload)
		for i := 0; i+1 < len(pcm); i += 2 {
			v := int(int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8))
			peak = max(peak, int(math.Abs(float64(v))))
		}
	}
	return n, peak
}

// loudWAV is one second of a 1 kHz tone at 8 kHz.
func loudWAV() []byte {
	pcm := make([]byte, 16000)
	for i := range 8000 {
		v := int16(12000 * math.Sin(2*math.Pi*1000*float64(i)/8000))
		pcm[2*i], pcm[2*i+1] = byte(uint16(v)), byte(uint16(v)>>8) //nolint:gosec // test samples
	}
	return media.EncodeWAV(pcm, 8000)
}

// TestMusicOnHold fails if a held party hears silence when music on hold is
// configured (a class playlist, the holder's extension class, or the
// built-in tone when nothing is chosen), if the holder's own audio leaks
// into the music, or if the music continues after the call resumes (spec
// S-33).
func TestMusicOnHold(t *testing.T) {
	cases := []struct {
		name       string
		holderCall bool // true: the caller holds; false: the callee holds
		classes    []snapshot.MOHClass
		exts       []snapshot.Extension
		wantFetch  string
	}{
		{name: "builtin tone when no class is set", holderCall: true},
		{
			name: "default class playlist", holderCall: true, wantFetch: "ann/hold1.wav",
			classes: []snapshot.MOHClass{{ID: 1, Name: "default", IsDefault: true, Names: []string{"hold1"}, Objects: []string{"ann/hold1.wav"}}},
		},
		{
			name: "holder extension class wins over the default", holderCall: false, wantFetch: "ann/hold1.wav",
			classes: []snapshot.MOHClass{
				{ID: 1, Name: "default", IsDefault: true},
				{ID: 2, Name: "ext", Shuffle: true, Names: []string{"hold1"}, Objects: []string{"ann/hold1.wav"}},
			},
			exts: []snapshot.Extension{{Number: "200", MOHClassID: 2}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := &fakeObjects{}
			_ = objs.Put(context.Background(), "ann/hold1.wav", loudWAV())
			feats := func(s *snapshot.Snapshot) {
				s.WithMOHClasses(tc.classes)
				if tc.exts != nil {
					s.WithExtensionData(tc.exts)
				}
			}
			pbx := startPBX(t, callerDevices(), shortRing(2*time.Second), withMedia(false),
				withFeatures(feats), withVoicemail(newVMStore(), objs))
			a := newPhone(t, pbx, "a1", "pa")
			a.register(t)
			b := newPhone(t, pbx, "b1", "pb1")
			b.register(t)
			aRTP, bRTP := listenRTP(t, a), listenRTP(t, b)
			b.setCallee(answerAfter(nil))
			r := waitCall(t, dial(t.Context(), a, "200"))
			if r.err != nil {
				t.Fatalf("call: %v", r.err)
			}
			inv := waitReq(t, b.invites, "INVITE to b")
			waitReq(t, b.acks, "ACK to b")

			held, other := bRTP, aRTP // the caller holds: the callee hears the music
			if !tc.holderCall {
				held, other = aRTP, bRTP
			}
			// A call that is not held carries no music.
			if n, _ := rtpCount(held, 300*time.Millisecond); n != 0 {
				t.Fatalf("%d packets before the hold: the music must wait for it", n)
			}

			var res = func(dir string) int {
				if tc.holderCall {
					return a.reinviteAsCaller(t, r.dcs, holdBody(dir)).StatusCode
				}
				b.mu.Lock()
				dss := b.servers[inv.CallID().Value()]
				b.mu.Unlock()
				up := sip.NewRequest(sip.UPDATE, inv.Contact().Address)
				up.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
				up.SetBody([]byte(b.sdp + "a=" + dir + "\r\n"))
				ures, err := dss.Do(t.Context(), up)
				if err != nil {
					t.Fatalf("UPDATE: %v", err)
				}
				return ures.StatusCode
			}
			if code := res("sendonly"); code != 200 {
				t.Fatalf("hold re-INVITE = %d", code)
			}
			n, peak := rtpCount(held, 1500*time.Millisecond)
			if n < 40 {
				t.Fatalf("held party got %d packets in 1.5s, want a steady music stream", n)
			}
			if peak < 500 {
				t.Fatalf("held party's music is silent (peak %d)", peak)
			}
			if tc.wantFetch != "" && !slices.Contains(objs.snapshotGets(), tc.wantFetch) {
				t.Fatalf("the class announcement %s was never fetched: %v", tc.wantFetch, objs.snapshotGets())
			}
			// The holder hears nothing of it.
			if n, _ := rtpCount(other, 200*time.Millisecond); n != 0 {
				t.Fatalf("the holder heard %d music packets", n)
			}

			if code := res("sendrecv"); code != 200 {
				t.Fatalf("resume re-INVITE = %d", code)
			}
			rtpCount(held, 150*time.Millisecond) // drain what was in flight
			if n, _ := rtpCount(held, 600*time.Millisecond); n != 0 {
				t.Fatalf("music continued after resume: %d packets", n)
			}
			hangup(t, r.dcs)
		})
	}
}

// TestMOHResolution fails if a held party's class is not picked in the
// order extension, ring group, inbound route, global default, built-in
// tone, or if a class ID that no longer exists is not skipped.
func TestMOHResolution(t *testing.T) {
	s := snapshot.New(1, "d", nil).WithMOHClasses([]snapshot.MOHClass{
		{ID: 1, Name: "default", IsDefault: true},
		{ID: 2, Name: "ext"},
		{ID: 3, Name: "group"},
		{ID: 4, Name: "route"},
	})
	cases := []struct {
		ids  []int64
		want int64
	}{
		{[]int64{2, 3, 4}, 2},
		{[]int64{0, 3, 4}, 3},
		{[]int64{0, 0, 4}, 4},
		{[]int64{0, 0, 0}, 1},
		{[]int64{99, 0, 4}, 4},
		{nil, 1},
	}
	for _, tc := range cases {
		c, ok := s.ResolveMOH(tc.ids...)
		if !ok || c.ID != tc.want {
			t.Errorf("ResolveMOH(%v) = %d, %v; want %d", tc.ids, c.ID, ok, tc.want)
		}
	}
	if _, ok := snapshot.New(1, "d", nil).ResolveMOH(5); ok {
		t.Error("a snapshot with no classes resolved one; the built-in tone must play")
	}
}

// TestMOHPlaylist fails if a loop does not cycle its tracks in order, if a
// shuffle repeats a track back to back or skips one in a pass, if a track
// that cannot be loaded stalls the stream instead of being skipped, or if a
// class whose announcements cannot be loaded does not fall back to the
// built-in track (silence is never an option).
func TestMOHPlaylist(t *testing.T) {
	tracks := map[string]media.Track{}
	load := func(skip string) func(string) media.Track {
		return func(obj string) media.Track {
			if obj == skip {
				return nil
			}
			if tracks[obj] == nil {
				tracks[obj] = media.NewPCMTrack([]byte{obj[0], 0}, 8000)
			}
			return tracks[obj]
		}
	}
	id := func(tr media.Track) byte {
		pcm, err := tr.PCMAt(8000)
		if err != nil {
			t.Fatal(err)
		}
		return pcm[0]
	}
	pl := &mohPlaylist{objs: []string{"1", "2", "3"}, load: load(""), last: -1}
	var got []byte
	for range 7 {
		got = append(got, id(pl.next())-'0')
	}
	if string(got) != string([]byte{1, 2, 3, 1, 2, 3, 1}) {
		t.Fatalf("loop order = %v", got)
	}
	sh := &mohPlaylist{objs: []string{"1", "2", "3", "4"}, load: load(""), shuffle: true, last: -1}
	var prev byte
	for pass := range 30 {
		seen := map[byte]bool{}
		for range 4 {
			v := id(sh.next())
			if v == prev {
				t.Fatalf("pass %d: track %d repeated back to back", pass, v)
			}
			prev = v
			seen[v] = true
		}
		if len(seen) != 4 {
			t.Fatalf("pass %d played %v: a shuffle pass must play every track once", pass, seen)
		}
	}
	skipping := &mohPlaylist{objs: []string{"1", "2", "3"}, load: load("2"), last: -1}
	got = got[:0]
	for range 4 {
		got = append(got, id(skipping.next())-'0')
	}
	if string(got) != string([]byte{1, 3, 1, 3}) {
		t.Fatalf("with track 2 unloadable the order = %v, want 1 3 1 3", got)
	}
	srv := &Server{deps: Deps{Objects: &fakeObjects{}}, cfg: Config{StateTimeout: time.Second}, log: discard}
	fb := srv.mohPlaylist(t.Context(), snapshot.MOHClass{Objects: []string{"ann/missing.wav"}}).next()
	if pcm, err := fb.PCMAt(8000); err != nil || len(pcm) < 16000 {
		t.Fatalf("fallback track = %d bytes, %v; want the built-in track", len(pcm), err)
	}
}

// stereoSong is two seconds of a 48 kHz stereo WAV with a 1 kHz tone, the
// shape of a typical uploaded song.
func stereoSong() []byte {
	const rate = 48000
	raw := make([]byte, 4*2*rate)
	for i := range 2 * rate {
		v := int16(8000 * math.Sin(2*math.Pi*1000*float64(i)/rate))
		binary.LittleEndian.PutUint16(raw[4*i:], uint16(v))
		binary.LittleEndian.PutUint16(raw[4*i+2:], uint16(v))
	}
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(raw)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 2)
	binary.LittleEndian.PutUint32(h[24:], rate)
	binary.LittleEndian.PutUint32(h[28:], rate*4)
	binary.LittleEndian.PutUint16(h[32:], 4)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(raw)))
	return append(h, raw...)
}

// TestMOHAudioPerCodecRate fails if hold music made from a 48 kHz stereo
// song reaches a leg at the wrong rate (8 kHz G.711, 16 kHz G.722, 48 kHz
// Opus), if the 8 kHz copy is not what a song stored at 48 kHz gives
// (rather than a track already squashed at upload), or if two held calls
// each decode and resample their own copy instead of sharing one.
func TestMOHAudioPerCodecRate(t *testing.T) {
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/song.wav", stereoSong())
	srv := &Server{deps: Deps{Objects: objs}, cfg: Config{StateTimeout: time.Second}, log: discard}
	tr, ok := srv.mohAudio(t.Context(), "ann/song.wav")
	if !ok {
		t.Fatal("the song did not load")
	}
	for _, rate := range []int{8000, 16000, 48000} {
		pcm, err := tr.PCMAt(rate)
		if err != nil || len(pcm) != 2*2*rate {
			t.Fatalf("%d Hz: %d bytes, %v; want two seconds", rate, len(pcm), err)
		}
	}
	// The song keeps what 8 kHz cannot hold: 1 kHz is in band at every rate.
	hi, _ := tr.PCMAt(48000)
	lo, _ := tr.PCMAt(8000)
	if rmsOf(hi) < 4000 || rmsOf(lo) < 4000 {
		t.Fatalf("rms %.0f at 48k / %.0f at 8k, the tone was lost", rmsOf(hi), rmsOf(lo))
	}
	again, ok := srv.mohAudio(t.Context(), "ann/song.wav")
	if !ok || again != tr {
		t.Fatal("a second hold decoded the song again instead of sharing the cached track")
	}
	a, _ := tr.PCMAt(16000)
	b, _ := again.PCMAt(16000)
	if &a[0] != &b[0] {
		t.Fatal("the 16 kHz copy was resampled twice")
	}
}

func rmsOf(pcm []byte) float64 {
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(pcm)/2))
}

// TestMOHCacheBudget fails if the decoded-announcement cache grows without
// bound: entries past their lifetime and, beyond the byte budget, the oldest
// entries must go, while the entry just added stays.
func TestMOHCacheBudget(t *testing.T) {
	srv := &Server{mohCache: map[string]mohAudioEntry{"old": {size: 1, at: time.Now().Add(-time.Hour)}}}
	now := time.Now()
	srv.mohMu.Lock()
	for i, name := range []string{"a", "b", "c"} {
		srv.cacheMOH(name, mohAudioEntry{size: mohCacheBudget / 2, at: now.Add(time.Duration(i) * time.Second)})
	}
	got := len(srv.mohCache)
	_, hasC := srv.mohCache["c"]
	_, hasA := srv.mohCache["a"]
	srv.mohMu.Unlock()
	if hasA || !hasC || got != 2 {
		t.Fatalf("cache holds %d entries (a=%v c=%v), want b and c only", got, hasA, hasC)
	}
}

// TestWaitingMusic fails if a caller to a ring group with waiting music
// enabled hears no music while the group rings, if a group without the
// setting sends any early media, or if the music keeps playing after the
// member answers (spec S-33, waiting music instead of ringback).
func TestWaitingMusic(t *testing.T) {
	for _, on := range []bool{true, false} {
		name := map[bool]string{true: "enabled", false: "disabled"}[on]
		t.Run(name, func(t *testing.T) {
			group := []snapshot.RingGroup{{ID: 1, Name: "700", Strategy: "ring-all", RingTimeout: 20, MemberDelay: 1, MOHRingback: on, MOHClassID: 1}}
			objs := &fakeObjects{}
			_ = objs.Put(context.Background(), "ann/hold1.wav", loudWAV())
			feats := func(s *snapshot.Snapshot) {
				s.WithRingGroups(group, [][]snapshot.RingGroupMember{{{ExtensionID: 2, Extension: "200", Position: 1}}})
				s.WithMOHClasses([]snapshot.MOHClass{{ID: 1, Name: "wait", Names: []string{"hold1"}, Objects: []string{"ann/hold1.wav"}}})
			}
			pbx := startPBX(t, callerDevices(), shortRing(10*time.Second), withMedia(false),
				withFeatures(feats), withVoicemail(newVMStore(), objs))
			a := newPhone(t, pbx, "a1", "pa")
			a.register(t)
			b := newPhone(t, pbx, "b1", "pb1")
			b.register(t)
			aRTP, bRTP := listenRTP(t, a), listenRTP(t, b)
			ring := make(chan struct{})
			b.setCallee(answerAfter(ring))
			res := dial(t.Context(), a, "700")
			waitReq(t, b.invites, "INVITE to b")
			n, peak := rtpCount(aRTP, 1200*time.Millisecond)
			if on && (n < 30 || peak < 500) {
				t.Fatalf("caller heard %d packets (peak %d) while the group rang, want music", n, peak)
			}
			if !on && n != 0 {
				t.Fatalf("caller heard %d packets with waiting music off", n)
			}
			close(ring)
			r := waitCall(t, res)
			if r.err != nil {
				t.Fatalf("call: %v", r.err)
			}
			waitReq(t, b.acks, "ACK to b")
			rtpCount(aRTP, 200*time.Millisecond)
			rtpCount(bRTP, 200*time.Millisecond)
			if n, _ := rtpCount(aRTP, 500*time.Millisecond); n != 0 {
				t.Fatalf("music continued after the member answered: %d packets", n)
			}
			hangup(t, r.dcs)
		})
	}
}
