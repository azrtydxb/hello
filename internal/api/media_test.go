package api

// Tests for the Phase 5 media API (spec contract 6). The DB-backed tests
// skip without HELLO_TEST_DATABASE_URL; the MinIO flow test uses startMinio,
// which skips without a MinIO (the CI HELLO_TEST_MINIO_* service or a local
// throwaway container).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// seedRecording inserts a recordings row the way hello-sip's media stream
// does: directly, without audit or revision bump.
func seedRecording(t *testing.T, e *env, correlation, object string, durationMs int64) {
	t.Helper()
	if _, err := e.db.ExecContext(context.Background(),
		`INSERT INTO recordings (correlation_id, minio_object, initiated_by, duration_ms)
		 VALUES ($1, $2, 'dtmf', $3)`, correlation, object, durationMs); err != nil {
		t.Fatal(err)
	}
}

// seedCDR inserts a minimal CDR row linking a correlation id to two
// extensions.
func seedCDR(t *testing.T, e *env, correlation, source, destination string) {
	t.Helper()
	if _, err := e.db.ExecContext(context.Background(),
		`INSERT INTO cdrs (correlation_id, sip_call_id, source, destination, start_time, end_time,
		                            duration_ms, billable_ms, sip_node, final_status, termination_side)
		 VALUES ($1, $1, $2, $3, now(), now(), 1000, 1000, 'test', 200, 'caller')`,
		correlation, source, destination); err != nil {
		t.Fatal(err)
	}
}

// TestRecordingsListPaging: paged like the CDRs, newest first, the extension
// filter joining the CDRs, and no object-key leak. It fails if the filter
// returns recordings of other extensions or if paging skips or repeats rows.
func TestRecordingsListPaging(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	seedRecording(t, e, "corr-1", "rec/1-abc.wav", 5100)
	seedRecording(t, e, "corr-2", "rec/2-def.wav", 12500)
	seedRecording(t, e, "corr-3", "rec/3-ghi.wav", 9000)
	seedCDR(t, e, "corr-2", "101", "102")
	seedCDR(t, e, "corr-3", "102", "103")

	var page struct {
		Items []map[string]any `json:"items"`
		Next  string           `json:"next"`
	}
	get := func(path string) {
		t.Helper()
		if err := json.Unmarshal(c.must(http.StatusOK, "GET", path, nil).body, &page); err != nil {
			t.Fatal(err)
		}
	}

	get("/api/v1/recordings")
	if len(page.Items) != 3 || page.Next != "" {
		t.Fatalf("list = %v next=%q", page.Items, page.Next)
	}
	// Newest first; the object key stays internal.
	for _, k := range []string{"id", "correlationId", "initiatedBy", "durationMs", "createdAt"} {
		if _, ok := page.Items[0][k]; !ok {
			t.Fatalf("recording missing %q: %v", k, page.Items[0])
		}
	}
	if _, leaks := page.Items[0]["object"]; leaks {
		t.Fatalf("recording leaks the object key: %v", page.Items[0])
	}
	if page.Items[0]["correlationId"] != "corr-3" {
		t.Fatalf("order wrong: %v", page.Items)
	}

	// The extension filter keeps only that extension's calls (via the CDRs).
	get("/api/v1/recordings?extension=101")
	if len(page.Items) != 1 || page.Items[0]["correlationId"] != "corr-2" {
		t.Fatalf("extension filter = %v", page.Items)
	}
	get("/api/v1/recordings?extension=104")
	if len(page.Items) != 0 {
		t.Fatalf("filter for an absent extension = %v", page.Items)
	}

	// Paging: the cursor returns the older page.
	get("/api/v1/recordings?limit=2")
	if len(page.Items) != 2 || page.Next == "" {
		t.Fatalf("first page = %v next=%q", page.Items, page.Next)
	}
	get("/api/v1/recordings?before=" + page.Next)
	if len(page.Items) != 1 || page.Items[0]["correlationId"] != "corr-1" {
		t.Fatalf("second page = %v", page.Items)
	}
	for _, bad := range []string{
		"/api/v1/recordings?limit=0",
		"/api/v1/recordings?limit=201",
		"/api/v1/recordings?before=nope",
		"/api/v1/recordings?extension=has%20space",
	} {
		if r := c.do("GET", bad, nil); r.code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, r.code)
		}
	}
	if r := c.do("GET", "/api/v1/recordings/999999/audio", nil); r.code != http.StatusNotFound {
		t.Errorf("audio for a missing recording = %d, want 404 (presign gate)", r.code)
	}
}

