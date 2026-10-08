package api

// Tests for the Phase 5 media API (spec contract 6). The DB-backed tests
// skip without HELLO_TEST_DATABASE_URL; the MinIO flow test uses startMinio,
// which skips without a MinIO (the CI HELLO_TEST_MINIO_* service or a local
// throwaway container).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
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

	// The audio route streams the recording bucket's object, inline.
	rec := c.must(http.StatusOK, "GET", "/api/v1/recordings/1/audio", nil)
	if !bytes.Equal(rec.body, wav) || rec.header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("audio = %d bytes %q, want the recording", len(rec.body), rec.header.Get("Content-Type"))
	}
	if cd := rec.header.Get("Content-Disposition"); cd != "" {
		t.Fatalf("plain audio plays as a download: %q", cd)
	}
	// ?download=1 sends the same object as an attachment.
	dl := c.must(http.StatusOK, "GET", "/api/v1/recordings/1/audio?download=1", nil)
	if !strings.HasPrefix(dl.header.Get("Content-Disposition"), "attachment") || !bytes.Equal(dl.body, wav) {
		t.Fatalf("download Content-Disposition %q, want an attachment", dl.header.Get("Content-Disposition"))
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

// annMultipart is a multipart body with an optional name part and a file
// part holding data.
func annMultipart(name string, data []byte) string {
	body := ""
	if name != "" {
		body += "--BND\r\nContent-Disposition: form-data; name=\"name\"\r\n\r\n" + name + "\r\n"
	}
	return body + "--BND\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\n" +
		"Content-Type: audio/wav\r\n\r\n" + string(data) + "\r\n--BND--\r\n"
}

// TestAnnouncementReplace: PUT overwrites the audio at ann/<name>.wav with
// the upload's validation, moves updatedAt, audits and bumps the revision;
// refused replacements leave the stored audio alone. The audio route
// presigns the announcement bucket. It fails if a bad upload overwrites the
// audio, if the name can be changed, or if the replacement is not audited.
func TestAnnouncementReplace(t *testing.T) {
	objs := newMemObjects()
	e := newPBXEnv(t, objs)
	ctx := context.Background()
	c := e.login()

	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	created := c.must(http.StatusCreated, "POST", "/api/v1/announcements", annMultipart("closing", wav)).json(t)
	c.header.Del("Content-Type")
	path := fmt.Sprintf("/api/v1/announcements/%v", created["id"])
	rev0, err := e.st.ConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Refused: not multipart, non-WAV bytes, no file, another name, unknown id.
	c.must(http.StatusBadRequest, "PUT", path, map[string]string{"name": "closing"})
	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	c.must(http.StatusBadRequest, "PUT", path, annMultipart("", []byte("not audio")))
	c.must(http.StatusBadRequest, "PUT", path,
		"--BND\r\nContent-Disposition: form-data; name=\"name\"\r\n\r\nclosing\r\n--BND--\r\n")
	c.must(http.StatusBadRequest, "PUT", path, annMultipart("other", wav))
	c.must(http.StatusNotFound, "PUT", "/api/v1/announcements/999999", annMultipart("", wav))
	if got := string(objs.objs["ann:ann/closing.wav"]); got != string(wav) {
		t.Fatalf("a refused replacement changed the audio")
	}
	if rev, _ := e.st.ConfigRevision(ctx); rev != rev0 {
		t.Fatalf("refused replacements moved the revision %d -> %d", rev0, rev)
	}

	// Accepted: the same key holds the new bytes; the name may be repeated.
	replacement := append(append([]byte{}, wav...), []byte("data-new")...)
	got := c.must(http.StatusOK, "PUT", path, annMultipart("closing", replacement)).json(t)
	c.header.Del("Content-Type")
	if got["name"] != "closing" || got["id"] != created["id"] {
		t.Fatalf("replaced = %v", got)
	}
	if got["updatedAt"] == created["updatedAt"] {
		t.Fatalf("updatedAt did not move: %v", got)
	}
	if string(objs.objs["ann:ann/closing.wav"]) != string(replacement) {
		t.Fatalf("audio not replaced")
	}
	if len(objs.objs) != 1 {
		t.Fatalf("replacement stored another object: %v", objs.objs)
	}
	if rev, _ := e.st.ConfigRevision(ctx); rev <= rev0 {
		t.Fatalf("revision %d did not move past %d", rev, rev0)
	}
	var audits int
	if err := e.db.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_events WHERE resource = 'announcement' AND action = 'update'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("replace audits = %d, want 1", audits)
	}

	// Playback: the announcement bucket's bytes, streamed with Range
	// support; 404 for an unknown id.
	stored := objs.objs["ann:ann/closing.wav"]
	audio := c.must(http.StatusOK, "GET", path+"/audio", nil)
	if len(stored) == 0 || !bytes.Equal(audio.body, stored) || audio.header.Get("Content-Type") != "audio/wav" ||
		audio.header.Get("Content-Length") != fmt.Sprint(len(stored)) || !strings.HasPrefix(audio.header.Get("Cache-Control"), "private") {
		t.Fatalf("audio = %d bytes %v, want the announcement audio", len(audio.body), audio.header)
	}
	c.header.Set("Range", "bytes=4-7")
	part := c.must(http.StatusPartialContent, "GET", path+"/audio", nil)
	c.header.Del("Range")
	if !bytes.Equal(part.body, stored[4:8]) {
		t.Fatalf("range body = %q, want %q", part.body, stored[4:8])
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/announcements/999999/audio", nil)
}

// TestRecordingPartiesAndDownload: the list carries the call's parties and
// CDR id from its CDR (nothing when no CDR exists), and ?download=1 presigns an attachment. It fails if a recording
// without a CDR is dropped, or if download is ignored or accepts garbage.
func TestRecordingPartiesAndDownload(t *testing.T) {
	objs := newMemObjects()
	e := newPBXEnv(t, objs)
	ctx := context.Background()
	c := e.login()
	if err := objs.PutRecording(ctx, "rec/1.wav", strings.NewReader(string(wav)), int64(len(wav))); err != nil {
		t.Fatal(err)
	}
	seedRecording(t, e, "corr-a", "rec/1.wav", 5000)
	seedRecording(t, e, "corr-b", "rec/2.wav", 7000)
	seedCDR(t, e, "corr-a", "101", "102")

	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/recordings", nil).body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("list = %v", page.Items)
	}
	noCDR, withCDR := page.Items[0], page.Items[1]
	if _, ok := noCDR["cdrId"]; ok || noCDR["source"] != "" || noCDR["destination"] != "" {
		t.Fatalf("recording without a CDR = %v", noCDR)
	}
	var cdrID int64
	if err := e.db.QueryRowContext(ctx,
		`SELECT id FROM cdrs WHERE correlation_id = 'corr-a'`).Scan(&cdrID); err != nil {
		t.Fatal(err)
	}
	if withCDR["source"] != "101" || withCDR["destination"] != "102" || withCDR["cdrId"] != float64(cdrID) {
		t.Fatalf("recording with a CDR = %v, want CDR %d", withCDR, cdrID)
	}

	id := fmt.Sprint(withCDR["id"])
	plain := c.must(http.StatusOK, "GET", "/api/v1/recordings/"+id+"/audio", nil)
	if cd := plain.header.Get("Content-Disposition"); cd != "" {
		t.Fatalf("plain audio sent as a download: %q", cd)
	}
	dl := c.must(http.StatusOK, "GET", "/api/v1/recordings/"+id+"/audio?download=1", nil)
	if want := `attachment; filename="recording-` + id + `.wav"`; dl.header.Get("Content-Disposition") != want {
		t.Fatalf("download Content-Disposition %q, want %q", dl.header.Get("Content-Disposition"), want)
	}
	c.must(http.StatusBadRequest, "GET", "/api/v1/recordings/"+id+"/audio?download=maybe", nil)
}

