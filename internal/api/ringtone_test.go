package api

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// toneBytes is a WAV of n samples of silence in the given format.
func toneBytes(tag, ch uint16, rate uint32, bits uint16, n int) []byte {
	var b bytes.Buffer
	data := make([]byte, n*int(bits/8)*int(max(ch, 1)))
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), tag, ch, rate, rate * uint32(ch) * uint32(bits/8), ch * bits / 8, bits} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// TestRingtoneLibrary walks the tone library against PostgreSQL: upload,
// list, the format limits (anything outside PCM, 8 or 16 kHz, mono, 16-bit,
// 30 s and 512 KiB is 400), a taken name (409), a MinIO outage (502), the
// ring map choosing a custom tone (only on a vendor that is provisioned one,
// only a tone in the library), and the guard on a tone the map uses. It fails
// if a WAV outside the limits is accepted, if a tone is stored under another
// bucket key than name/hash, if the ring map accepts a tone that does not
// exist or a vendor that gets none, or if a tone in use can be deleted.
func TestRingtoneLibrary(t *testing.T) {
	objs := &flakyObjects{memObjects: newMemObjects()}
	e := newPBXEnv(t, objs)
	c := e.login()
	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	defer c.header.Del("Content-Type")

	good := toneBytes(1, 1, 8000, 16, 8000)
	made := c.must(http.StatusCreated, "POST", "/api/v1/ringtones", annMultipart("chime", good)).json(t)
	if made["name"] != "chime" || made["sampleRate"] != float64(8000) || made["durationMs"] != float64(1000) || made["usedBy"] != float64(0) {
		t.Fatalf("created = %v", made)
	}
	if !objs.has("ring:chime/" + made["sha256"].(string) + ".wav") {
		t.Fatalf("the audio is not stored under name/hash: %v", objs.objs)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/ringtones", annMultipart("chime", good))
	c.must(http.StatusCreated, "POST", "/api/v1/ringtones", annMultipart("wide", toneBytes(1, 1, 16000, 16, 16000*5)))

	for name, body := range map[string]string{
		"stereo":     annMultipart("t1", toneBytes(1, 2, 8000, 16, 800)),
		"44.1 kHz":   annMultipart("t2", toneBytes(1, 1, 44100, 16, 800)),
		"8-bit":      annMultipart("t3", toneBytes(1, 1, 8000, 8, 800)),
		"mu-law":     annMultipart("t4", toneBytes(7, 1, 8000, 8, 800)),
		"over 30 s":  annMultipart("t5", toneBytes(1, 1, 8000, 16, 8000*31)),
		"over 512 K": annMultipart("t6", toneBytes(1, 1, 16000, 16, 16000*17)),
		"not a wav":  annMultipart("t7", []byte("RIFF nope")),
		"empty":      annMultipart("t8", nil),
		"bad name":   annMultipart("Not Valid", good),
		"no name":    annMultipart("", good),
		"long name":  annMultipart(strings.Repeat("a", 33), good),
	} {
		if r := c.do("POST", "/api/v1/ringtones", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	c.header.Set("Content-Type", "application/json")
	c.must(http.StatusBadRequest, "POST", "/api/v1/ringtones", map[string]string{"name": "x"})
	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")

	list := c.must(http.StatusOK, "GET", "/api/v1/ringtones", nil).json(t)
	if its := list["items"].([]any); len(its) != 2 {
		t.Fatalf("list = %v, want the two accepted tones only", list)
	}

	// The ring map: a vendor that is provisioned a custom tone, one that is not, a missing tone.
	view := c.must(http.StatusOK, "GET", "/api/v1/ring-map", nil).json(t)
	for _, v := range view["vendors"].([]any) {
		m := v.(map[string]any)
		want := m["vendor"] == "yealink" || m["vendor"] == "snom"
		if m["customTones"] != want {
			t.Errorf("%v customTones = %v", m["vendor"], m["customTones"])
		}
	}
	c.header.Set("Content-Type", "application/json")
	put := func(code int, entries ...map[string]any) response {
		return c.must(code, "PUT", "/api/v1/ring-map", map[string]any{"entries": entries})
	}
	put(http.StatusBadRequest, map[string]any{"tag": "doorbell", "vendor": "poly", "tone": "custom:chime"})
	put(http.StatusBadRequest, map[string]any{"tag": "doorbell", "vendor": "grandstream", "tone": "custom:chime"})
	put(http.StatusBadRequest, map[string]any{"tag": "doorbell", "vendor": "yealink", "tone": "custom:ghost"})
	put(http.StatusBadRequest, map[string]any{"tag": "doorbell", "vendor": "yealink", "tone": "custom:Bad Name"})
	got := put(http.StatusOK, map[string]any{"tag": "doorbell", "vendor": "yealink", "tone": "custom:chime"},
		map[string]any{"tag": "external", "vendor": "snom", "tone": "custom:wide"}).json(t)
	if g := got["global"].([]any); len(g) != 2 || g[0].(map[string]any)["tone"] != "custom:chime" {
		t.Fatalf("global = %v", got)
	}
	if again := c.must(http.StatusOK, "GET", "/api/v1/ring-map", nil).json(t); len(again["global"].([]any)) != 2 {
		t.Fatalf("map = %v", again)
	}
	grp := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Lobby"}).json(t)
	c.must(http.StatusOK, "PUT", fmt.Sprintf("/api/v1/ring-map/groups/%v", grp["id"]),
		map[string]any{"entries": []map[string]any{{"tag": "alarm", "vendor": "yealink", "tone": "custom:chime"}}})

	// A tone in use cannot be deleted; once unused it can, audio included.
	list = c.must(http.StatusOK, "GET", "/api/v1/ringtones", nil).json(t)
	var chimeID, wideID any
	for _, it := range list["items"].([]any) {
		m := it.(map[string]any)
		switch m["name"] {
		case "chime":
			chimeID = m["id"]
			if m["usedBy"] != float64(2) {
				t.Errorf("chime usedBy = %v, want 2", m["usedBy"])
			}
		case "wide":
			wideID = m["id"]
		}
	}
	c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/ringtones/%v", chimeID), nil)
	c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/ringtones/%v", wideID), nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/ringtones/999999", nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/ringtones/abc", nil)
	put(http.StatusOK)
	c.must(http.StatusOK, "PUT", fmt.Sprintf("/api/v1/ring-map/groups/%v", grp["id"]), map[string]any{"entries": []any{}})
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/ringtones/%v", chimeID), nil)
	if objs.has("ring:chime/" + made["sha256"].(string) + ".wav") {
		t.Error("the deleted tone's audio is still stored")
	}

	// The name is free again; a MinIO outage is 502 and leaves no row.
	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	c.must(http.StatusCreated, "POST", "/api/v1/ringtones", annMultipart("chime", good))
	objs.broken.Store(true)
	c.must(http.StatusBadGateway, "POST", "/api/v1/ringtones", annMultipart("fresh", good))
	objs.broken.Store(false)
	c.header.Set("Content-Type", "application/json")
	if list := c.must(http.StatusOK, "GET", "/api/v1/ringtones", nil).json(t); len(list["items"].([]any)) != 2 {
		t.Fatalf("a refused upload left a row: %v", list)
	}
}