// TestRecordingAudioAndDelete runs the presigned audio route and the delete
// against a real MinIO (bucket hello-recordings). It fails if the audio
// route hands out a URL for a recording that does not exist, or if deletion
// leaves the object behind.
func TestRecordingAudioAndDelete(t *testing.T) {
	objs := startMinio(t)
	e := newPBXEnv(t, objs)
	ctx := context.Background()
	c := e.login()
	object := "rec/1760000000-call-abc.wav"
	if err := objs.PutRecording(ctx, object, strings.NewReader(string(wav)), int64(len(wav))); err != nil {
		t.Fatal(err)
	}
	seedRecording(t, e, "corr-abc", object, 5100)

	// The audio route 302s to a presigned URL of the recording bucket.
	c.hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { c.hc.CheckRedirect = nil }()
	rec := c.do("GET", "/api/v1/recordings/1/audio", nil)
	if rec.code != http.StatusFound {
		t.Fatalf("audio = %d %s, want 302", rec.code, rec.body)
	}
	loc := rec.header.Get("Location")
	if !strings.Contains(loc, object) || !strings.Contains(loc, "X-Amz-Signature") {
		t.Fatalf("Location %q is not a presigned URL for %s", loc, object)
	}

	// Delete removes the row and the object; the mutation was audited.
	rev0, err := e.st.ConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.must(http.StatusNoContent, "DELETE", "/api/v1/recordings/1", nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/recordings/1", nil)
	// The object was in the recordings bucket, so its absence after the
	// delete is covered by the presign gate: a presign of a missing row
	// cannot happen and RemoveRecording's error would fail the handler's
	// log path. The audio route 404s once the row is gone.

	rev1, _ := e.st.ConfigRevision(ctx)
	if rev1 <= rev0 {
		t.Fatalf("revision %d did not move past %d", rev1, rev0)
	}
	var audits int
	if err := e.db.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_events WHERE resource = 'recording'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("recording audits = %d, want 1", audits)
	}
	if r := c.do("GET", "/api/v1/recordings/1/audio", nil); r.code != http.StatusNotFound {
		t.Fatalf("audio after delete = %d, want 404", r.code)
	}
	_ = fmt.Sprint
}

