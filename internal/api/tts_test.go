package api

// Tests for text-to-speech prompts. The provider is a fake: no test touches
// the network or needs a real key (the key below is built at runtime).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/tts"
)

type fakeTTS struct {
	mu       sync.Mutex
	calls    int
	lastKey  string
	lastText string
	err      error
	voiceErr error
	checkErr error
	seconds  int
}

func (f *fakeTTS) Synthesize(_ context.Context, key, _, _, text string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastKey, f.lastText = key, text
	if f.err != nil {
		return nil, f.err
	}
	n := f.seconds
	if n == 0 {
		n = 1
	}
	pcm := bytes.Repeat([]byte{byte(f.calls), 1}, 8000*n)
	return media.EncodeWAV(pcm, 8000), nil
}

func (f *fakeTTS) CheckKey(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastKey = key
	return f.checkErr
}

func (f *fakeTTS) Voices(context.Context, string) ([]tts.Model, error) {
	f.mu.Lock()
	err := f.voiceErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return []tts.Model{{ID: "elevenlabs/eleven-v3", Name: "v3", Voices: []string{"sarah", "george"}}}, nil
}

func (f *fakeTTS) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// mpMust sends a multipart/form-data body (boundary BND) and fails unless it
// answers want.
func mpMust(c *client, want int, method, path, body string) response {
	c.e.t.Helper()
	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	defer c.header.Del("Content-Type")
	return c.must(want, method, path, body)
}

func newTTSEnv(t *testing.T) (*env, *fakeTTS, *memObjects, *client, string) {
	t.Helper()
	objs := newMemObjects()
	fake := &fakeTTS{}
	e := newEnvConfig(t, Config{Objects: objs, TTS: fake}, testBox(t))
	c := e.login()
	key := trunkPassword("tts")
	return e, fake, objs, c, key
}

