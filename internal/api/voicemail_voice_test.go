package api

// Tests for the voicemail voice in the console: previewing the default
// spoken greeting, reverting greetings to it, uploading the busy greeting
// and the recorded name, and pre-generating the static prompts on a settings
// save. The provider is a fake: no test touches the network or needs a real
// key (the key is built at runtime).

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/ttscache"
	"github.com/azrtydxb/hello/internal/voiceprompt"
)

func newExtension(t *testing.T, c *client, number, name string) any {
	t.Helper()
	return c.must(http.StatusCreated, "POST", "/api/v1/extensions",
		map[string]string{"number": number, "name": name}).json(t)["id"]
}

// TestVoicemailGreetingPreview fails if the preview does not speak the default
// greeting of the box in the configured voice, if the same text is generated
// twice, if a changed voice reuses old audio, or if a missing key is not a
// clear 409.
func TestVoicemailGreetingPreview(t *testing.T) {
	e, fake, objs, c, key := newTTSEnv(t)
	_ = e
	extID := newExtension(t, c, "101", "Sales")
	path := fmt.Sprintf("/api/v1/extensions/%v/voicemail/greeting/preview", extID)

	// No key yet.
	r := c.must(http.StatusConflict, "POST", path, nil)
	if strings.Contains(string(r.body), key) {
		t.Fatal("the error carries the key")
	}
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{
		"apiKey": key, "model": "elevenlabs/eleven-v3", "voice": "sarah"})

	// No body: the general greeting.
	got := c.must(http.StatusOK, "POST", path, nil)
	if ct := got.header.Get("Content-Type"); ct != "audio/wav" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if !strings.HasPrefix(string(got.body), "RIFF") {
		t.Fatalf("not a WAV: %q", got.body[:8])
	}
	want := "Sales is not available. Please leave a message after the tone."
	if fake.lastText != want || fake.count() != 1 {
		t.Fatalf("spoke %q (%d calls), want %q once", fake.lastText, fake.count(), want)
	}
	// It landed in the shared cache under the hash key calls look up.
	if !objs.has(ttscache.Key("elevenlabs/eleven-v3", "sarah", want)) {
		t.Fatal("the preview was not stored under tts/<hash>.wav")
	}
	// Again: no new generation.
	c.must(http.StatusOK, "POST", path, map[string]string{"slot": "greeting"})
	if fake.count() != 1 {
		t.Fatalf("a repeated preview generated again (%d)", fake.count())
	}
	// The other variants.
	c.must(http.StatusOK, "POST", path, map[string]string{"slot": "busy"})
	if fake.lastText != "Sales is busy at the moment. Please leave a message after the tone." {
		t.Fatalf("busy text = %q", fake.lastText)
	}
	c.must(http.StatusOK, "POST", path, map[string]string{"slot": "unreachable"})
	if fake.lastText != "Sales cannot be reached at the moment. Please leave a message after the tone." {
		t.Fatalf("unreachable text = %q", fake.lastText)
	}
	// A new voice is a new key: generated again at once.
	before := fake.count()
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"voice": "george"})
	c.must(http.StatusOK, "POST", path, nil)
	if fake.count() != before+1 {
		t.Fatalf("a changed voice reused the old audio (%d calls, was %d)", fake.count(), before)
	}
	// Bad requests.
	c.must(http.StatusBadRequest, "POST", path, map[string]string{"slot": "nope"})
	c.must(http.StatusNotFound, "POST", "/api/v1/extensions/999999/voicemail/greeting/preview", nil)
	// A provider failure is reported, not hidden.
	other := newExtension(t, c, "102", "Support")
	fake.mu.Lock()
	fake.err = fmt.Errorf("provider down")
	fake.mu.Unlock()
	r = c.must(http.StatusBadGateway, "POST", fmt.Sprintf("/api/v1/extensions/%v/voicemail/greeting/preview", other), nil)
	if strings.Contains(string(r.body), key) {
		t.Fatal("the error carries the key")
	}
}

