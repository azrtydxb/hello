package chat

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging/media"
	"github.com/azrtydxb/hello/internal/store"
)

var testPNG = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{1, 2, 3}, 40)...)

// form builds a multipart attachment body.
func form(t *testing.T, name, ctype string, data []byte, fields map[string]string) (string, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if data != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
		h.Set("Content-Type", ctype)
		p, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = p.Write(data)
	}
	_ = mw.Close()
	return b.String(), mw.FormDataContentType()
}

func (g *rig) attach(tok, name, ctype string, data []byte, fields map[string]string) resp {
	g.t.Helper()
	body, ct := form(g.t, name, ctype, data, fields)
	return g.do("POST", "/chat/v1/attachments?site="+siteKey, body, func(r *http.Request) {
		r.Header.Set("Content-Type", ct)
		r.Header.Set("Authorization", "Bearer "+tok)
	})
}

// TestChatAttachments fails if a site that did not turn attachments on takes
// a file, a file the chat allowlist refuses (SVG, a polyglot, an MP4, one
// over 5 MiB) is stored, the sixth file in an hour is taken, a visitor reads
// another visitor's file or one without the nosniff header, or the logo and
// the frame's policy do not let the widget show images.
func TestChatAttachments(t *testing.T) {
	objs := &media.MemObjects{}
	g := newRig(t, func(s *store.ChatSite) { s.Attachments = true }, func(c *Config) { c.Objects = objs })
	tok := g.token()

	cfg := g.do("GET", "/chat/v1/challenge?site="+siteKey, nil, nil).json()["config"].(map[string]any)
	if cfg["attachments"] != true || cfg["maxAttachmentBytes"].(float64) != media.ChatMax || cfg["logo"] != "" {
		t.Fatalf("config = %v", cfg)
	}

	r := g.attach(tok, "photo.png", "image/png", testPNG, map[string]string{"clientMsgId": "a1", "text": "my screen"})
	if r.Code != http.StatusCreated {
		t.Fatalf("png: %d %s", r.Code, r.Body)
	}
	sent := g.fake.sent[len(g.fake.sent)-1]
	if len(sent.MediaIDs) != 1 || sent.Text != "my screen" || sent.ClientMsgID != "a1" || !g.fake.ready[sent.MediaIDs[0]] || objs.Len() != 1 {
		t.Fatalf("sent %+v; ready %v; %d objects", sent, g.fake.ready, objs.Len())
	}
	for _, c := range []struct {
		name, ctype string
		data        []byte
		code        int
	}{
		{"x.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 400},
		{"x.gif", "image/gif", []byte("GIF89a<script>alert(1)</script>"), 400},
		{"x.mp4", "video/mp4", append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 32)...), 400},
		{"x.png", "application/pdf", testPNG, 400},
	} {
		if r := g.attach(tok, c.name, c.ctype, c.data, map[string]string{"clientMsgId": "b-" + c.name}); r.Code != c.code {
			t.Fatalf("%s: %d %s", c.name, r.Code, r.Body)
		}
	}
	if objs.Len() != 1 {
		t.Fatalf("a refused file was stored: %d objects", objs.Len())
	}
	// The hourly limit of 5 (one taken above, four refused but counted).
	if r := g.attach(tok, "y.png", "image/png", testPNG, map[string]string{"clientMsgId": "c1"}); r.Code != http.StatusTooManyRequests {
		t.Fatalf("the sixth file in an hour: %d", r.Code)
	}

	t.Run("size and form", func(t *testing.T) {
		g := newRig(t, func(s *store.ChatSite) { s.Attachments = true }, func(c *Config) { c.Objects = &media.MemObjects{} })
		tok := g.token()
		big := append(append([]byte{}, testPNG...), make([]byte, media.ChatMax)...)
		if r := g.attach(tok, "big.png", "image/png", big, map[string]string{"clientMsgId": "x"}); r.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("over 5 MiB: %d", r.Code)
		}
		if r := g.attach(tok, "a.png", "image/png", testPNG, nil); r.Code != http.StatusBadRequest {
			t.Fatalf("no clientMsgId: %d", r.Code)
		}
		if r := g.attach(tok, "", "", nil, map[string]string{"clientMsgId": "y"}); r.Code != http.StatusBadRequest {
			t.Fatalf("no file: %d", r.Code)
		}
		if r := g.do("POST", "/chat/v1/attachments?site="+siteKey, `{}`, auth(tok)); r.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("JSON: %d", r.Code)
		}
		if r := g.attach("nobody-aaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "a.png", "image/png", testPNG, map[string]string{"clientMsgId": "z"}); r.Code != http.StatusUnauthorized {
			t.Fatalf("no visitor: %d", r.Code)
		}
	})

	t.Run("off", func(t *testing.T) {
		for name, rg := range map[string]*rig{
			"site setting": newRig(t, nil, func(c *Config) { c.Objects = &media.MemObjects{} }),
			"no store":     newRig(t, func(s *store.ChatSite) { s.Attachments = true }, nil),
		} {
			tok := rg.token()
			if r := rg.attach(tok, "a.png", "image/png", testPNG, map[string]string{"clientMsgId": "a"}); r.Code != http.StatusNotFound {
				t.Fatalf("%s: %d", name, r.Code)
			}
			if c := rg.do("GET", "/chat/v1/challenge?site="+siteKey, nil, nil).json()["config"].(map[string]any); c["attachments"] != false {
				t.Fatalf("%s: config offers attachments", name)
			}
		}
	})

	t.Run("download", func(t *testing.T) {
		v1 := g.fake.addVisitor("tok-media-1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		g.fake.addVisitor("tok-media-2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		key := "msg/10/2026/upload-9/abc"
		_ = objs.Put(context.Background(), key, bytes.NewReader([]byte("%PDF-1.4 x")), 10, "application/pdf")
		g.fake.media[v1.ID] = map[int64]store.MediaFile{9: {ConvMedia: store.ConvMedia{ID: 9, Status: "ready", ContentType: "application/pdf", Filename: "invoice.pdf"}, Key: key}}
		r := g.do("GET", "/chat/v1/media/9?site="+siteKey, nil, auth("tok-media-1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
		h := r.Header()
		if r.Code != 200 || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Type") != "application/pdf" ||
			!strings.HasPrefix(h.Get("Content-Disposition"), "attachment") || h.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("own file: %d %v", r.Code, h)
		}
		if r := g.do("GET", "/chat/v1/media/9?site="+siteKey, nil, auth("tok-media-2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")); r.Code != 404 {
			t.Fatalf("another visitor's file: %d", r.Code)
		}
		if r := g.do("GET", "/chat/v1/media/9?site="+siteKey, nil, nil); r.Code != 401 {
			t.Fatalf("no token: %d", r.Code)
		}
	})

	t.Run("logo", func(t *testing.T) {
		objs := &media.MemObjects{}
		g := newRig(t, func(s *store.ChatSite) { s.Logo, s.LogoType, s.LogoVersion = true, "image/png", 77 }, func(c *Config) { c.Objects = objs })
		_ = objs.Put(context.Background(), media.LogoKey(1), bytes.NewReader(testPNG), int64(len(testPNG)), "image/png")
		cfg := g.do("GET", "/chat/v1/challenge?site="+siteKey, nil, nil).json()["config"].(map[string]any)
		if cfg["logo"] != "/chat/v1/logo?site="+siteKey+"&v=77" {
			t.Fatalf("logo URL %v", cfg["logo"])
		}
		r := g.do("GET", "/chat/v1/logo?site="+siteKey, nil, nil)
		if r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || r.Header().Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(r.Body.Bytes(), testPNG) {
			t.Fatalf("logo: %d %v", r.Code, r.Header())
		}
		if r := g.do("GET", "/chat/v1/logo?site=ffffffffffffffffffffffff", nil, nil); r.Code != 404 {
			t.Fatalf("unknown site: %d", r.Code)
		}
		fr := g.do("GET", "/chat/v1/frame?site="+siteKey, nil, nil)
		if csp := fr.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data: blob:") {
			t.Fatalf("frame policy %q", csp)
		}
	})
}

// TestChatAttachmentErasedMeanwhile fails if the file of an upload whose row
// an erase removed while the file was being stored stays in the media store.
func TestChatAttachmentErasedMeanwhile(t *testing.T) {
	objs := &media.MemObjects{}
	g := newRig(t, func(s *store.ChatSite) { s.Attachments = true }, func(c *Config) { c.Objects = objs })
	tok := g.token()
	g.fake.mu.Lock()
	g.fake.finishErr = store.ErrNotFound
	g.fake.mu.Unlock()
	if r := g.attach(tok, "photo.png", "image/png", testPNG, map[string]string{"clientMsgId": "e1"}); r.Code == http.StatusCreated {
		t.Fatalf("an erased upload was taken: %d", r.Code)
	}
	if n := objs.Len(); n != 0 {
		t.Fatalf("%d files left in the media store after the erase", n)
	}
}