// TestTTSSettingsKeyWriteOnly fails if the OpenRouter key is ever returned,
// stored in the clear or audited, or if the settings are not admin-only.
func TestTTSSettingsKeyWriteOnly(t *testing.T) {
	e, fake, _, c, key := newTTSEnv(t)
	ctx := context.Background()

	s := c.must(http.StatusOK, "GET", "/api/v1/tts/settings", nil)
	if j := s.json(t); j["configured"] != false || j["provider"] != "openrouter" {
		t.Fatalf("fresh settings = %v", j)
	}
	// Without a key a test and a generation are refused with a clear reason.
	c.must(http.StatusConflict, "POST", "/api/v1/tts/settings/test", nil)

	put := c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{
		"apiKey": key, "model": "elevenlabs/eleven-v3", "voice": "sarah"})
	j := put.json(t)
	if j["configured"] != true || j["model"] != "elevenlabs/eleven-v3" || j["voice"] != "sarah" ||
		j["keyHint"] != key[len(key)-4:] {
		t.Fatalf("put = %v", j)
	}
	for name, body := range map[string][]byte{
		"put": put.body, "get": c.must(http.StatusOK, "GET", "/api/v1/tts/settings", nil).body,
	} {
		if strings.Contains(string(body), key) {
			t.Fatalf("%s response carries the key", name)
		}
	}
	// Stored sealed.
	var enc []byte
	if err := e.db.QueryRowContext(ctx, `SELECT api_key_enc FROM tts_settings WHERE id = 1`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 || bytes.Contains(enc, []byte(key)) {
		t.Fatalf("stored key is not sealed: %d bytes", len(enc))
	}
	if got, err := e.st.TTSKey(ctx); err != nil || got != key {
		t.Fatalf("TTSKey = %v", err)
	}
	// Changing only the voice keeps the key.
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"voice": "george"})
	if got, _ := e.st.TTSKey(ctx); got != key {
		t.Fatal("a voice change dropped the key")
	}
	// Test connection uses the stored key.
	c.must(http.StatusOK, "POST", "/api/v1/tts/settings/test", nil)
	if fake.lastKey != key {
		t.Fatal("test connection did not use the stored key")
	}
	fake.checkErr = tts.ErrUnauthorized
	r := c.must(http.StatusBadGateway, "POST", "/api/v1/tts/settings/test", nil)
	if !strings.Contains(string(r.body), "401") || strings.Contains(string(r.body), key) {
		t.Fatalf("unauthorised test = %s", r.body)
	}
	// Bad input.
	c.must(http.StatusBadRequest, "PUT", "/api/v1/tts/settings", map[string]string{"apiKey": "has space"})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/tts/settings", map[string]string{})
	// The key is nowhere in the audit trail or the sessions' logs.
	var n int
	if err := e.db.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_events WHERE actor LIKE '%'||$1||'%' OR resource_id LIKE '%'||$1||'%'`, key).Scan(&n); err != nil || n != 0 {
		t.Fatalf("key in audit trail: %d %v", n, err)
	}

	// RBAC: settings are admin-only; the voice list needs an operator.
	op, _ := e.asRole("olive", auth.RoleOperator)
	vw, _ := e.asRole("vic", auth.RoleViewer)
	for _, rq := range [][2]string{{"GET", "/api/v1/tts/settings"}, {"PUT", "/api/v1/tts/settings"},
		{"DELETE", "/api/v1/tts/settings/key"}, {"POST", "/api/v1/tts/settings/test"}} {
		op.must(http.StatusForbidden, rq[0], rq[1], nil)
		vw.must(http.StatusForbidden, rq[0], rq[1], nil)
	}
	op.must(http.StatusOK, "GET", "/api/v1/tts/voices", nil)
	vw.must(http.StatusForbidden, "GET", "/api/v1/tts/voices", nil)

	// Removing the key.
	if j := c.must(http.StatusOK, "DELETE", "/api/v1/tts/settings/key", nil).json(t); j["configured"] != false || j["keyHint"] != "" {
		t.Fatalf("after delete = %v", j)
	}
	if got, _ := e.st.TTSKey(ctx); got != "" {
		t.Fatal("key survived delete")
	}
}

// TestGenerateAnnouncementOnce fails if a generation repeats for unchanged
// text, if playback calls the provider, or if the edited marker is wrong.
func TestGenerateAnnouncementOnce(t *testing.T) {
	e, fake, objs, c, key := newTTSEnv(t)
	body := map[string]any{"name": "welcome", "text": "Welcome to Acme."}

	// No key yet: refused, nothing stored.
	c.must(http.StatusConflict, "POST", "/api/v1/announcements/generate", body)
	if fake.count() != 0 || objs.has("ann:ann/welcome.wav") {
		t.Fatal("generated without a key")
	}
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"apiKey": key, "voice": "sarah"})

	res := c.must(http.StatusCreated, "POST", "/api/v1/announcements/generate", body).json(t)
	ann := res["announcement"].(map[string]any)
	if res["regenerated"] != true || ann["generated"] != true || ann["textEdited"] != false ||
		ann["sourceText"] != "Welcome to Acme." || ann["voiceId"] != "sarah" || ann["generatedAt"] == nil {
		t.Fatalf("created = %v", res)
	}
	if fake.count() != 1 || fake.lastKey != key || !objs.has("ann:ann/welcome.wav") {
		t.Fatalf("calls=%d stored=%v", fake.count(), objs.has("ann:ann/welcome.wav"))
	}
	// The stored audio is a playable 8 kHz WAV.
	if _, rate, err := media.DecodeWAV(objs.objs["ann:ann/welcome.wav"]); err != nil || rate != 8000 {
		t.Fatalf("stored audio rate=%d err=%v", rate, err)
	}
	// A duplicate name is a conflict and costs nothing.
	c.must(http.StatusConflict, "POST", "/api/v1/announcements/generate", body)
	if fake.count() != 1 {
		t.Fatal("a conflicting name called the provider")
	}

	id := fmt.Sprint(ann["id"])
	gen := "/api/v1/announcements/" + id + "/generate"
	// Playback and listing never call the provider.
	c.must(http.StatusOK, "GET", "/api/v1/announcements/"+id+"/audio", nil)
	c.must(http.StatusOK, "GET", "/api/v1/announcements", nil)
	// Same text again: idempotent.
	r := c.must(http.StatusOK, "POST", gen, map[string]any{}).json(t)
	if r["regenerated"] != false || fake.count() != 1 {
		t.Fatalf("repeat generated: %v calls=%d", r, fake.count())
	}
	r = c.must(http.StatusOK, "POST", gen, map[string]any{"text": "Welcome to Acme."}).json(t)
	if r["regenerated"] != false || fake.count() != 1 {
		t.Fatalf("repeat with text generated: %v", r)
	}

	// Editing the text marks it edited without generating.
	a := c.must(http.StatusOK, "PATCH", "/api/v1/announcements/"+id, map[string]any{"text": "Welcome to Acme Corp."}).json(t)
	if a["textEdited"] != true || a["generated"] != true || fake.count() != 1 {
		t.Fatalf("after edit = %v calls=%d", a, fake.count())
	}
	// Regenerate on the explicit click.
	r = c.must(http.StatusOK, "POST", gen, map[string]any{}).json(t)
	a = r["announcement"].(map[string]any)
	if r["regenerated"] != true || a["textEdited"] != false || fake.count() != 2 || fake.lastText != "Welcome to Acme Corp." {
		t.Fatalf("regenerate = %v calls=%d", r, fake.count())
	}
	// Force repeats the same request.
	c.must(http.StatusOK, "POST", gen, map[string]any{"force": true})
	if fake.count() != 3 {
		t.Fatalf("force calls = %d", fake.count())
	}

	// A provider failure is a clear 502, audited without the text, and the
	// stored audio is untouched.
	before := string(objs.objs["ann:ann/welcome.wav"])
	fake.err = tts.ErrQuota
	r2 := c.must(http.StatusBadGateway, "POST", gen, map[string]any{"text": "Something else", "force": true})
	if !strings.Contains(string(r2.body), "credits") || string(objs.objs["ann:ann/welcome.wav"]) != before {
		t.Fatalf("failure = %s", r2.body)
	}
	fake.err = nil
	var failed int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'generate_failed' AND resource = 'announcement'`).Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("failed audits = %d %v", failed, err)
	}
	var audited int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'generate' AND resource = 'announcement'`).Scan(&audited); err != nil || audited != 3 {
		t.Fatalf("generate audits = %d %v", audited, err)
	}

	// Text bounds.
	c.must(http.StatusBadRequest, "POST", gen, map[string]any{"text": strings.Repeat("a", tts.MaxTextChars+1)})
	c.must(http.StatusBadRequest, "POST", "/api/v1/announcements/generate", map[string]any{"name": "x", "text": "   "})
	c.must(http.StatusBadRequest, "POST", "/api/v1/announcements/generate", map[string]any{"name": "bad name", "text": "hi"})
	c.must(http.StatusNotFound, "POST", "/api/v1/announcements/999999/generate", map[string]any{"text": "hi"})

	// Uploading audio over a generated prompt clears its generation record.
	up := mpMust(c, http.StatusOK, "PUT", "/api/v1/announcements/"+id, annMultipart("", wav)).json(t)
	if up["generated"] != false || up["generatedAt"] != nil {
		t.Fatalf("after upload = %v", up)
	}
	// An operator may generate; a viewer may not.
	vw, _ := e.asRole("vic", auth.RoleViewer)
	vw.must(http.StatusForbidden, "POST", gen, map[string]any{})
}

// TestGenerateKeepsManualUpload fails if a hand-uploaded announcement is not
// listed as not generated or loses its audio to a draft.
func TestGenerateKeepsManualUpload(t *testing.T) {
	_, fake, objs, c, _ := newTTSEnv(t)
	a := mpMust(c, http.StatusCreated, "POST", "/api/v1/announcements", annMultipart("closing", wav)).json(t)
	if a["generated"] != false || a["textEdited"] != false || a["sourceText"] != "" {
		t.Fatalf("upload = %v", a)
	}
	before := string(objs.objs["ann:ann/closing.wav"])
	c.must(http.StatusOK, "PATCH", fmt.Sprintf("/api/v1/announcements/%v", a["id"]), map[string]any{"text": "We are closed."})
	if string(objs.objs["ann:ann/closing.wav"]) != before || fake.count() != 0 {
		t.Fatal("saving a draft touched the audio or the provider")
	}
	// Generating without a key says so.
	c.must(http.StatusConflict, "POST", fmt.Sprintf("/api/v1/announcements/%v/generate", a["id"]), map[string]any{})
}

// TestUploadResampledTo8k fails if a 24 kHz upload is stored at its own rate
// or a stereo or empty file is accepted.
func TestUploadResampledTo8k(t *testing.T) {
	_, _, objs, c, _ := newTTSEnv(t)
	w24 := media.EncodeWAV(bytes.Repeat([]byte{0, 1}, 24000), 24000)
	mpMust(c, http.StatusCreated, "POST", "/api/v1/announcements", annMultipart("hi", w24))
	pcm, rate, err := media.DecodeWAV(objs.objs["ann:ann/hi.wav"])
	if err != nil || rate != 8000 || len(pcm)/2 != 8000 {
		t.Fatalf("stored rate=%d samples=%d err=%v", rate, len(pcm)/2, err)
	}
	stereo := media.EncodeWAV(make([]byte, 400), 8000)
	stereo[22] = 2 // channels
	r := mpMust(c, http.StatusBadRequest, "POST", "/api/v1/announcements", annMultipart("st", stereo))
	if !strings.Contains(string(r.body), "16-bit mono") {
		t.Fatalf("stereo refusal = %s", r.body)
	}
	mpMust(c, http.StatusBadRequest, "POST", "/api/v1/announcements", annMultipart("empty", media.EncodeWAV(nil, 8000)))
}

// TestVoicemailGreetingFromText fails if a greeting from text generates twice
// for the same text, if an announcement cannot be picked, or if an upload does
// not clear the generation record.
func TestVoicemailGreetingFromText(t *testing.T) {
	objs := newMemObjects()
	fake := &fakeTTS{}
	e := newEnvConfig(t, Config{Objects: objs, TTS: fake}, testBox(t))
	c := e.login()
	extID := func() any {
		return c.must(http.StatusCreated, "POST", "/api/v1/extensions",
			map[string]string{"number": "101", "name": "Sales"}).json(t)["id"]
	}()
	path := fmt.Sprintf("/api/v1/extensions/%v/voicemail/greeting", extID)
	key := trunkPassword("tts")
	c.must(http.StatusConflict, "PUT", path, map[string]any{"slot": "greeting", "source": "text", "text": "Leave a message."})
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"apiKey": key})

	res := c.must(http.StatusOK, "PUT", path, map[string]any{"slot": "greeting", "source": "text", "text": "Leave a message."}).json(t)
	box := res["box"].(map[string]any)
	obj, _ := box["greetingObject"].(string)
	if res["regenerated"] != true || obj == "" || box["greetingTts"] == nil || fake.count() != 1 || !objs.has(obj) {
		t.Fatalf("generated greeting = %v calls=%d", res, fake.count())
	}
	// Same text: nothing generated, same object.
	res = c.must(http.StatusOK, "PUT", path, map[string]any{"slot": "greeting", "source": "text", "text": "Leave a message."}).json(t)
	if res["regenerated"] != false || fake.count() != 1 || res["box"].(map[string]any)["greetingObject"] != obj {
		t.Fatalf("repeat = %v calls=%d", res, fake.count())
	}
	// The unreachable slot is independent.
	res = c.must(http.StatusOK, "PUT", path, map[string]any{"slot": "unreachable", "source": "text", "text": "Not available."}).json(t)
	if fake.count() != 2 || res["box"].(map[string]any)["unreachableObject"] == "" || res["box"].(map[string]any)["greetingObject"] != obj {
		t.Fatalf("unreachable = %v", res)
	}

	// Pick an existing announcement.
	ann := mpMust(c, http.StatusCreated, "POST", "/api/v1/announcements", annMultipart("hours", wav)).json(t)
	res = c.must(http.StatusOK, "PUT", path, map[string]any{"slot": "greeting", "source": "announcement", "announcementId": ann["id"]}).json(t)
	box = res["box"].(map[string]any)
	newObj, _ := box["greetingObject"].(string)
	if newObj == "" || newObj == obj || box["greetingTts"] != nil || fake.count() != 2 {
		t.Fatalf("picked = %v calls=%d", res, fake.count())
	}
	if !bytes.Equal(objs.objs[newObj], wav) || objs.has(obj) {
		t.Fatal("the picked audio was not copied or the old greeting was kept")
	}
	// Bad requests.
	c.must(http.StatusBadRequest, "PUT", path, map[string]any{"slot": "x", "source": "text"})
	c.must(http.StatusNotFound, "PUT", path, map[string]any{"slot": "greeting", "source": "announcement", "announcementId": 999999})
	c.must(http.StatusNotFound, "PUT", "/api/v1/extensions/999999/voicemail/greeting", map[string]any{"slot": "greeting", "source": "text", "text": "x"})

	// An uploaded greeting clears the generation record.
	body := "--BND\r\nContent-Disposition: form-data; name=\"unreachable\"; filename=\"g.wav\"\r\n" +
		"Content-Type: audio/wav\r\n\r\n" + string(wav) + "\r\n--BND--\r\n"
	got := mpMust(c, http.StatusOK, "PUT", fmt.Sprintf("/api/v1/extensions/%v/voicemail", extID), body).json(t)
	if got["unreachableTts"] != nil {
		t.Fatalf("upload kept the generation record: %v", got["unreachableTts"])
	}
}

// TestGenerateParkAnnouncement fails if the park slot clips are generated
// without a key, are not all stored as playable announcements, are
// generated again when nothing changed, overwrite an uploaded recording
// without force, or can be generated by a viewer.
func TestGenerateParkAnnouncement(t *testing.T) {
	e, fake, objs, c, key := newTTSEnv(t)
	const path = "/api/v1/park-lots/announcement/generate"

	c.must(http.StatusConflict, "POST", path, map[string]any{})
	if fake.count() != 0 {
		t.Fatal("generated without a key")
	}
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"apiKey": key, "voice": "sarah"})

	// An operator's own recording of one digit is kept.
	if _, err := e.st.CreateAnnouncement(context.Background(), "test", "park.digit.4", "ann/park.digit.4.wav", nil); err != nil {
		t.Fatal(err)
	}
	res := c.must(http.StatusOK, "POST", path, map[string]any{}).json(t)
	if res["generated"] != float64(10) || len(res["announcements"].([]any)) != 11 || fake.count() != 10 {
		t.Fatalf("first generation = %v (provider calls %d)", res, fake.count())
	}
	for _, name := range []string{"park.slot", "park.digit.0", "park.digit.9"} {
		raw, ok := objs.objs["ann:ann/"+name+".wav"]
		if !ok {
			t.Fatalf("%s not stored", name)
		}
		if _, rate, err := media.DecodeWAV(raw); err != nil || rate != 8000 {
			t.Fatalf("%s: rate=%d err=%v", name, rate, err)
		}
		if a, ok, err := e.st.AnnouncementByName(context.Background(), name); err != nil || !ok || !a.Generated {
			t.Fatalf("%s announcement = %+v %v %v", name, a, ok, err)
		}
	}
	if objs.has("ann:ann/park.digit.4.wav") {
		t.Fatal("the uploaded recording of digit 4 was overwritten")
	}
	// Nothing changed: no provider call.
	if res := c.must(http.StatusOK, "POST", path, nil).json(t); res["generated"] != float64(0) || fake.count() != 10 {
		t.Fatalf("repeat = %v (provider calls %d)", res, fake.count())
	}
	// Force regenerates every clip.
	if res := c.must(http.StatusOK, "POST", path, map[string]any{"force": true}).json(t); res["generated"] != float64(11) {
		t.Fatalf("forced = %v", res)
	}
	vw, _ := e.asRole("vic", auth.RoleViewer)
	vw.must(http.StatusForbidden, "POST", path, nil)
}

// TestTTSUpstreamFailures fails if a provider failure is not a 502 on every
// route that calls it (new announcement, voice list, greeting from text), or
// if saving a prompt for a missing announcement is not a 404.
func TestTTSUpstreamFailures(t *testing.T) {
	_, fake, _, c, key := newTTSEnv(t)
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"apiKey": key, "voice": "sarah"})
	ext := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	fake.mu.Lock()
	fake.err = errors.New("provider down")
	fake.voiceErr = errors.New("provider down")
	fake.mu.Unlock()

	c.must(http.StatusBadGateway, "POST", "/api/v1/announcements/generate", map[string]any{"name": "welcome", "text": "Welcome."})
	c.must(http.StatusBadGateway, "GET", "/api/v1/tts/voices", nil)
	c.must(http.StatusBadGateway, "PUT", fmt.Sprintf("/api/v1/extensions/%v/voicemail/greeting", ext["id"]),
		map[string]any{"slot": "greeting", "source": "text", "text": "Leave a message."})
	c.must(http.StatusNotFound, "PATCH", "/api/v1/announcements/999999", map[string]any{"text": "Hello."})
}