// TestVoicemailBoxReportsAIVoice fails if the box does not say whether empty
// greeting slots are spoken in the AI voice.
func TestVoicemailBoxReportsAIVoice(t *testing.T) {
	_, _, _, c, key := newTTSEnv(t)
	extID := newExtension(t, c, "101", "Sales")
	path := fmt.Sprintf("/api/v1/extensions/%v/voicemail", extID)
	c.must(http.StatusOK, "PUT", path, map[string]string{"email": ""})
	box := c.must(http.StatusOK, "GET", path, nil).json(t)
	if box["aiVoice"] != false || box["greetingObject"] != "" || box["busyObject"] != "" || box["nameObject"] != "" {
		t.Fatalf("fresh box = %v", box)
	}
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"apiKey": key})
	if box = c.must(http.StatusOK, "GET", path, nil).json(t); box["aiVoice"] != true {
		t.Fatalf("with a key = %v", box)
	}
	c.must(http.StatusOK, "DELETE", "/api/v1/tts/settings/key", nil)
	if box = c.must(http.StatusOK, "GET", path, nil).json(t); box["aiVoice"] != false {
		t.Fatalf("key removed = %v", box)
	}
}

// TestVoicemailUploadBusyAndNameThenRevert fails if the busy greeting and the
// recorded name cannot be uploaded, replaced or reverted to the default, or if
// a revert leaves the old audio in storage.
func TestVoicemailUploadBusyAndNameThenRevert(t *testing.T) {
	_, _, objs, c, _ := newTTSEnv(t)
	extID := newExtension(t, c, "101", "Sales")
	path := fmt.Sprintf("/api/v1/extensions/%v/voicemail", extID)
	part := func(name string) string {
		return "--BND\r\nContent-Disposition: form-data; name=\"" + name + "\"; filename=\"g.wav\"\r\n" +
			"Content-Type: audio/wav\r\n\r\n" + string(wav) + "\r\n"
	}
	body := part("busy") + part("name") + part("greeting") + "--BND--\r\n"
	box := mpMust(c, http.StatusOK, "PUT", path, body).json(t)
	busy, _ := box["busyObject"].(string)
	name, _ := box["nameObject"].(string)
	greet, _ := box["greetingObject"].(string)
	if busy == "" || name == "" || greet == "" || !objs.has(busy) || !objs.has(name) || !objs.has(greet) {
		t.Fatalf("box after upload = %v", box)
	}
	if !strings.Contains(busy, "busy") || !strings.Contains(name, "name") {
		t.Fatalf("object keys = %q %q", busy, name)
	}

	// Replacing the busy greeting removes the old object.
	box = mpMust(c, http.StatusOK, "PUT", path, part("busy")+"--BND--\r\n").json(t)
	busy2, _ := box["busyObject"].(string)
	if busy2 == "" || busy2 == busy || objs.has(busy) || !objs.has(busy2) {
		t.Fatalf("busy replace: %q -> %q", busy, busy2)
	}

	// Revert each slot.
	for slot, key := range map[string]string{"busy": "busyObject", "name": "nameObject", "greeting": "greetingObject"} {
		old, _ := box[key].(string)
		if slot == "name" {
			old = name
		}
		if slot == "greeting" {
			old = greet
		}
		box = c.must(http.StatusOK, "DELETE", path+"/greeting/"+slot, nil).json(t)
		if box[key] != "" {
			t.Fatalf("%s not reverted: %v", slot, box)
		}
		if objs.has(old) {
			t.Fatalf("%s: the old audio %q was kept in storage", slot, old)
		}
	}
	// Reverting what is already default is a no-op success.
	c.must(http.StatusOK, "DELETE", path+"/greeting/busy", nil)
	c.must(http.StatusBadRequest, "DELETE", path+"/greeting/bogus", nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/extensions/999999/voicemail/greeting/busy", nil)
}

// TestVoicemailBusyGreetingFromText: the generate route accepts the busy
// slot and keeps its record apart from the others.
func TestVoicemailBusyGreetingFromText(t *testing.T) {
	_, fake, _, c, key := newTTSEnv(t)
	extID := newExtension(t, c, "101", "Sales")
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"apiKey": key})
	path := fmt.Sprintf("/api/v1/extensions/%v/voicemail/greeting", extID)
	res := c.must(http.StatusOK, "PUT", path, map[string]any{"slot": "busy", "source": "text", "text": "Sales is busy."}).json(t)
	box := res["box"].(map[string]any)
	if box["busyObject"] == "" || box["busyTts"] == nil || box["greetingObject"] != "" || fake.count() != 1 {
		t.Fatalf("busy greeting = %v", box)
	}
	// Reverting it clears the generation record too.
	box = c.must(http.StatusOK, "DELETE", path+"/busy", nil).json(t)
	if box["busyObject"] != "" || box["busyTts"] != nil {
		t.Fatalf("revert left %v", box)
	}
}