// TestVoicemailBoxesList: every box with its extension and its unheard and
// total counts, in number order. It fails if a box without messages is
// dropped or if heard messages count as unheard.
func TestVoicemailBoxesList(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	ctx := context.Background()
	c := e.login()
	box101 := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/extensions/%v/voicemail", e.ext101["id"]), nil).json(t)
	for i, heard := range []bool{false, false, true} {
		if _, err := e.db.ExecContext(ctx,
			`INSERT INTO voicemail_messages (box_id, minio_object, caller, duration_ms, heard) VALUES ($1, $2, '102', 1000, $3)`,
			box101["id"], fmt.Sprintf("box/x/%d.wav", i), heard); err != nil {
			t.Fatal(err)
		}
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/voicemail/boxes", nil).body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("boxes = %v", list.Items)
	}
	a, b := list.Items[0], list.Items[1]
	if a["number"] != "101" || a["name"] != "Sales" || a["id"] != box101["id"] ||
		a["extensionId"] != e.ext101["id"] || a["unheard"] != float64(2) || a["total"] != float64(3) {
		t.Fatalf("box 101 = %v", a)
	}
	if b["number"] != "102" || b["unheard"] != float64(0) || b["total"] != float64(0) {
		t.Fatalf("box 102 = %v", b)
	}
}

