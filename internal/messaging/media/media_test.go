package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{1, 2, 3, 4}, 64)...)
	jpegBytes = append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), bytes.Repeat([]byte{9, 8, 7}, 64)...)
	gifBytes  = append([]byte("GIF89a\x01\x00\x01\x00"), bytes.Repeat([]byte{0}, 40)...)
	pdfBytes  = []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")
	mp4Bytes  = append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), bytes.Repeat([]byte{5}, 64)...)
	gp3Bytes  = append([]byte("\x00\x00\x00\x14ftyp3gp4\x00\x00\x00\x003gp4"), bytes.Repeat([]byte{5}, 64)...)
	amrBytes  = append([]byte("#!AMR\n"), bytes.Repeat([]byte{0x3c, 0x48}, 32)...)
	vcard     = []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Jane Doe\r\nTEL:+3247000000\r\nEND:VCARD\r\n")
)

// TestMediaPipeline fails if a polyglot or SVG is accepted, a declared type
// that contradicts the sniffed class is accepted, an oversize file is
// stored, a redirect leaves the provider's host, a media response lacks
// nosniff, or an outbound link works after expiry or use count (spec
// messaging-channels M-18; the database half is in internal/messaging).
func TestMediaPipeline(t *testing.T) {
	t.Run("allowlist and sniffing", func(t *testing.T) {
		ok := []struct {
			data     []byte
			declared string
			want     string
		}{
			{pngBytes, "image/png", "image/png"},
			{pngBytes, "image/jpeg", "image/png"}, // same class: the declared type is only advisory
			{jpegBytes, "", "image/jpeg"},
			{gifBytes, "application/octet-stream", "image/gif"},
			{pdfBytes, "application/pdf", "application/pdf"},
			{mp4Bytes, "video/mp4", "video/mp4"},
			{gp3Bytes, "video/3gpp", "video/3gpp"},
			{amrBytes, "audio/amr", "audio/amr"},
			{vcard, "text/x-vcard", "text/vcard"},
			{[]byte("see you at five"), "text/plain; charset=utf-8", "text/plain"},
		}
		for _, c := range ok {
			in, ref := Inspect(c.data, c.declared, InboxRules(0))
			if ref != nil || in.ContentType != c.want || in.Size != int64(len(c.data)) || len(in.SHA256) != 64 {
				t.Errorf("Inspect(%s, %q) = %+v, %v; want %s", c.want, c.declared, in, ref, c.want)
			}
		}
		refused := []struct {
			name     string
			data     []byte
			declared string
			rules    Rules
			reason   string
		}{
			{"svg declared", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "image/svg+xml", InboxRules(0), ReasonActiveContent},
			{"svg undeclared", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`), "", InboxRules(0), ReasonActiveContent},
			{"html as text", []byte("<!DOCTYPE html><html><body>hi</body></html>"), "text/plain", InboxRules(0), ReasonActiveContent},
			{"gif with script", append(append([]byte{}, gifBytes...), []byte("<script>alert(document.cookie)</script>")...), "image/gif", InboxRules(0), ReasonActiveContent},
			{"pdf that is a page", append(append([]byte{}, pdfBytes...), []byte("<HTML><BODY onload=x()>")...), "application/pdf", InboxRules(0), ReasonActiveContent},
			{"gifar", append(append([]byte{}, gifBytes...), []byte("PK\x05\x06\x00\x00\x00\x00\x01\x00\x01\x00")...), "image/gif", InboxRules(0), ReasonActiveContent},
			{"pdf claiming image", pdfBytes, "image/png", InboxRules(0), ReasonMismatch},
			{"png claiming pdf", pngBytes, "application/pdf", InboxRules(0), ReasonMismatch},
			{"exe", []byte("MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff"), "", InboxRules(0), ReasonNotAllowed},
			{"zip", []byte("PK\x03\x04\x14\x00\x00\x00\x08\x00"), "application/zip", InboxRules(0), ReasonNotAllowed},
			{"mp4 in a chat", mp4Bytes, "video/mp4", ChatRules(), ReasonNotAllowed},
			{"too large", bytes.Repeat([]byte("a"), 101), "text/plain", Rules{Allowed: InboxTypes, Max: 100}, ReasonTooLarge},
			{"empty", nil, "", InboxRules(0), ReasonEmpty},
			{"html declared", []byte("hello"), "text/html", InboxRules(0), ReasonActiveContent},
		}
		for _, c := range refused {
			if in, ref := Inspect(c.data, c.declared, c.rules); ref == nil || ref.Reason != c.reason {
				t.Errorf("%s: Inspect = %+v, %v; want refused %s", c.name, in, ref, c.reason)
			}
		}
	})

	t.Run("redirects stay on the provider's hosts", func(t *testing.T) {
		var leaked sync.Map
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			leaked.Store("hit", r.Header.Get("Authorization"))
			_, _ = w.Write(pngBytes)
		}))
		defer other.Close()
		var prov *httptest.Server
		prov = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/media/off":
				http.Redirect(w, r, other.URL+"/x", http.StatusFound)
			case "/media/same":
				http.Redirect(w, r, prov.URL+"/cdn/file", http.StatusFound)
			case "/cdn/file":
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(pngBytes)
			case "/media/gone":
				http.NotFound(w, r)
			case "/media/auth":
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer prov.Close()
		provURL, _ := url.Parse(prov.URL)
		check := func(u *url.URL) bool { return u.Host == provURL.Host }
		auth := func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret") }
		ctx := context.Background()
		if _, err := provider.FetchMedia(ctx, prov.Client(), prov.URL+"/media/off", check, auth); !errors.Is(err, provider.ErrMediaHost) {
			t.Fatalf("a redirect off the provider's host: %v", err)
		}
		if _, ok := leaked.Load("hit"); ok {
			t.Fatal("the other host was contacted")
		}
		if _, err := provider.FetchMedia(ctx, prov.Client(), other.URL+"/x", check, auth); !errors.Is(err, provider.ErrMediaHost) {
			t.Fatalf("a first URL off the provider's host: %v", err)
		}
		m, err := provider.FetchMedia(ctx, prov.Client(), prov.URL+"/media/same", check, auth)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(m.Body)
		_ = m.Body.Close()
		if !bytes.Equal(b, pngBytes) || m.ContentType != "image/png" {
			t.Fatalf("same-host redirect: %d bytes, %q", len(b), m.ContentType)
		}
		if _, err := provider.FetchMedia(ctx, prov.Client(), prov.URL+"/media/gone", check, auth); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("gone: %v", err)
		}
		if _, err := provider.FetchMedia(ctx, prov.Client(), prov.URL+"/media/auth", check, auth); !errors.Is(err, provider.ErrAuth) {
			t.Fatalf("auth: %v", err)
		}
	})

	t.Run("served with nosniff", func(t *testing.T) {
		objs := &MemObjects{}
		ctx := context.Background()
		_ = objs.Put(ctx, "msg/1/2026/9/a", bytes.NewReader(pdfBytes), int64(len(pdfBytes)), "application/pdf")
		_ = objs.Put(ctx, "msg/1/2026/9/b", bytes.NewReader(pngBytes), int64(len(pngBytes)), "image/png")
		for _, c := range []struct {
			m    Stored
			disp string
		}{
			{Stored{Key: "msg/1/2026/9/a", ContentType: "application/pdf", Filename: `in"voice/../x.pdf`}, "attachment"},
			{Stored{Key: "msg/1/2026/9/b", ContentType: "image/png", Filename: "photo.png"}, "inline"},
		} {
			rec := httptest.NewRecorder()
			Serve(rec, httptest.NewRequest("GET", "/x", nil), objs, c.m, nil)
			h := rec.Header()
			if rec.Code != 200 || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Type") != c.m.ContentType ||
				h.Get("Cache-Control") != "private, no-store" || !strings.HasPrefix(h.Get("Content-Disposition"), c.disp) ||
				!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
				t.Fatalf("%s: %d %v", c.m.ContentType, rec.Code, h)
			}
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("Range", "bytes=0-3")
		Serve(rec, req, objs, Stored{Key: "msg/1/2026/9/b", ContentType: "image/png"}, nil)
		if rec.Code != http.StatusPartialContent || rec.Body.Len() != 4 {
			t.Fatalf("range: %d, %d bytes", rec.Code, rec.Body.Len())
		}
		rec = httptest.NewRecorder()
		Serve(rec, httptest.NewRequest("GET", "/x", nil), objs, Stored{Key: "msg/1/2026/9/none", ContentType: "image/png"}, nil)
		if rec.Code != http.StatusNotFound || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("absent object: %d %v", rec.Code, rec.Header())
		}
	})

	t.Run("links expire and run out", func(t *testing.T) {
		objs := &MemObjects{}
		_ = objs.Put(context.Background(), "msg/1/2026/9/a", bytes.NewReader(pngBytes), int64(len(pngBytes)), "image/png")
		tok, hash, err := NewLinkToken()
		if err != nil || len(tok) != 43 || !bytes.Equal(hash, LinkHash(tok)) {
			t.Fatalf("token %q, %v", tok, err)
		}
		now := time.Now()
		uses, expires := 0, now.Add(LinkTTL)
		redeem := func(_ context.Context, h []byte) (Stored, error) {
			if !bytes.Equal(h, hash) || !now.Before(expires) || uses >= LinkMaxUses {
				return Stored{}, errors.New("no such link")
			}
			uses++
			return Stored{Key: "msg/1/2026/9/a", ContentType: "image/png"}, nil
		}
		mux := http.NewServeMux()
		mux.Handle("GET /m/{token}", LinkHandler(redeem, objs, nil))
		get := func(tok string) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("GET", "/m/"+tok, nil))
			return rec
		}
		for i := 0; i < LinkMaxUses; i++ {
			if rec := get(tok); rec.Code != 200 || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("use %d: %d", i+1, rec.Code)
			}
		}
		if rec := get(tok); rec.Code != 404 {
			t.Fatalf("a used-up link answered %d", rec.Code)
		}
		uses, now = 0, now.Add(LinkTTL+time.Second)
		if rec := get(tok); rec.Code != 404 {
			t.Fatalf("an expired link answered %d", rec.Code)
		}
		if rec := get("short"); rec.Code != 404 {
			t.Fatalf("a malformed token answered %d", rec.Code)
		}
	})
}