// TestSettingsSavePregeneratesStaticPrompts fails if saving the TTS settings
// does not generate the static voicemail prompts in the background, stored
// under tts/<hash>.wav, or generates the same prompts twice.
func TestSettingsSavePregeneratesStaticPrompts(t *testing.T) {
	objs := newMemObjects()
	fake := &fakeTTS{}
	e := newEnvConfig(t, Config{Objects: objs, TTS: fake, PromptLanguage: "en"}, testBox(t))
	c := e.login()
	key := trunkPassword("tts")
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"apiKey": key, "model": "elevenlabs/eleven-v3", "voice": "sarah"})

	static := voiceprompt.Static(voiceprompt.English)
	deadline := time.Now().Add(20 * time.Second)
	missing := func() []string {
		var out []string
		for _, p := range static {
			if !objs.has(ttscache.Key("elevenlabs/eleven-v3", "sarah", p.Text)) {
				out = append(out, p.Text)
			}
		}
		return out
	}
	for len(missing()) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("static prompts not generated: %d missing, e.g. %q", len(missing()), missing()[0])
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Each prompt once (the texts are distinct), and no key in the objects.
	if fake.count() != len(static) {
		t.Fatalf("%d generations for %d prompts", fake.count(), len(static))
	}
	// Saving again with the same voice costs nothing.
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"voice": "sarah"})
	time.Sleep(300 * time.Millisecond)
	if fake.count() != len(static) {
		t.Fatalf("a repeat save generated again: %d", fake.count())
	}
}

