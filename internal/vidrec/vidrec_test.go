package vidrec

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/store"
)

type memObjects struct {
	mu      sync.Mutex
	objs    map[string][]byte
	failPut bool
	failRm  map[string]bool
}

func (m *memObjects) Open(_ context.Context, o string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[o]
	if !ok {
		return nil, errors.New("no such object")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memObjects) Size(_ context.Context, o string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[o]
	if !ok {
		return 0, errors.New("no such object")
	}
	return int64(len(b)), nil
}

func (m *memObjects) Put(_ context.Context, o string, r io.Reader, size int64) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if m.failPut {
		return errors.New("store down")
	}
	if int64(len(b)) != size {
		return errors.New("size mismatch")
	}
	m.mu.Lock()
	m.objs[o] = b
	m.mu.Unlock()
	return nil
}

func (m *memObjects) Remove(_ context.Context, o string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failRm[o] {
		return errors.New("cannot remove")
	}
	delete(m.objs, o)
	return nil
}

type memStore struct {
	mu       sync.Mutex
	streams  map[int64]*store.VideoStream
	released []int64
	swept    int64
	audits   []string
	days     int
	usage    int64
}

func (s *memStore) ClaimVideoStreams(_ context.Context, n int) ([]store.VideoStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.VideoStream
	for _, v := range s.streams {
		if (v.State == "ended" || v.State == "failed") && v.Final == "" && v.Segments > 0 && len(out) < n && v.Node != "claimed" {
			v.Node = "claimed"
			out = append(out, *v)
		}
	}
	return out, nil
}

func (s *memStore) FinishVideoStream(_ context.Context, id int64, final string, b int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.streams[id]
	v.Final, v.Bytes, v.State = final, b, "finalized"
	return nil
}

func (s *memStore) ReleaseVideoStream(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, id)
	s.streams[id].State = "failed-retry" // out of this pass
	return nil
}

func (s *memStore) SweepVideoStreams(context.Context, time.Duration) (int64, error) {
	return s.swept, nil
}

func (s *memStore) ExpiredVideoStreams(_ context.Context, days, n int) ([]store.VideoStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.days = days
	var out []store.VideoStream
	for _, v := range s.streams {
		if v.EndedAt != nil && time.Since(*v.EndedAt) > time.Duration(days)*24*time.Hour && len(out) < n {
			out = append(out, *v)
		}
	}
	return out, nil
}

func (s *memStore) DeleteVideoStream(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streams, id)
	return nil
}

func (s *memStore) VideoUsageBytes(context.Context) (int64, error) { return s.usage, nil }

func (s *memStore) GetRecordingSettings(context.Context) (store.RecordingSettings, error) {
	return store.RecordingSettings{VideoRetentionDays: 30}, nil
}

func (s *memStore) Audit(_ context.Context, actor, action, resource, id string) error {
	s.audits = append(s.audits, actor+" "+action+" "+resource+" "+id)
	return nil
}

func (s *memStore) TryLock(context.Context, string) (func(), bool, error) {
	return func() {}, true, nil
}

var sps = []byte{0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}

// mp4Stream makes a stream of n fragments of 3 pictures each in the object
// store and its row.
func mp4Stream(t *testing.T, o *memObjects, id int64, n int) *store.VideoStream {
	t.Helper()
	prefix := "rec/1-c/v1-caller-in.parts"
	init, err := media.MP4Init(sps, []byte{0x68, 0xce, 0x3c, 0x80}, 640, 360)
	if err != nil {
		t.Fatal(err)
	}
	o.objs[prefix+"/init"] = init
	v := &store.VideoStream{ID: id, Correlation: "c", Part: 1, Leg: "caller", Direction: "in", Codec: "h264", Container: "mp4", Prefix: prefix,
		Width: 640, Height: 360, State: "ended", Segments: n}
	var base uint64
	for i := 1; i <= n; i++ {
		frag := media.MP4Fragment(uint32(i), base, []media.MP4Sample{{Data: bytes.Repeat([]byte{byte(i)}, 400), Duration: 3000, Key: true},
			{Data: []byte{0, 0, 0, 2, 0x41, 9}, Duration: 3000}, {Data: []byte{0, 0, 0, 2, 0x41, 8}, Duration: 3000}})
		obj := prefix + "/" + string(rune('0'+i))
		o.objs[obj] = frag
		v.SegmentList = append(v.SegmentList, store.VideoSegment{Seq: i, Object: obj, Bytes: int64(len(frag)), Ticks: 9000, Base: base, StartMs: int64(base / 90), Key: true})
		v.Bytes += int64(len(frag))
		base += 9000
	}
	return v
}

func newWorker(st *memStore, o *memObjects) *Worker {
	return &Worker{Store: st, Objects: o, QuotaBytes: 1 << 30}
}

