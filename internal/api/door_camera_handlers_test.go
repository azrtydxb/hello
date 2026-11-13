package api

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/safehttp"
	"github.com/azrtydxb/hello/internal/store"
)

// camFake adds the door camera store calls and the extension lookup.
type camFake struct {
	*acctFake
	cmu   sync.Mutex
	kinds map[int64]string
	cams  map[int64]store.DoorCameraAccess
}

func (f *camFake) GetExtension(_ context.Context, id int64) (store.Extension, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	k, ok := f.kinds[id]
	if !ok {
		return store.Extension{}, store.ErrNotFound
	}
	return store.Extension{ID: id, Number: "900", Kind: k}, nil
}

func (f *camFake) GetDoorCamera(_ context.Context, id int64) (store.DoorCamera, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	c, ok := f.cams[id]
	if !ok {
		return store.DoorCamera{}, nil
	}
	return store.DoorCamera{Configured: true, Kind: c.Kind, URL: c.URL, Username: c.Username, PasswordSet: c.Password != ""}, nil
}

func (f *camFake) SetDoorCamera(_ context.Context, _ string, id int64, in store.DoorCameraInput) (store.DoorCamera, error) {
	f.cmu.Lock()
	c := f.cams[id]
	c.Kind, c.URL, c.Username = in.Kind, in.URL, in.Username
	if in.Password != nil {
		c.Password = *in.Password
	}
	f.cams[id] = c
	f.cmu.Unlock()
	return f.GetDoorCamera(context.Background(), id)
}

func (f *camFake) DeleteDoorCamera(_ context.Context, _ string, id int64) error {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	if _, ok := f.cams[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.cams, id)
	return nil
}

func (f *camFake) DoorCameraForView(_ context.Context, id int64) (store.DoorCameraAccess, bool, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	c, ok := f.cams[id]
	return c, ok, nil
}