// TestBoxLanguage fails if a box's prompt language cannot be read, set to Dutch
// and cleared again, if an unknown language is accepted, if the effective
// language does not follow the deployment language when the box has none, or
// if the preview of the default greeting is not spoken in the box language
// (pbx-gaps S-20).
func TestBoxLanguage(t *testing.T) {
	objs := newMemObjects()
	fake := &fakeTTS{}
	e := newEnvConfig(t, Config{Objects: objs, TTS: fake, PromptLanguage: "en"}, testBox(t))
	c := e.login()
	key := trunkPassword("tts")
	extID := newExtension(t, c, "101", "Sales")
	c.must(http.StatusOK, "PUT", fmt.Sprintf("/api/v1/extensions/%v/voicemail", extID), map[string]string{"email": ""})
	box := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/extensions/%v/voicemail", extID), nil).json(t)
	if box["promptLanguage"] != "" {
		t.Fatalf("a fresh box has language %v, want none (inherit)", box["promptLanguage"])
	}
	path := fmt.Sprintf("/api/v1/voicemail/boxes/%v/language", box["id"])

	got := c.must(http.StatusOK, "GET", path, nil).json(t)
	if got["promptLanguage"] != "" || got["effective"] != "en" {
		t.Fatalf("fresh language = %v, want inherit and English", got)
	}
	if langs, _ := got["languages"].([]any); len(langs) != 2 || langs[0] != "en" || langs[1] != "nl" {
		t.Fatalf("languages = %v, want en and nl", got["languages"])
	}

	got = c.must(http.StatusOK, "PUT", path, map[string]string{"promptLanguage": "NL"}).json(t)
	if got["promptLanguage"] != "nl" || got["effective"] != "nl" {
		t.Fatalf("after PUT nl = %v", got)
	}
	if b := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/extensions/%v/voicemail", extID), nil).json(t); b["promptLanguage"] != "nl" {
		t.Fatalf("the box view says %v", b["promptLanguage"])
	}
	// The box list carries it for the console.
	list := c.must(http.StatusOK, "GET", "/api/v1/voicemail/boxes", nil).json(t)
	if rows, _ := list["items"].([]any); len(rows) != 1 || rows[0].(map[string]any)["promptLanguage"] != "nl" {
		t.Fatalf("box list = %v", list)
	}

	// The preview is spoken in Dutch.
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"apiKey": key, "model": "elevenlabs/eleven-v3", "voice": "sarah"})
	c.must(http.StatusOK, "POST", fmt.Sprintf("/api/v1/extensions/%v/voicemail/greeting/preview", extID), nil)
	// (The prompts are also pre-generated in the background, so the fake's
	// last text is not reliable; the greeting names its owner, which no
	// pre-generation does, so its stored clip proves the preview's language.)
	if want := voiceprompt.GreetingText(voiceprompt.Dutch, voiceprompt.Unavailable, "Sales"); !objs.has(ttscache.Key("elevenlabs/eleven-v3", "sarah", want)) {
		t.Fatalf("the preview did not speak %q", want)
	}

	// Refused: an unknown language, and no value at all.
	c.must(http.StatusBadRequest, "PUT", path, map[string]string{"promptLanguage": "klingon"})
	c.must(http.StatusBadRequest, "PUT", path, map[string]string{})
	c.must(http.StatusNotFound, "GET", "/api/v1/voicemail/boxes/999999/language", nil)
	c.must(http.StatusNotFound, "PUT", "/api/v1/voicemail/boxes/999999/language", map[string]string{"promptLanguage": "nl"})

	// The operator PUT on the box takes it too, and an empty value clears it.
	c.must(http.StatusOK, "PUT", fmt.Sprintf("/api/v1/extensions/%v/voicemail", extID), map[string]string{"promptLanguage": ""})
	if got = c.must(http.StatusOK, "GET", path, nil).json(t); got["promptLanguage"] != "" || got["effective"] != "en" {
		t.Fatalf("after clearing = %v", got)
	}
}

// TestBoxLanguagePregeneratesItsPrompts fails if choosing Dutch for a box does
// not generate the Dutch static prompts in the background while the deployment
// language stays English, so the first Dutch caller does not wait for the
// provider (S-21).
func TestBoxLanguagePregeneratesItsPrompts(t *testing.T) {
	objs := newMemObjects()
	fake := &fakeTTS{}
	e := newEnvConfig(t, Config{Objects: objs, TTS: fake, PromptLanguage: "en"}, testBox(t))
	c := e.login()
	key := trunkPassword("tts")
	extID := newExtension(t, c, "101", "Sales")
	c.must(http.StatusOK, "PUT", "/api/v1/tts/settings", map[string]string{"apiKey": key, "model": "elevenlabs/eleven-v3", "voice": "sarah"})
	c.must(http.StatusOK, "PUT", fmt.Sprintf("/api/v1/extensions/%v/voicemail", extID), map[string]string{"promptLanguage": "nl"})

	missing := func(l voiceprompt.Lang) []string {
		var out []string
		for _, p := range voiceprompt.Static(l) {
			if !objs.has(ttscache.Key("elevenlabs/eleven-v3", "sarah", p.Text)) {
				out = append(out, p.Text)
			}
		}
		return out
	}
	deadline := time.Now().Add(20 * time.Second)
	for len(missing(voiceprompt.Dutch)) > 0 || len(missing(voiceprompt.English)) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("prompts not generated: %d Dutch and %d English missing", len(missing(voiceprompt.Dutch)), len(missing(voiceprompt.English)))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
