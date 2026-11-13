package sip

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// fakeVideoRows is the video stream rows.
type fakeVideoRows struct {
	mu   sync.Mutex
	rows map[int64]media.VideoStreamInfo
	next int64
	used int64
}

func (f *fakeVideoRows) OpenVideoStream(_ context.Context, corr string, in media.VideoStreamInfo) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows = map[int64]media.VideoStreamInfo{}
	}
	f.next++
	f.rows[f.next] = in
	return f.next, nil
}

func (f *fakeVideoRows) UpdateVideoStream(_ context.Context, id int64, in media.VideoStreamInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[id] = in
	return nil
}

func (f *fakeVideoRows) VideoUsageBytes(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.used, nil
}

func (f *fakeVideoRows) streams() []media.VideoStreamInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []media.VideoStreamInfo
	for _, r := range f.rows {
		out = append(out, r)
	}
	return out
}

// spsFor is a Constrained Baseline SPS for a picture of w x h.
func spsFor(w, h int) []byte {
	var b []byte
	n := 0
	bit := func(v uint) {
		if n%8 == 0 {
			b = append(b, 0)
		}
		b[len(b)-1] |= byte(v&1) << (7 - uint(n%8))
		n++
	}
	bits := func(v uint, k int) {
		for i := k - 1; i >= 0; i-- {
			bit(v >> uint(i))
		}
	}
	ue := func(v uint) {
		v++
		l := 0
		for x := v; x > 1; x >>= 1 {
			l++
		}
		bits(0, l)
		bits(v, l+1)
	}
	bits(66, 8)
	bits(0xc0, 8)
	bits(30, 8)
	ue(0)
	ue(0)
	ue(2)
	ue(1)
	bit(0)
	ue(uint(w/16 - 1))
	ue(uint(h/16 - 1))
	bit(1)
	bit(1)
	bit(0)
	bit(0)
	bit(1)
	return append([]byte{0x67}, b...)
}

// h264Pictures builds n pictures as RTP, a keyframe every ten: SPS and PPS in a
// STAP-A and an IDR for a keyframe, one P slice for the rest. Sequence numbers
// run on from one picture to the next.
func h264Pictures(n int, ssrc uint32) [][][]byte {
	var out [][][]byte
	seq := uint16(1)
	for i := 0; i < n; i++ {
		ts := uint32(90000 + i*3000)
		if i%10 != 0 {
			out = append(out, [][]byte{media.BuildRTP(96, seq, ts, ssrc, true, append([]byte{0x41}, bytes.Repeat([]byte{byte(i)}, 300)...))})
			seq++
			continue
		}
		sps, pps := spsFor(640, 480), []byte{0x68, 0xce, 0x3c, 0x80}
		stap := []byte{24, byte(len(sps) >> 8), byte(len(sps))}
		stap = append(stap, sps...)
		stap = append(stap, 0, byte(len(pps)))
		stap = append(stap, pps...)
		out = append(out, [][]byte{
			media.BuildRTP(96, seq, ts, ssrc, false, stap),
			media.BuildRTP(96, seq+1, ts, ssrc, true, append([]byte{0x65}, bytes.Repeat([]byte{0x77}, 500)...)),
		})
		seq += 2
	}
	return out
}

func videoRecordingPBX(t *testing.T, rows *fakeVideoRows, objs *fakeObjects, ext snapshot.Extension, opts ...pbxOpt) *testPBX {
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{ext})
		s.WithAnnouncements(map[string]string{"recording-notice": "ann/recording-notice.wav", "recording-notice-video": "ann/recording-notice-video.wav"})
	}
	_ = objs.Put(context.Background(), "ann/recording-notice.wav", media.EncodeWAV(make([]byte, 1600), 8000))
	_ = objs.Put(context.Background(), "ann/recording-notice-video.wav", media.EncodeWAV(make([]byte, 1600), 8000))
	base := []pbxOpt{withMedia(false), doorFeatures(), withVideoOn(), withFeatures(feats), withVoicemail(newVMStore(), objs),
		func(c *Config, d *Deps) {
			d.Recordings = &fakeRecordings{}
			d.VideoRecordings = rows
			c.RecordingVideo, c.RecordingVideoSegment, c.RecordingVideoSegmentBytes = true, time.Second, 2<<10
			c.RecordingVideoQuotaBytes = 1 << 20
		}}
	return startPBX(t, doorDevices(), append(base, opts...)...)
}