func TestCleanFilenameAndKeys(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":       "passwd",
		`C:\Users\x\a b.pdf`:     "a b.pdf",
		"":                       "attachment.png",
		"..":                     "attachment.png",
		"x\x00y\".png":           "xy.png",
		strings.Repeat("é", 200): strings.Repeat("é", 127),
	} {
		if got := CleanFilename(in, "image/png"); got != want {
			t.Errorf("CleanFilename(%q) = %q, want %q", in, got, want)
		}
	}
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if k := MessageKey(7, at, 42, "ab"); k != "msg/7/2026/42/ab" {
		t.Fatal(k)
	}
	if k := UploadKey(7, at, 9, "ab"); k != "msg/7/2026/upload-9/ab" {
		t.Fatal(k)
	}
	if err := (&MemObjects{}).Put(context.Background(), "voicemail/x", strings.NewReader("x"), 1, "x"); err == nil {
		t.Fatal("a key outside msg/ was stored")
	}
}

// fakeStore is the workers' database.
type fakeStore struct {
	mu       sync.Mutex
	jobs     []FetchJob
	results  map[int64]FetchResult
	garbage  []Garbage
	dropped  []Garbage
	claimErr error
}

func (f *fakeStore) ClaimMediaFetch(context.Context, int) ([]FetchJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j := f.jobs
	f.jobs = nil
	return j, f.claimErr
}
func (f *fakeStore) FinishMediaFetch(_ context.Context, j FetchJob, r FetchResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.results == nil {
		f.results = map[int64]FetchResult{}
	}
	f.results[j.ID] = r
	return nil
}
func (f *fakeStore) ChannelProviderConfig(context.Context, int64) (provider.Config, error) {
	return provider.Config{AccountSID: "AC1"}, nil
}
func (f *fakeStore) MediaGarbage(context.Context, int) ([]Garbage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.garbage, nil
}
func (f *fakeStore) DropMedia(_ context.Context, g Garbage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = append(f.dropped, g)
	return nil
}