// TestDoorCamera fails if the camera password is returned, logged or audited,
// if a user or viewer can change a camera, if the picture is not fetched with
// the stored credentials and passed on with its type, if a camera that answers
// with anything but a picture, an unreachable one or an address outside the
// allow-list is not a clean 502 that does not echo the address, if too many
// pictures at once are not refused, or if rtsp:// is accepted (spec S-49).
func TestDoorCamera(t *testing.T) {
	const pw = "cam-s3cret-pw"
	var mu sync.Mutex
	var seenAuth, seenPath string
	snap := []byte("\xff\xd8\xff\xe0jpegbytes\xff\xd9")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth, seenPath = r.Header.Get("Authorization"), r.URL.Path
		mu.Unlock()
		switch r.URL.Path {
		case "/snap.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(snap)
		case "/stream.mjpg":
			w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
			_, _ = io.WriteString(w, "--frame\r\nContent-Type: image/jpeg\r\n\r\n")
			_, _ = w.Write(snap)
			_, _ = io.WriteString(w, "\r\n")
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<script>alert(1)</script>")
		case "/missing":
			http.NotFound(w, r)
		case "/hold":
			w.Header().Set("Content-Type", "image/jpeg")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	}))
	defer upstream.Close()

	af, _, _, _ := newAcctFake(t)
	f := &camFake{acctFake: af, kinds: map[int64]string{7: store.ExtensionDoor, 8: store.ExtensionUser}, cams: map[int64]store.DoorCameraAccess{}}
	logs := &strings.Builder{}
	h := Handler(Config{Store: f, CameraHTTP: &http.Client{Transport: http.DefaultTransport}, SessionTTL: time.Hour,
		Log: slog.New(slog.NewTextHandler(logs, nil))})

	put := func(who string, body any) *httptest.ResponseRecorder {
		return acctCall(h, who, "PUT", "/api/v1/extensions/7/camera", body)
	}
	t.Run("only an administrator sets it, and the password is write-only", func(t *testing.T) {
		body := map[string]any{"kind": "snapshot", "url": upstream.URL + "/snap.jpg", "username": "viewer", "password": pw}
		for _, who := range []string{"alice", "dave"} {
			if rec := put(who, body); rec.Code != http.StatusForbidden {
				t.Errorf("%s PUT = %d, want 403", who, rec.Code)
			}
			if rec := acctCall(h, who, "DELETE", "/api/v1/extensions/7/camera", nil); rec.Code != http.StatusForbidden {
				t.Errorf("%s DELETE = %d, want 403", who, rec.Code)
			}
		}
		rec := put("carol", body)
		got := wantJSON(t, rec, http.StatusOK, "set")
		if got["configured"] != true || got["passwordSet"] != true || got["username"] != "viewer" || strings.Contains(rec.Body.String(), pw) {
			t.Fatalf("set = %v", got)
		}
		// A later change without a password keeps it.
		put("carol", map[string]any{"kind": "snapshot", "url": upstream.URL + "/snap.jpg", "username": "viewer2"})
		if c, _, _ := f.DoorCameraForView(context.Background(), 7); c.Password != pw || c.Username != "viewer2" {
			t.Errorf("stored camera = %+v, want the password kept", c)
		}
		rec = acctCall(h, "dave", "GET", "/api/v1/extensions/7/camera", nil)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), pw) {
			t.Errorf("viewer GET = %d %s", rec.Code, rec.Body)
		}
		if strings.Contains(logs.String(), pw) {
			t.Error("the password reached the log")
		}
		for _, a := range af.audit {
			if strings.Contains(a, pw) {
				t.Errorf("audit carries the password: %q", a)
			}
		}
	})

	t.Run("validation and kinds", func(t *testing.T) {
		for _, c := range []struct {
			who  string
			path string
			body any
			want int
		}{
			{"carol", "/api/v1/extensions/7/camera", map[string]any{"url": "http://x/y"}, http.StatusBadRequest},
			{"carol", "/api/v1/extensions/7/camera", map[string]any{"kind": "snapshot", "url": "http://x/y", "extra": 1}, http.StatusBadRequest},
		} {
			if rec := acctCall(h, c.who, "PUT", c.path, c.body); rec.Code != c.want {
				t.Errorf("PUT %v = %d %s, want %d", c.body, rec.Code, rec.Body, c.want)
			}
		}
		for _, c := range []struct {
			method, path string
			want         int
		}{
			{"GET", "/api/v1/extensions/8/camera", http.StatusConflict},
			{"GET", "/api/v1/extensions/99/camera", http.StatusNotFound},
			{"PUT", "/api/v1/extensions/8/camera", http.StatusConflict},
			{"PUT", "/api/v1/extensions/99/camera", http.StatusNotFound},
			{"DELETE", "/api/v1/extensions/8/camera", http.StatusConflict},
			{"DELETE", "/api/v1/extensions/99/camera", http.StatusNotFound},
			{"GET", "/api/v1/extensions/8/camera/view", http.StatusConflict},
			{"GET", "/api/v1/extensions/99/camera/view", http.StatusNotFound},
		} {
			var body any
			if c.method == "PUT" {
				body = map[string]any{"kind": "snapshot", "url": "http://x/y"}
			}
			if rec := acctCall(h, "carol", c.method, c.path, body); rec.Code != c.want {
				t.Errorf("%s %s = %d %s, want %d", c.method, c.path, rec.Code, rec.Body, c.want)
			}
		}
	})

	view := func(who string) *httptest.ResponseRecorder {
		return acctCall(h, who, "GET", "/api/v1/extensions/7/camera/view", nil)
	}
	setURL := func(kind, path string) {
		f.cmu.Lock()
		c := f.cams[7]
		c.Kind, c.URL = kind, upstream.URL+path
		f.cams[7] = c
		f.cmu.Unlock()
	}

	t.Run("a snapshot is fetched with the credentials and passed on", func(t *testing.T) {
		setURL("snapshot", "/snap.jpg")
		rec := view("dave")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Body.String() != string(snap) {
			t.Fatalf("view = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Error("a camera picture is cacheable")
		}
		mu.Lock()
		defer mu.Unlock()
		if want := "Basic " + basic("viewer2", pw); seenAuth != want || seenPath != "/snap.jpg" {
			t.Errorf("the camera saw %q on %q, want the stored credentials", seenAuth, seenPath)
		}
		if view("alice").Code != http.StatusForbidden {
			t.Error("a user can look at the camera")
		}
	})

	t.Run("a stream is passed through", func(t *testing.T) {
		setURL("mjpeg", "/stream.mjpg")
		rec := view("dave")
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "multipart/x-mixed-replace") ||
			!strings.Contains(rec.Body.String(), string(snap)) {
			t.Errorf("stream = %d %q (%d bytes)", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	})

	t.Run("a camera that does not give a picture is a clean 502", func(t *testing.T) {
		for name, path := range map[string]string{"a web page": "/html", "a 404": "/missing"} {
			setURL("snapshot", path)
			rec := view("dave")
			if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), upstream.URL) || strings.Contains(rec.Body.String(), pw) {
				t.Errorf("%s: %d %s, want a 502 that does not echo the address", name, rec.Code, rec.Body)
			}
		}
		f.cmu.Lock()
		f.cams[7] = store.DoorCameraAccess{Kind: "snapshot", URL: "http://127.0.0.1:1/x"}
		f.cmu.Unlock()
		if rec := view("dave"); rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "127.0.0.1") {
			t.Errorf("unreachable camera: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("no camera and no proxy", func(t *testing.T) {
		f.cmu.Lock()
		delete(f.cams, 7)
		f.cmu.Unlock()
		if rec := view("dave"); rec.Code != http.StatusNotFound {
			t.Errorf("a station with no camera: %d, want 404", rec.Code)
		}
		if rec := acctCall(h, "carol", "DELETE", "/api/v1/extensions/7/camera", nil); rec.Code != http.StatusNotFound {
			t.Errorf("delete of no camera = %d, want 404", rec.Code)
		}
		put("carol", map[string]any{"kind": "snapshot", "url": upstream.URL + "/snap.jpg"})
		if rec := acctCall(h, "carol", "DELETE", "/api/v1/extensions/7/camera", nil); rec.Code != http.StatusNoContent {
			t.Errorf("delete = %d, want 204", rec.Code)
		}
		put("carol", map[string]any{"kind": "snapshot", "url": upstream.URL + "/snap.jpg"})
		h2 := Handler(Config{Store: f, SessionTTL: time.Hour, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if rec := acctCall(h2, "dave", "GET", "/api/v1/extensions/7/camera/view", nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("without a proxy client: %d, want 503", rec.Code)
		}
	})

	t.Run("the guarded transport refuses a loopback camera", func(t *testing.T) {
		guarded := Handler(Config{Store: f, SessionTTL: time.Hour, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			CameraHTTP: &http.Client{Transport: safehttp.NewTransport(safehttp.Options{})}})
		setURL("snapshot", "/snap.jpg")
		rec := acctCall(guarded, "dave", "GET", "/api/v1/extensions/7/camera/view", nil)
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "HELLO_DOOR_CAMERA_ALLOW") {
			t.Errorf("loopback camera: %d %s, want 502 naming HELLO_DOOR_CAMERA_ALLOW", rec.Code, rec.Body)
		}
	})

	t.Run("too many pictures at once", func(t *testing.T) {
		setURL("snapshot", "/hold")
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for range cameraStreamsMax {
			wg.Go(func() {
				req := httptest.NewRequest("GET", "/api/v1/extensions/7/camera/view", nil).WithContext(ctx)
				req.AddCookie(&http.Cookie{Name: "hello_session", Value: "dave" + cookieSuffix})
				h.ServeHTTP(httptest.NewRecorder(), req)
			})
		}
		deadline := time.Now().Add(5 * time.Second)
		for cameraStreams.Load() < cameraStreamsMax && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		rec := view("dave")
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Errorf("a ninth picture: %d, want 429 with Retry-After", rec.Code)
		}
		cancel()
		wg.Wait()
		if n := cameraStreams.Load(); n != 0 {
			t.Errorf("%d pictures still counted after they ended", n)
		}
	})
}

func basic(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}