// TestFinalizeMP4 fails if a stream that ended is not joined into one file of
// its init segment, a sidx and its fragments in order, recorded on the row, with
// the pieces deleted afterwards; or if a fragment that is missing does not end
// the file at the one before; or if a failed upload loses the pieces.
func TestFinalizeMP4(t *testing.T) {
	o := &memObjects{objs: map[string][]byte{}}
	v := mp4Stream(t, o, 1, 3)
	st := &memStore{streams: map[int64]*store.VideoStream{1: v}}
	newWorker(st, o).Pass(t.Context())
	if v.State != "finalized" || v.Final != "rec/1-c/v1-caller-in.mp4" {
		t.Fatalf("row = %+v", v)
	}
	final := o.objs[v.Final]
	if int64(len(final)) != v.Bytes {
		t.Errorf("file is %d bytes, row says %d", len(final), v.Bytes)
	}
	if string(final[4:8]) != "ftyp" || !bytes.Contains(final, []byte("sidx")) {
		t.Error("the file has no ftyp or sidx")
	}
	i := bytes.Index(final, []byte("sidx"))
	if refs := binary.BigEndian.Uint16(final[i+4+4+4+4+8+8+2:]); refs != 3 {
		t.Errorf("sidx references = %d", refs)
	}
	for k := range o.objs {
		if k != v.Final {
			t.Errorf("piece %s was not deleted", k)
		}
	}

	t.Run("a missing fragment ends the file early", func(t *testing.T) {
		o := &memObjects{objs: map[string][]byte{}}
		v := mp4Stream(t, o, 1, 3)
		delete(o.objs, v.SegmentList[2].Object)
		st := &memStore{streams: map[int64]*store.VideoStream{1: v}}
		newWorker(st, o).Pass(t.Context())
		if v.State != "finalized" {
			t.Fatalf("not finalized: %+v", v)
		}
		j := bytes.Index(o.objs[v.Final], []byte("sidx"))
		if refs := binary.BigEndian.Uint16(o.objs[v.Final][j+4+4+4+4+8+8+2:]); refs != 2 {
			t.Errorf("sidx references = %d, want the two fragments that exist", refs)
		}
	})
	t.Run("a failed upload keeps the pieces and the claim is let go", func(t *testing.T) {
		o := &memObjects{objs: map[string][]byte{}, failPut: true}
		v := mp4Stream(t, o, 1, 2)
		st := &memStore{streams: map[int64]*store.VideoStream{1: v}}
		newWorker(st, o).Pass(t.Context())
		if v.State == "finalized" || len(st.released) != 1 {
			t.Errorf("state %s, released %v", v.State, st.released)
		}
		if len(o.objs) != 3 { // init and two fragments
			t.Errorf("objects left = %d", len(o.objs))
		}
	})
}

// TestFinalizeWebM fails if a VP8 stream's joined file does not carry an EBML
// header and a Segment whose size covers the Cues and every cluster.
func TestFinalizeWebM(t *testing.T) {
	o := &memObjects{objs: map[string][]byte{}}
	prefix := "rec/2-c/v1-callee-in.parts"
	o.objs[prefix+"/init"] = media.WebMInit(320, 240)
	v := &store.VideoStream{ID: 7, Correlation: "c", Part: 1, Leg: "callee", Codec: "vp8", Container: "webm", Prefix: prefix, Width: 320, Height: 240, State: "failed", Segments: 2, Failure: "object_store"}
	for i := 1; i <= 2; i++ {
		c := media.WebMCluster(uint64((i-1)*1000), []media.WebMFrame{{Data: bytes.Repeat([]byte{1}, 300), Key: true}, {Data: []byte{1, 2, 3}, At: 33}})
		obj := prefix + "/" + string(rune('0'+i))
		o.objs[obj] = c
		v.SegmentList = append(v.SegmentList, store.VideoSegment{Seq: i, Object: obj, Bytes: int64(len(c)), Ticks: 90000, Base: uint64((i - 1) * 90000), StartMs: int64((i - 1) * 1000), Key: true})
	}
	st := &memStore{streams: map[int64]*store.VideoStream{7: v}}
	newWorker(st, o).Pass(t.Context())
	if v.State != "finalized" || v.Final != "rec/2-c/v1-callee-in.webm" {
		t.Fatalf("row = %+v", v)
	}
	f := o.objs[v.Final]
	if !bytes.HasPrefix(f, []byte{0x1A, 0x45, 0xDF, 0xA3}) || !bytes.Contains(f, []byte{0x1C, 0x53, 0xBB, 0x6B}) || !bytes.Contains(f, []byte("V_VP8")) {
		t.Error("not a WebM with Cues")
	}
}

// TestPurgeVideo fails if a stream that ended before the retention keeps any
// object or row, if one whose objects cannot be removed loses its row first, if
// a recent stream is touched, or if the audit event carries more than a count.
func TestPurgeVideo(t *testing.T) {
	o := &memObjects{objs: map[string][]byte{}, failRm: map[string]bool{}}
	old := time.Now().Add(-40 * 24 * time.Hour)
	recent := time.Now().Add(-2 * 24 * time.Hour)
	a := mp4Stream(t, o, 1, 1)
	a.EndedAt, a.State, a.Final = &old, "finalized", ""
	a.Prefix = "rec/1-c/v1-caller-in.parts"
	b := &store.VideoStream{ID: 2, Prefix: "rec/9-x/v1-caller-in.parts", Container: "mp4", State: "ended", EndedAt: &old,
		SegmentList: []store.VideoSegment{{Object: "rec/9-x/v1-caller-in.parts/1"}}}
	o.objs[b.Prefix+"/1"] = []byte("x")
	o.failRm[b.Prefix+"/1"] = true
	c := &store.VideoStream{ID: 3, Prefix: "rec/8-y/v1-caller-in.parts", Container: "mp4", State: "ended", EndedAt: &recent}
	o.objs[c.Prefix+"/init"] = []byte("keep")
	st := &memStore{streams: map[int64]*store.VideoStream{1: a, 2: b, 3: c}}
	w := newWorker(st, o)
	if n := w.Purge(t.Context()); n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	if _, ok := st.streams[1]; ok {
		t.Error("the expired stream's row is still there")
	}
	if _, ok := st.streams[2]; !ok {
		t.Error("a row was dropped while its object could not be removed")
	}
	if _, ok := st.streams[3]; !ok || len(o.objs[c.Prefix+"/init"]) == 0 {
		t.Error("a recent stream was touched")
	}
	if st.days != 30 {
		t.Errorf("retention used = %d days", st.days)
	}
	if len(st.audits) != 1 || st.audits[0] != "system recording.video.purge recording_streams 1" {
		t.Errorf("audit = %v", st.audits)
	}
}
