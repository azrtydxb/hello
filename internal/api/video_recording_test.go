package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func seedVideoStream(t *testing.T, e *env, corr string, part int, leg, state, prefix, final string, segs []string) int64 {
	t.Helper()
	list := "["
	var bytesTotal int
	for i, s := range segs {
		if i > 0 {
			list += ","
		}
		list += fmt.Sprintf(`{"seq":%d,"object":%q,"bytes":%d,"ticks":90000,"base":%d,"startMs":%d,"key":true}`, i+1, s, 5, i*90000, i*1000)
		bytesTotal += 5
	}
	list += "]"
	var id int64
	err := e.db.QueryRowContext(context.Background(), `
		INSERT INTO recording_streams (call_correlation, part, leg, codec, container, object_prefix, segments, bytes, width, height,
			offset_ms, duration_ms, segment_list, final_object, state, ended_at)
		VALUES ($1, $2, $3, 'h264', 'mp4', $4, $5, $6, 640, 360, 1200, 2000, $7::jsonb, NULLIF($8, ''), $9, now()) RETURNING id`,
		corr, part, leg, prefix, len(segs), bytesTotal, list, final, state).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestRecordingVideoAPI fails if a recording's manifest does not list its video
// streams with offsets, if a finalized stream is not served as its file with
// Range support and the headers a recording needs (video/mp4, private no-store),
// if a stream that is not finalized is not served as its init segment and
// fragments joined (also with Range across the join), if a download is not
// audited, if the list does not say which recordings have video, or if deleting
// a recording leaves its video rows or objects behind (S-42).
func TestRecordingVideoAPI(t *testing.T) {
	objs := newMemObjects()
	e := newPBXEnv(t, objs)
	c := e.login()
	seedRecording(t, e, "corr-v", "rec/1-v.wav", 8000)
	seedRecording(t, e, "corr-a", "rec/2-a.wav", 8000)
	pfx := "rec/1-v/v1-caller-in.parts"
	objs.objs["rec:"+pfx+"/init"] = []byte("INIT_")
	objs.objs["rec:"+pfx+"/1"] = []byte("FRAG1")
	objs.objs["rec:"+pfx+"/2"] = []byte("FRAG2")
	objs.objs["rec:rec/1-v/v1-callee-in.mp4"] = []byte("0123456789")
	open := seedVideoStream(t, e, "corr-v", 1, "caller", "ended", pfx, "", []string{pfx + "/1", pfx + "/2"})
	done := seedVideoStream(t, e, "corr-v", 1, "callee", "finalized", "rec/1-v/v1-callee-in.parts", "rec/1-v/v1-callee-in.mp4", nil)
	var rec struct {
		Items []struct {
			ID    int64
			Video bool
			Corr  string `json:"correlationId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/recordings", nil).body, &rec); err != nil {
		t.Fatal(err)
	}
	var vid, aud int64
	for _, it := range rec.Items {
		if it.Corr == "corr-v" {
			vid = it.ID
			if !it.Video {
				t.Error("the list does not say the recording has video")
			}
		} else {
			aud = it.ID
			if it.Video {
				t.Error("the list says an audio-only recording has video")
			}
		}
	}

	man := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/recordings/%d/manifest", vid), nil)
	if man.header.Get("Cache-Control") != "private, no-store" {
		t.Errorf("manifest Cache-Control = %q", man.header.Get("Cache-Control"))
	}
	var m struct {
		Streams []struct {
			ID        int64
			Leg       string
			Codec     string
			OffsetMs  int64
			Finalized bool
			MediaType string
			Segments  int
		}
	}
	if err := json.Unmarshal(man.body, &m); err != nil || len(m.Streams) != 2 {
		t.Fatalf("manifest = %s", man.body)
	}
	if strings.Contains(string(man.body), "object") || strings.Contains(string(man.body), "rec/1-v") {
		t.Errorf("the manifest leaks object names: %s", man.body)
	}
	for _, s := range m.Streams {
		if s.OffsetMs != 1200 || s.Codec != "h264" || s.MediaType != "video/mp4" {
			t.Errorf("stream = %+v", s)
		}
		if (s.ID == done) != s.Finalized {
			t.Errorf("stream %d finalized = %v", s.ID, s.Finalized)
		}
	}
	if aud != 0 {
		if auditless := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/recordings/%d/manifest", aud), nil).json(t); len(auditless["streams"].([]any)) != 0 {
			t.Errorf("an audio-only recording lists streams: %v", auditless)
		}
	}

	// A finalized stream is its file.
	f := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/recordings/%d/video/%d", vid, done), nil)
	if string(f.body) != "0123456789" || f.header.Get("Content-Type") != "video/mp4" || f.header.Get("Cache-Control") != "private, no-store" {
		t.Errorf("finalized stream = %q %v", f.body, f.header)
	}
	c.header.Set("Range", "bytes=2-4")
	p := c.must(http.StatusPartialContent, "GET", fmt.Sprintf("/api/v1/recordings/%d/video/%d", vid, done), nil)
	c.header.Del("Range")
	if string(p.body) != "234" {
		t.Errorf("range = %q", p.body)
	}
	// One that is not is the pieces joined, Range across the seam included.
	j := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/recordings/%d/video/%d", vid, open), nil)
	if string(j.body) != "INIT_FRAG1FRAG2" {
		t.Errorf("joined = %q", j.body)
	}
	c.header.Set("Range", "bytes=3-11")
	p = c.must(http.StatusPartialContent, "GET", fmt.Sprintf("/api/v1/recordings/%d/video/%d", vid, open), nil)
	c.header.Del("Range")
	if string(p.body) != "T_FRAG1FR" {
		t.Errorf("range across fragments = %q", p.body)
	}
	dl := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/recordings/%d/video/%d?download=1", vid, done), nil)
	if !strings.Contains(dl.header.Get("Content-Disposition"), "attachment") {
		t.Errorf("download header = %v", dl.header)
	}
	c.must(http.StatusBadRequest, "GET", fmt.Sprintf("/api/v1/recordings/%d/video/%d?download=maybe", vid, done), nil)
	// A finalized stream whose file the object store cannot open is a bad gateway,
	// not a missing recording.
	lost := seedVideoStream(t, e, "corr-v", 2, "caller", "finalized", "rec/1-v/v2-caller-in.parts", "rec/1-v/v2-caller-in.mp4", nil)
	c.must(http.StatusBadGateway, "GET", fmt.Sprintf("/api/v1/recordings/%d/video/%d", vid, lost), nil)
	c.must(http.StatusNotFound, "GET", fmt.Sprintf("/api/v1/recordings/%d/video/99999", vid), nil)
	c.must(http.StatusNotFound, "GET", fmt.Sprintf("/api/v1/recordings/%d/video/%d", aud, done), nil) // another call's stream
	var audits int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'recording.video.download'`).Scan(&audits); err != nil || audits < 3 {
		t.Errorf("download audits = %d (%v)", audits, err)
	}

	// Deleting the recording takes the video with it.
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/recordings/%d", vid), nil)
	var left int
	_ = e.db.QueryRow(`SELECT count(*) FROM recording_streams WHERE call_correlation = 'corr-v'`).Scan(&left)
	if left != 0 {
		t.Errorf("%d stream rows left", left)
	}
	for k := range objs.objs {
		if strings.HasPrefix(k, "rec:rec/1-v/") {
			t.Errorf("object %s left behind", k)
		}
	}
	_ = bytes.MinRead
}

// TestRecordingSettingsAPI fails if the video retention can be read or changed
// by anyone but an admin, if it accepts a value outside 1 to 3650 days, or if a
// change is not audited.
func TestRecordingSettingsAPI(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	if got := c.must(http.StatusOK, "GET", "/api/v1/recordings/settings", nil).json(t); got["videoRetentionDays"] != float64(30) {
		t.Fatalf("default = %v", got)
	}
	for _, bad := range []any{map[string]int{"videoRetentionDays": 0}, map[string]int{"videoRetentionDays": 3651}, map[string]string{"x": "y"}} {
		c.must(http.StatusBadRequest, "PUT", "/api/v1/recordings/settings", bad)
	}
	if got := c.must(http.StatusOK, "PUT", "/api/v1/recordings/settings", map[string]int{"videoRetentionDays": 7}).json(t); got["videoRetentionDays"] != float64(7) {
		t.Fatalf("after change = %v", got)
	}
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE resource = 'recording_settings'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("audits = %d (%v)", n, err)
	}
	anon := e.client()
	anon.must(http.StatusUnauthorized, "GET", "/api/v1/recordings/settings", nil)
}