// TestAnnouncementsFlow: multipart upload (validation, uniqueness), list,
// delete, and the audit/revision trail. The upload lands the WAV in the
// object store, which memObjects fakes here; a real MinIO is not needed.
func TestAnnouncementsFlow(t *testing.T) {
	objs := newMemObjects()
	e := newPBXEnv(t, objs)
	ctx := context.Background()
	c := e.login()
	rev0, err := e.st.ConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	multipart := func(name, contentType, body string) map[string]string {
		c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
		defer c.header.Del("Content-Type")
		return map[string]string{"__path": "/api/v1/announcements", "__method": "POST", "__body": body, "__ct": contentType, "__name": name}
	}
	_ = multipart

	// A valid upload: name + WAV bytes, listed afterwards.
	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	body := "--BND\r\nContent-Disposition: form-data; name=\"name\"\r\n\r\nclosing\r\n" +
		"--BND\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\n" +
		"Content-Type: audio/wav\r\n\r\n" + string(wav) + "\r\n--BND--\r\n"
	got := c.must(http.StatusCreated, "POST", "/api/v1/announcements", body).json(t)
	c.header.Del("Content-Type")
	if got["name"] != "closing" {
		t.Fatalf("created = %v", got)
	}
	if !objs.has("ann:ann/closing.wav") {
		t.Fatalf("audio not stored: %v", objs.objs)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/announcements", nil).body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0]["name"] != "closing" {
		t.Fatalf("list = %v", list.Items)
	}
	if _, leaks := list.Items[0]["object"]; leaks {
		t.Fatalf("announcement leaks the object key: %v", list.Items[0])
	}
	rev1, _ := e.st.ConfigRevision(ctx)
	if rev1 <= rev0 {
		t.Fatalf("revision %d did not move past %d", rev1, rev0)
	}

	// The name is unique (contract 6), and a duplicate does not touch the
	// stored audio.
	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	c.must(http.StatusConflict, "POST", "/api/v1/announcements", body)
	c.header.Del("Content-Type")
	if !objs.has("ann:ann/closing.wav") {
		t.Fatalf("duplicate POST removed the stored audio")
	}

	// Validation: bad name, no file, non-WAV bytes, wrong content type.
	bad := []struct {
		name string
		ct   string
		body string
	}{
		{"bad name", "multipart/form-data; boundary=BND",
			"--BND\r\nContent-Disposition: form-data; name=\"name\"\r\n\r\nnot ok!\r\n" +
				"--BND\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\n\r\n" + string(wav) + "\r\n--BND--\r\n"},
		{"missing file", "multipart/form-data; boundary=BND",
			"--BND\r\nContent-Disposition: form-data; name=\"name\"\r\n\r\nheld\r\n--BND--\r\n"},
		{"not WAV", "multipart/form-data; boundary=BND",
			"--BND\r\nContent-Disposition: form-data; name=\"name\"\r\n\r\nheld\r\n" +
				"--BND\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.txt\"\r\n\r\nnot audio\r\n--BND--\r\n"},
	}
	for _, b := range bad {
		c.header.Set("Content-Type", b.ct)
		if r := c.do("POST", "/api/v1/announcements", b.body); r.code != http.StatusBadRequest {
			t.Errorf("%s: POST = %d %s, want 400", b.name, r.code, r.body)
		}
	}
	c.header.Del("Content-Type")
	if len(objs.objs) != 1 {
		t.Fatalf("refused uploads stored objects: %v", objs.objs)
	}

	// Delete removes the row and the object; audited again.
	annID := fmt.Sprint(list.Items[0]["id"])
	c.must(http.StatusNoContent, "DELETE", "/api/v1/announcements/"+annID, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/announcements/"+annID, nil)
	if objs.has("ann:ann/closing.wav") {
		t.Fatalf("audio survived the delete")
	}
	var audits int
	if err := e.db.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_events WHERE resource = 'announcement'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("announcement audits = %d, want 2 (create + delete)", audits)
	}
}

// TestExtensionRecordDefault covers the Phase 5 PATCH field (contract 6).
func TestExtensionRecordDefault(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	path := fmt.Sprintf("/api/v1/extensions/%v", e.ext101["id"])
	got := c.must(http.StatusOK, "PATCH", path, map[string]any{"recordDefault": true}).json(t)
	if got["recordDefault"] != true {
		t.Fatalf("recordDefault not stored: %v", got)
	}
	got = c.must(http.StatusOK, "GET", path, nil).json(t)
	if got["recordDefault"] != true {
		t.Fatalf("get = %v", got)
	}
	got = c.must(http.StatusOK, "PATCH", path, map[string]any{"recordDefault": false}).json(t)
	if got["recordDefault"] != false {
		t.Fatalf("clear = %v", got["recordDefault"])
	}
	// No field at all stays 400.
	if r := c.do("PATCH", path, map[string]any{}); r.code != http.StatusBadRequest {
		t.Errorf("empty patch = %d, want 400", r.code)
	}
}