// videoCall places a recorded call between two video phones and streams
// pictures from the caller until n have gone, returning the call and the notice
// fetches seen after the call is over.
func videoCall(t *testing.T, pbx *testPBX, n int) (cd callResult, b *phone) {
	t.Helper()
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b = newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	aRTP, _ := videoPhone(t, a, "sendrecv")
	videoPhone(t, b, "sendrecv")
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, b.invites, "INVITE")
	waitReq(t, b.acks, "ACK")
	answerA, ok := media.ParseVideoSDP(r.dcs.InviteResponse.Body())
	if !ok {
		t.Fatalf("no video in the answer:\n%s", r.dcs.InviteResponse.Body())
	}
	time.Sleep(300 * time.Millisecond) // the recording starts at the answer
	for _, pic := range h264Pictures(n, 4242) {
		for _, p := range pic {
			_, _ = aRTP.WriteToUDP(p, loopbackTo(answerA.Port))
		}
		time.Sleep(15 * time.Millisecond)
	}
	return r, b
}

// TestVideoRecordingOnCall fails if a recorded call with video does not keep the
// video next to the audio: the call's recording must start video files at the
// answer, store an init segment and fragments of the caller's stream in the
// recordings bucket as it goes (fragments cut at the keyframes once the size
// limit passes), keep the stream's row current, end it at hangup, say so in the
// trace, and play the video variant of the recording notice (S-42).
func TestVideoRecordingOnCall(t *testing.T) {
	objs, rows := &fakeObjects{}, &fakeVideoRows{}
	pbx := videoRecordingPBX(t, rows, objs, snapshot.Extension{Number: "200", RecordDefault: true})
	r, b := videoCall(t, pbx, 60)
	hangup(t, r.dcs)
	waitReq(t, b.byes, "BYE")
	cd := pbx.nextCDR(t)
	wantTrace(t, cd.Trace, "Video is recorded next to the audio")
	if !strings.Contains(traceText(cd.Trace), "Video stored (1 stream(s)") {
		t.Errorf("trace does not say what was stored:\n%s", traceText(cd.Trace))
	}
	streams := rows.streams()
	if len(streams) != 1 {
		t.Fatalf("streams = %+v", streams)
	}
	st := streams[0]
	if st.Leg != "caller" || st.Codec != "h264" || st.Container != "mp4" || !st.Ended || st.Failed != "" || st.Width != 640 || st.Height != 480 {
		t.Errorf("stream = %+v", st)
	}
	if len(st.Segments) < 2 {
		t.Errorf("fragments = %d, want several (2 KiB limit)", len(st.Segments))
	}
	all := objs.snapshot()
	init := all[st.InitObject()]
	if len(init) < 100 || string(init[4:8]) != "ftyp" || !bytes.Contains(init, []byte("avcC")) {
		t.Fatalf("init segment = %x", init[:min(len(init), 32)])
	}
	var total int64
	for _, s := range st.Segments {
		d := all[s.Object]
		if int64(len(d)) != s.Bytes || string(d[4:8]) != "moof" {
			t.Errorf("fragment %s is not a %d byte moof", s.Object, s.Bytes)
		}
		total += s.Bytes
	}
	if st.Bytes != total {
		t.Errorf("row bytes %d, fragments %d", st.Bytes, total)
	}
	if !strings.HasPrefix(st.Prefix, "rec/") || !strings.Contains(st.Prefix, "v1-caller-in.parts") || st.Final != strings.TrimSuffix(st.Prefix, ".parts")+".mp4" {
		t.Errorf("prefix = %q", st.Prefix)
	}
	if got := objs.snapshotGets(); !containsStr(got, "ann/recording-notice-video.wav") {
		t.Errorf("the video variant of the recording notice was not played; fetched %v", got)
	}
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestVideoRecordingPolicy fails if video is recorded where it must not be: an
// extension that keeps the audio only, the node's switch off, or a stored total
// at the quota. Each leaves the audio recording alone, writes no stream row and
// leaves the reason in the trace; and an object store that is down stops the
// stream with the reason without ending the call.
func TestVideoRecordingPolicy(t *testing.T) {
	run := func(t *testing.T, ext snapshot.Extension, opt pbxOpt, why string) {
		t.Helper()
		objs, rows := &fakeObjects{}, &fakeVideoRows{}
		var opts []pbxOpt
		if opt != nil {
			opts = append(opts, opt)
		}
		pbx := videoRecordingPBX(t, rows, objs, ext, opts...)
		r, b := videoCall(t, pbx, 20)
		hangup(t, r.dcs)
		waitReq(t, b.byes, "BYE")
		cd := pbx.nextCDR(t)
		if !strings.Contains(traceText(cd.Trace), why) {
			t.Errorf("trace %q does not say %q", traceText(cd.Trace), why)
		}
		if n := len(rows.streams()); n != 0 {
			t.Errorf("%d stream rows written", n)
		}
		for k := range objs.snapshot() {
			if strings.Contains(k, "-in.") {
				t.Errorf("video object %s stored", k)
			}
		}
	}
	t.Run("an extension that keeps the audio only", func(t *testing.T) {
		run(t, snapshot.Extension{Number: "200", RecordDefault: true, NoRecordVideo: true}, nil, "keeps the audio only")
	})
	t.Run("switched off", func(t *testing.T) {
		run(t, snapshot.Extension{Number: "200", RecordDefault: true}, func(c *Config, _ *Deps) { c.RecordingVideo = false }, "the recording is audio only")
	})
	t.Run("quota used up", func(t *testing.T) {
		before := media.VideoRecFailures("quota")
		run(t, snapshot.Extension{Number: "200", RecordDefault: true}, func(c *Config, d *Deps) {
			d.VideoRecordings = &fakeVideoRows{used: 2 << 20}
			c.RecordingVideoQuotaBytes = 1 << 20
		}, "quota")
		if media.VideoRecFailures("quota") != before+1 {
			t.Error("the refusal was not counted")
		}
	})
	t.Run("object store down", func(t *testing.T) {
		objs, rows := &fakeObjects{}, &fakeVideoRows{}
		pbx := videoRecordingPBX(t, rows, objs, snapshot.Extension{Number: "200", RecordDefault: true})
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)
		b := newPhone(t, pbx, "b1", "pb1")
		b.register(t)
		aRTP, _ := videoPhone(t, a, "sendrecv")
		videoPhone(t, b, "sendrecv")
		r := waitCall(t, dial(t.Context(), a, "200"))
		if r.err != nil {
			t.Fatal(r.err)
		}
		waitReq(t, b.invites, "INVITE")
		waitReq(t, b.acks, "ACK")
		answerA, _ := media.ParseVideoSDP(r.dcs.InviteResponse.Body())
		time.Sleep(300 * time.Millisecond)
		objs.mu.Lock()
		objs.down = true
		objs.mu.Unlock()
		for _, pic := range h264Pictures(40, 1) {
			for _, p := range pic {
				_, _ = aRTP.WriteToUDP(p, loopbackTo(answerA.Port))
			}
			time.Sleep(10 * time.Millisecond)
		}
		hangup(t, r.dcs)
		waitReq(t, b.byes, "BYE")
		cd := pbx.nextCDR(t)
		if !strings.Contains(traceText(cd.Trace), "Video recording stopped: object store") {
			t.Errorf("trace = %s", traceText(cd.Trace))
		}
	})
}