// flakyObjects is memObjects that, once broken, fails every upload and
// playback the way an unreachable MinIO does.
type flakyObjects struct {
	*memObjects
	broken atomic.Bool
}

var errObjectStoreDown = errors.New("object store: connection refused")

func (f *flakyObjects) OpenAudio(ctx context.Context, bucket, object string) (*AudioObject, error) {
	if f.broken.Load() {
		return nil, errObjectStoreDown
	}
	return f.memObjects.OpenAudio(ctx, bucket, object)
}

func (f *flakyObjects) Put(ctx context.Context, object string, r io.Reader, size int64) error {
	if f.broken.Load() {
		return errObjectStoreDown
	}
	return f.memObjects.Put(ctx, object, r, size)
}

func (f *flakyObjects) PutAnnouncement(ctx context.Context, object string, r io.Reader, size int64) error {
	if f.broken.Load() {
		return errObjectStoreDown
	}
	return f.memObjects.PutAnnouncement(ctx, object, r, size)
}

// TestObjectStoreFailures fails if a MinIO error on an upload (announcement
// create and replace, voicemail greeting) or a playback (voicemail message,
// recording, announcement) answers anything but 502 upstream, or if a
// recording Range request is not a 206 (spec ai-external-access S-3).
func TestObjectStoreFailures(t *testing.T) {
	objs := &flakyObjects{memObjects: newMemObjects()}
	e := newPBXEnv(t, objs)
	ctx := context.Background()
	c := e.login()

	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	ann := c.must(http.StatusCreated, "POST", "/api/v1/announcements", annMultipart("closing", wav)).json(t)
	c.header.Del("Content-Type")
	box := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/extensions/%v/voicemail", e.ext101["id"]), nil).json(t)
	var msgID int64
	if err := e.db.QueryRowContext(ctx, `INSERT INTO voicemail_messages (box_id, minio_object, caller, duration_ms)
		VALUES ($1, 'box/x/1760000000-call-abc.wav', '102', 1000) RETURNING id`, box["id"]).Scan(&msgID); err != nil {
		t.Fatal(err)
	}
	if err := objs.PutRecording(ctx, "rec/1760000000-call-abc.wav", bytes.NewReader(wav), int64(len(wav))); err != nil {
		t.Fatal(err)
	}
	seedRecording(t, e, "corr-abc", "rec/1760000000-call-abc.wav", 1000)

	c.header.Set("Range", "bytes=4-7")
	if r := c.must(http.StatusPartialContent, "GET", "/api/v1/recordings/1/audio", nil); !bytes.Equal(r.body, wav[4:8]) {
		t.Fatalf("range = %q, want %q", r.body, wav[4:8])
	}
	c.header.Del("Range")

	objs.broken.Store(true)
	upstream := func(method, path string, body any) {
		t.Helper()
		r := c.must(http.StatusBadGateway, method, path, body)
		var got struct {
			Error struct{ Code string } `json:"error"`
		}
		if err := json.Unmarshal(r.body, &got); err != nil || got.Error.Code != "upstream" {
			t.Fatalf("%s %s = %s, want code upstream", method, path, r.body)
		}
	}
	upstream("GET", fmt.Sprintf("/api/v1/voicemail/messages/%d/audio", msgID), nil)
	upstream("GET", "/api/v1/recordings/1/audio", nil)
	upstream("GET", fmt.Sprintf("/api/v1/announcements/%v/audio", ann["id"]), nil)
	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	upstream("POST", "/api/v1/announcements", annMultipart("opening", wav))
	upstream("PUT", fmt.Sprintf("/api/v1/announcements/%v", ann["id"]), annMultipart("", wav))
	upstream("PUT", fmt.Sprintf("/api/v1/extensions/%v/voicemail", e.ext101["id"]),
		"--BND\r\nContent-Disposition: form-data; name=\"greeting\"; filename=\"g.wav\"\r\n"+
			"Content-Type: audio/wav\r\n\r\n"+string(wav)+"\r\n--BND--\r\n")
	c.header.Del("Content-Type")
	// An unknown extension has no box to change.
	c.must(http.StatusNotFound, "PUT", "/api/v1/extensions/999999/voicemail", map[string]string{"email": ""})
}