// fakeCarrier serves media by URL.
type fakeCarrier struct {
	files map[string][]byte
	err   map[string]error
}

func (fakeCarrier) Kind() string                   { return "fake" }
func (fakeCarrier) Caps() provider.Caps            { return provider.Caps{} }
func (fakeCarrier) Validate(context.Context) error { return nil }
func (fakeCarrier) Send(context.Context, provider.Outbound) (provider.Sent, error) {
	return provider.Sent{}, nil
}
func (fakeCarrier) Status(context.Context, string) (provider.State, error) {
	return provider.State{}, nil
}
func (fakeCarrier) Numbers(context.Context) ([]provider.Number, error) { return nil, nil }
func (c fakeCarrier) FetchMedia(_ context.Context, ref provider.MediaRef) (provider.Media, error) {
	if err := c.err[ref.URL]; err != nil {
		return provider.Media{}, err
	}
	return provider.Media{Body: io.NopCloser(bytes.NewReader(c.files[ref.URL])), ContentType: ref.ContentType}, nil
}

// TestMediaWorker fails if a download is not stored under msg/ with its
// sniffed type, a refused file is stored, a gone file is not expired, a
// download past its window is tried, or the sweeper drops a row whose file it
// could not remove.
func TestMediaWorker(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	carrier := fakeCarrier{
		files: map[string][]byte{"https://m/ok": jpegBytes, "https://m/svg": []byte(`<svg onload="x()"/>`), "https://m/big": bytes.Repeat([]byte("a"), 300)},
		err:   map[string]error{"https://m/gone": provider.ErrNotFound, "https://m/host": provider.ErrMediaHost, "https://m/down": provider.ErrAmbiguous},
	}
	st := &fakeStore{jobs: []FetchJob{
		{ID: 1, MessageID: 11, ChannelID: 3, Provider: "fake", Ref: provider.MediaRef{URL: "https://m/ok", ContentType: "image/jpeg"}, CreatedAt: now},
		{ID: 2, MessageID: 11, ChannelID: 3, Provider: "fake", Ref: provider.MediaRef{URL: "https://m/svg", ContentType: "image/svg+xml"}, CreatedAt: now},
		{ID: 3, MessageID: 11, ChannelID: 3, Provider: "fake", Ref: provider.MediaRef{URL: "https://m/gone"}, CreatedAt: now},
		{ID: 4, MessageID: 11, ChannelID: 3, Provider: "fake", Ref: provider.MediaRef{URL: "https://m/host"}, CreatedAt: now},
		{ID: 5, MessageID: 11, ChannelID: 3, Provider: "fake", Ref: provider.MediaRef{URL: "https://m/down"}, CreatedAt: now, Attempts: 1},
		{ID: 6, MessageID: 11, ChannelID: 3, Provider: "fake", Ref: provider.MediaRef{URL: "https://m/ok"}, CreatedAt: now.Add(-25 * time.Hour)},
		{ID: 7, MessageID: 11, ChannelID: 3, Provider: "fake", Ref: provider.MediaRef{URL: "https://m/big"}, CreatedAt: now},
		{ID: 8, MessageID: 11, ChannelID: 3, Provider: "fake", Ref: provider.MediaRef{URL: "https://m/ok", Size: 1 << 30}, CreatedAt: now},
	}}
	objs := &MemObjects{}
	var counted []string
	w := &Worker{Store: st, Objects: objs, Max: 100, Now: func() time.Time { return now },
		New:   func(string, provider.Config) (provider.SMSProvider, error) { return carrier, nil },
		Count: func(r string) { counted = append(counted, r) }}
	// The jpeg is larger than 100 bytes; give the one good file room.
	w.Max = int64(len(jpegBytes))
	n, err := w.FetchOnce(context.Background(), 10)
	if err != nil || n != 8 {
		t.Fatalf("FetchOnce = %d, %v", n, err)
	}
	r := st.results
	if r[1].Status != StatusReady || r[1].ContentType != "image/jpeg" || r[1].Key != MessageKey(3, now, 11, r[1].SHA256) {
		t.Fatalf("ok: %+v", r[1])
	}
	if b := objs.M[r[1].Key]; !bytes.Equal(b, jpegBytes) || objs.Types[r[1].Key] != "image/jpeg" {
		t.Fatalf("stored %d bytes as %q", len(b), objs.Types[r[1].Key])
	}
	want := map[int64][2]string{2: {StatusRejected, ReasonActiveContent}, 3: {StatusExpired, ReasonGone}, 4: {StatusRejected, ReasonMediaHost},
		5: {StatusPending, ""}, 6: {StatusExpired, ReasonWindow}, 7: {StatusRejected, ReasonTooLarge}, 8: {StatusRejected, ReasonTooLarge}}
	for id, wv := range want {
		if r[id].Status != wv[0] || r[id].Reason != wv[1] {
			t.Errorf("job %d: %+v, want %v", id, r[id], wv)
		}
	}
	if r[5].Retry != 5*time.Minute {
		t.Errorf("second try waits %s", r[5].Retry)
	}
	if objs.Len() != 1 || len(counted) != 8 {
		t.Fatalf("%d objects stored, %d counted", objs.Len(), len(counted))
	}

	// A carrier that cannot fetch media refuses at once.
	st.jobs = []FetchJob{{ID: 9, Provider: "text", Ref: provider.MediaRef{URL: "https://m/ok"}, CreatedAt: now}}
	w.New = func(string, provider.Config) (provider.SMSProvider, error) {
		return struct{ provider.SMSProvider }{carrier}, nil
	}
	if _, err := w.FetchOnce(context.Background(), 10); err != nil || st.results[9].Reason != ReasonNoFetcher {
		t.Fatalf("no fetcher: %+v, %v", st.results[9], err)
	}

	// The sweeper removes, checks and only then settles.
	st.garbage = []Garbage{{ID: 1, Key: r[1].Key, Drop: true}, {ID: 2, Key: "", Drop: true}}
	if n, err := w.CollectOnce(context.Background(), 10); err != nil || n != 2 || objs.Len() != 0 || len(st.dropped) != 2 {
		t.Fatalf("collect = %d, %v; %d objects, %d dropped", n, err, objs.Len(), len(st.dropped))
	}
	_ = objs.Put(context.Background(), "msg/3/2026/1/x", strings.NewReader("x"), 1, "text/plain")
	objs.FailRemove = errors.New("minio down")
	st.garbage, st.dropped = []Garbage{{ID: 3, Key: "msg/3/2026/1/x", Drop: true}}, nil
	if n, err := w.CollectOnce(context.Background(), 10); err != nil || n != 0 || len(st.dropped) != 0 {
		t.Fatalf("a failed removal settled the row: %d, %v, %v", n, err, st.dropped)
	}
}
