package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/valkey-io/valkey-go"
)

// memObjects is an in-memory Objects, for tests that exercise the handlers
// without an object store (the MinIO container test uses the real one).
type memObjects struct {
	mu       sync.Mutex
	objs     map[string][]byte
	presigns int
}

func newMemObjects() *memObjects { return &memObjects{objs: map[string][]byte{}} }

func (m *memObjects) Presign(_ context.Context, object string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objs[object]; !ok {
		return "", fmt.Errorf("memObjects: %s: not found", object)
	}
	m.presigns++
	return "http://objects.test/" + object + "?sig=1", nil
}

func (m *memObjects) Put(_ context.Context, object string, r io.Reader, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.objs[object] = b
	return nil
}

func (m *memObjects) Get(_ context.Context, object string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[object]
	if !ok {
		return nil, fmt.Errorf("memObjects: %s: not found", object)
	}
	return b, nil
}

func (m *memObjects) Remove(_ context.Context, object string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, object)
	return nil
}

func (m *memObjects) EnsureBucket(context.Context) error { return nil }

func (m *memObjects) has(object string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objs[object]
	return ok
}

// startMinio starts a throwaway MinIO container on a free local port and
// returns Objects against it. HELLO_TEST_MINIO_ENDPOINT (host:port) reuses a
// running instance instead.
func startMinio(t *testing.T) *MinioObjects {
	t.Helper()
	if ep := testMinioEndpoint; ep != "" {
		u, p := minioCreds()
		o, err := NewMinioObjects(ep, u, p, false)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available for the MinIO container")
	}
	// A free port: bind, read it, close, and let MinIO take it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	name := fmt.Sprintf("p4ctl-minio-%d", time.Now().UnixNano())
	ep := fmt.Sprintf("127.0.0.1:%d", port)
	// minio/minio first; the local mirror's cleanstart/minio as fallback.
	var out []byte
	started := false
	// The data path differs per image: /data on minio/minio, /tmp/data on
	// the cleanstart mirror (its backend refuses /data).
	for _, image := range []string{"minio/minio:latest", "cleanstart/minio:latest"} {
		path := "/data"
		if image != "minio/minio:latest" {
			path = "/tmp/data"
		}
		cmd := exec.Command("docker", "run", "-d", "--rm", "--name", name,
			"-p", fmt.Sprintf("127.0.0.1:%d:9000", port),
			"-e", "MINIO_ROOT_USER="+testMinioUser, "-e", "MINIO_ROOT_PASSWORD="+testMinioPass,
			image, "server", path)
		out, err = cmd.CombinedOutput()
		if err == nil {
			started = true
			break
		}
	}
	if !started {
		t.Skipf("minio container did not start: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run() // only ours: unique p4ctl- name
	})
	o, err := NewMinioObjects(ep, testMinioUser, testMinioPass, false)
	if err != nil {
		t.Fatal(err)
	}
	// Wait until the server answers, then make sure the bucket exists.
	deadline := time.Now().Add(60 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := o.EnsureBucket(ctx)
		cancel()
		if err == nil {
			return o
		}
		if time.Now().After(deadline) {
			t.Fatalf("minio not ready: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

const (
	testMinioUser = "hello"
	testMinioPass = "hello-voicemail-secret"
)

// minioCreds returns the credentials for a reused endpoint (env override),
// or the ones startMinio sets on its own container.
func minioCreds() (string, string) {
	if testMinioEndpoint != "" {
		if v := os.Getenv("HELLO_TEST_MINIO_PASS"); v != "" {
			return testMinioUser, v
		}
		return testMinioUser, "hello-secret"
	}
	return testMinioUser, testMinioPass
}

// testMinioEndpoint reuses a running MinIO instead of starting one (CI
// without docker can set HELLO_TEST_MINIO_ENDPOINT).
var testMinioEndpoint = strings.TrimSpace(os.Getenv("HELLO_TEST_MINIO_ENDPOINT"))

func testValkeyAddr() string { return os.Getenv("HELLO_TEST_VALKEY_ADDR") }

func newPBXEnv(t *testing.T, objects Objects) *env {
	t.Helper()
	e := newEnvConfig(t, Config{Objects: objects}, nil)
	c := e.login()
	e.ext101 = c.must(http.StatusCreated, "POST", "/api/v1/extensions",
		map[string]string{"number": "101", "name": "Sales"}).json(t)
	e.ext102 = c.must(http.StatusCreated, "POST", "/api/v1/extensions",
		map[string]string{"number": "102", "name": "Support"}).json(t)
	return e
}

// wav is a minimal RIFF/WAVE payload; the API checks the container header.
var wav = append([]byte("RIFF"), append([]byte{36, 0, 0, 0}, []byte("WAVEfmt ")...)...)

// TestVoicemailBoxSettings: box created with the extension, email and
// password changes, greeting upload, validation and the audit trail.
func TestVoicemailBoxSettings(t *testing.T) {
	objs := newMemObjects()
	e := newPBXEnv(t, objs)
	ctx := context.Background()
	c := e.login()
	path := fmt.Sprintf("/api/v1/extensions/%v/voicemail", e.ext101["id"])

	// A new extension already has its box (spec S-8).
	box := c.must(http.StatusOK, "GET", path, nil).json(t)
	if box["hasPassword"] != false || box["email"] != "" || box["greetingObject"] != "" {
		t.Fatalf("fresh box = %v", box)
	}

	rev0, err := e.st.ConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Settings change via JSON.
	box = c.must(http.StatusOK, "PUT", path, map[string]string{"email": "sales@hello.test"}).json(t)
	if box["email"] != "sales@hello.test" {
		t.Fatalf("email not stored: %v", box)
	}
	box = c.must(http.StatusOK, "PUT", path, map[string]string{"password": "9999"}).json(t)
	if box["hasPassword"] != true || strings.Contains(string(fmt.Sprint(box)), "9999") {
		t.Fatalf("password change: %v", box)
	}
	// The password is stored as a bcrypt hash, never in the clear.
	var hash string
	if err := e.db.QueryRowContext(ctx,
		`SELECT password_hash FROM voicemail_boxes WHERE extension_id = $1`, e.ext101["id"]).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == "" || strings.Contains(hash, "9999") {
		t.Fatalf("stored password hash = %q", hash)
	}

	// A WAV greeting uploads via multipart and lands in the object store.
	body := "--BND\r\nContent-Disposition: form-data; name=\"greeting\"; filename=\"g.wav\"\r\n" +
		"Content-Type: audio/wav\r\n\r\n" + string(wav) + "\r\n--BND--\r\n"
	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	box = c.must(http.StatusOK, "PUT", path, body).json(t)
	c.header.Del("Content-Type")
	object, _ := box["greetingObject"].(string)
	if object == "" || !strings.HasPrefix(object, "box/") || !objs.has(object) {
		t.Fatalf("greeting not stored: %v", box)
	}
	// A non-WAV upload is refused and stores nothing.
	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	body = "--BND\r\nContent-Disposition: form-data; name=\"greeting\"; filename=\"g.txt\"\r\n" +
		"Content-Type: text/plain\r\n\r\nnot audio\r\n--BND--\r\n"
	c.must(http.StatusBadRequest, "PUT", path, body)
	c.header.Del("Content-Type")
	if len(objs.objs) != 1 {
		t.Fatalf("refused upload stored objects: %v", objs.objs)
	}

	// Validation and the empty-change guard.
	for _, bad := range []any{
		map[string]string{"password": "12"},
		map[string]string{"email": "not-an-email"},
		map[string]any{},
	} {
		if r := c.do("PUT", path, bad); r.code != http.StatusBadRequest {
			t.Errorf("PUT %v = %d %s, want 400", bad, r.code, r.body)
		}
	}

	// The change was audited and bumped the revision (NOTIFY rides along).
	rev1, err := e.st.ConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rev1 <= rev0 {
		t.Fatalf("revision %d did not move past %d", rev1, rev0)
	}
	var audits int
	if err := e.db.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_events WHERE resource = 'voicemail_box'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits < 3 {
		t.Fatalf("voicemail_box audits = %d, want one per change", audits)
	}
	// A missing extension stays 404.
	c.must(http.StatusNotFound, "GET", "/api/v1/extensions/999999/voicemail", nil)
}

// TestVoicemailMessagesFlow runs list/heard/delete/audio against a real
// MinIO container. It fails if the audio route hands out a URL for a
// message that does not exist (the presign gate), or if deletion leaves the
// object behind.
func TestVoicemailMessagesFlow(t *testing.T) {
	objs := startMinio(t)
	e := newPBXEnv(t, objs)
	ctx := context.Background()
	c := e.login()
	boxResp := c.must(http.StatusOK, "GET",
		fmt.Sprintf("/api/v1/extensions/%v/voicemail", e.ext101["id"]), nil).json(t)
	boxID := fmt.Sprintf("%v", boxResp["id"])

	// A message hello-sip would have left: row + WAV in the bucket.
	object := fmt.Sprintf("box/%s/1760000000-call-abc.wav", boxID)
	if err := objs.Put(ctx, object, strings.NewReader(string(wav)+string(wav)), int64(2*len(wav))); err != nil {
		t.Fatal(err)
	}
	second := fmt.Sprintf("box/%s/1760000100-call-def.wav", boxID)
	if err := objs.Put(ctx, second, strings.NewReader(string(wav)), int64(len(wav))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(ctx,
		`INSERT INTO voicemail_messages (box_id, minio_object, caller, duration_ms) VALUES ($1, $2, '102', 34000)`,
		boxResp["id"], object); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(ctx,
		`INSERT INTO voicemail_messages (box_id, minio_object, caller, duration_ms) VALUES ($1, $2, '103', 5000)`,
		boxResp["id"], second); err != nil {
		t.Fatal(err)
	}

	listPath := "/api/v1/voicemail/messages?box=" + boxID
	var list struct {
		Items []map[string]any `json:"items"`
	}
	get := func(path string) {
		t.Helper()
		if err := json.Unmarshal(c.must(http.StatusOK, "GET", path, nil).body, &list); err != nil {
			t.Fatal(err)
		}
	}
	get(listPath)
	if len(list.Items) != 2 {
		t.Fatalf("messages = %v", list.Items)
	}
	m0 := list.Items[0]
	for _, k := range []string{"id", "boxId", "caller", "durationMs", "heard", "emailStatus", "createdAt"} {
		if _, ok := m0[k]; !ok {
			t.Fatalf("message missing %q: %v", k, m0)
		}
	}
	if _, internal := m0["object"]; internal {
		t.Fatalf("message leaks the object key: %v", m0)
	}
	// Newest first; the unheard filter narrows it.
	if m0["caller"] != "103" {
		t.Fatalf("order wrong: %v", list.Items)
	}
	get(listPath + "&unheard=true")
	if len(list.Items) != 2 {
		t.Fatalf("unheard filter = %d, want 2", len(list.Items))
	}
	if r := c.do("GET", "/api/v1/voicemail/messages?box=nope", nil); r.code != http.StatusBadRequest {
		t.Fatalf("bad box = %d, want 400", r.code)
	}

	// Heard: an empty body marks it, {"heard": false} unmarks it.
	newest := fmt.Sprint(m0["id"])
	h := c.must(http.StatusOK, "POST", "/api/v1/voicemail/messages/"+newest+"/heard", nil).json(t)
	if h["heard"] != true {
		t.Fatalf("heard response = %v", h)
	}
	get(listPath + "&unheard=true")
	if len(list.Items) != 1 {
		t.Fatalf("unheard after mark = %d, want 1", len(list.Items))
	}
	c.must(http.StatusOK, "POST", "/api/v1/voicemail/messages/"+newest+"/heard", map[string]bool{"heard": false})
	get(listPath + "&unheard=true")
	if len(list.Items) != 2 {
		t.Fatalf("unheard after unmark = %d, want 2", len(list.Items))
	}

	// Audio: 302 to a presigned URL, 15 minutes, only for a real message.
	// The test client follows redirects; stop at the 302 to inspect it.
	c.hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { c.hc.CheckRedirect = nil }()
	rec := c.do("GET", "/api/v1/voicemail/messages/"+fmt.Sprint(list.Items[1]["id"])+"/audio", nil)
	if rec.code != http.StatusFound {
		t.Fatalf("audio = %d %s, want 302", rec.code, rec.body)
	}
	loc := rec.header.Get("Location")
	if !strings.Contains(loc, object) || !strings.Contains(loc, "X-Amz-Signature") {
		t.Fatalf("Location %q is not a presigned URL for %s", loc, object)
	}
	if r := c.do("GET", "/api/v1/voicemail/messages/999999/audio", nil); r.code != http.StatusNotFound {
		t.Fatalf("audio for a missing message = %d, want 404 (presign gate)", r.code)
	}

	// Delete removes the row and the object.
	c.must(http.StatusNoContent, "DELETE", "/api/v1/voicemail/messages/"+fmt.Sprint(list.Items[1]["id"]), nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/voicemail/messages/"+fmt.Sprint(list.Items[1]["id"]), nil)
	if _, err := objs.Get(ctx, object); err == nil {
		t.Fatalf("object %s survived the delete", object)
	}
	if r := c.do("GET", "/api/v1/voicemail/messages/"+fmt.Sprint(list.Items[1]["id"])+"/audio", nil); r.code != http.StatusNotFound {
		t.Fatalf("audio after delete = %d, want 404 (presign gate)", r.code)
	}
	get(listPath)
	if len(list.Items) != 1 {
		t.Fatalf("messages after delete = %d, want 1", len(list.Items))
	}
	// Email status is visible on the list.
	if _, err := e.db.ExecContext(ctx,
		`UPDATE voicemail_messages SET email_status = 'sent'`); err != nil {
		t.Fatal(err)
	}
	get(listPath)
	if list.Items[0]["emailStatus"] != "sent" {
		t.Fatalf("emailStatus = %v", list.Items[0]["emailStatus"])
	}
}

// TestRingGroupCRUDValidation: CRUD with member positions, plus the
// validation that keeps strategies, failure destinations, positions and
// weights honest. It fails if a bad strategy, position or weight is stored.
func TestRingGroupCRUDValidation(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	m1 := fmt.Sprintf("%v", e.ext101["id"])
	m2 := fmt.Sprintf("%v", e.ext102["id"])

	good := map[string]any{
		"name": "sales", "strategy": "sequential", "ringTimeout": 25, "memberDelay": 4,
		"failureKind": "voicemail", "failureTarget": "101",
		"members": []map[string]any{
			{"extensionId": json.RawMessage(m1), "position": 1},
			{"extensionId": json.RawMessage(m2), "position": 2, "delay": 3},
		},
	}
	created := c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", good).json(t)
	id := fmt.Sprint(created["id"])
	if created["strategy"] != "sequential" || created["failureTarget"] != "101" {
		t.Fatalf("created group = %v", created)
	}
	members := created["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members = %v", members)
	}
	first := members[0].(map[string]any)
	if first["position"] != float64(1) || first["number"] != "101" || first["weight"] != float64(1) {
		t.Fatalf("first member = %v (want position 1, number 101, weight default 1)", first)
	}
	if members[1].(map[string]any)["delay"] != float64(3) {
		t.Fatalf("second member = %v", members[1])
	}

	// Names are unique.
	c.must(http.StatusConflict, "POST", "/api/v1/ring-groups", good)

	// List and get.
	var groups struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/ring-groups", nil).body, &groups); err != nil || len(groups.Items) != 1 {
		t.Fatalf("groups = %v (%v)", groups, err)
	}
	one := c.must(http.StatusOK, "GET", "/api/v1/ring-groups/"+id, nil).json(t)
	if len(one["members"].([]any)) != 2 {
		t.Fatalf("get = %v", one)
	}

	// PATCH replaces only what it names; a members list replaces all.
	patched := c.must(http.StatusOK, "PATCH", "/api/v1/ring-groups/"+id,
		map[string]any{"strategy": "weighted"}).json(t)
	if patched["strategy"] != "weighted" || patched["name"] != "sales" || len(patched["members"].([]any)) != 2 {
		t.Fatalf("patched = %v", patched)
	}
	patched = c.must(http.StatusOK, "PATCH", "/api/v1/ring-groups/"+id,
		map[string]any{"members": []map[string]any{{"extensionId": json.RawMessage(m2), "position": 1}}}).json(t)
	if len(patched["members"].([]any)) != 1 {
		t.Fatalf("member replacement = %v", patched["members"])
	}

	// Validation (contract 7): the enum, the failure kinds, the positions,
	// the weights. Each is 400 with the field named, never a 500.
	ext := func(id string, pos int) map[string]any {
		return map[string]any{"extensionId": json.RawMessage(id), "position": pos}
	}
	bad := []struct {
		name string
		body map[string]any
	}{
		{"unknown strategy", map[string]any{"strategy": "loudest"}},
		{"empty members", map[string]any{"members": []map[string]any{}}},
		{"position below one", map[string]any{"members": []map[string]any{ext(m1, 0)}}},
		{"duplicate positions", map[string]any{"members": []map[string]any{ext(m1, 1), ext(m2, 1)}}},
		{"zero weight", map[string]any{"members": []map[string]any{{"extensionId": json.RawMessage(m1), "position": 1, "weight": 0}}}},
		{"negative weight", map[string]any{"members": []map[string]any{{"extensionId": json.RawMessage(m1), "position": 1, "weight": -2}}}},
		{"duplicate members", map[string]any{"members": []map[string]any{ext(m1, 1), ext(m1, 2)}}},
		{"timeout out of range", map[string]any{"ringTimeout": 2}},
		{"unknown failure kind", map[string]any{"failureKind": "voicemail-box"}},
		{"kind none keeps a target", map[string]any{"failureKind": "none"}},
		{"voicemail target not a number", map[string]any{"failureKind": "voicemail", "failureTarget": "sales"}},
		{"bad external target", map[string]any{"failureKind": "external", "failureTarget": "x1"}},
		{"bad name", map[string]any{"name": "not ok"}},
	}
	for _, b := range bad {
		r := c.do("PATCH", "/api/v1/ring-groups/"+id, b.body)
		if r.code != http.StatusBadRequest {
			t.Errorf("%s: PATCH = %d %s, want 400", b.name, r.code, r.body)
			continue
		}
		if f, ok := r.json(t)["error"].(map[string]any)["fields"].([]any); !ok || len(f) == 0 {
			t.Errorf("%s: no fields in %s", b.name, r.body)
		}
	}
	// Nothing above was stored.
	one = c.must(http.StatusOK, "GET", "/api/v1/ring-groups/"+id, nil).json(t)
	if one["strategy"] != "weighted" || len(one["members"].([]any)) != 1 {
		t.Fatalf("rejected changes leaked: %v", one)
	}
	// A nonexistent member is 404 from the store's FK mapping.
	r := c.do("PATCH", "/api/v1/ring-groups/"+id,
		map[string]any{"members": []map[string]any{ext("999999", 1)}})
	if r.code != http.StatusNotFound {
		t.Errorf("unknown member = %d, want 404", r.code)
	}
	// Create-time validation: members are required.
	if r := c.do("POST", "/api/v1/ring-groups", map[string]any{"name": "x"}); r.code != http.StatusBadRequest {
		t.Errorf("create without members = %d, want 400", r.code)
	}
	c.must(http.StatusNoContent, "DELETE", "/api/v1/ring-groups/"+id, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/ring-groups/"+id, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/ring-groups/999999", nil)
}

// TestFeatureCodeRoutes: list, full replacement, and the code/action
// validation. It fails if a code outside the schema's shape is stored.
func TestFeatureCodeRoutes(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	ctx := context.Background()
	c := e.login()

	var list struct {
		Items []map[string]any `json:"items"`
	}
	get := func() {
		t.Helper()
		if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/feature-codes", nil).body, &list); err != nil {
			t.Fatal(err)
		}
	}
	get()
	if len(list.Items) != 0 {
		t.Fatalf("fresh list = %v", list.Items)
	}
	put := func(items []map[string]any) response {
		return c.do("PUT", "/api/v1/feature-codes", map[string]any{"items": items})
	}
	got := c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*72", "action": "forward_always", "argument": "set"},
		{"code": "*97", "action": "voicemail"},
		{"code": "##", "action": "attended_transfer"},
	}}).json(t)
	if len(got["items"].([]any)) != 3 {
		t.Fatalf("put = %v", got)
	}
	rev0, err := e.st.ConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Replacement: codes absent from the list are removed.
	got = c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*78", "action": "dnd_on"},
	}}).json(t)
	items := got["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["code"] != "*78" {
		t.Fatalf("replacement = %v", items)
	}
	rev1, _ := e.st.ConfigRevision(ctx)
	if rev1 <= rev0 {
		t.Fatalf("revision %d did not move past %d", rev1, rev0)
	}
	for _, bad := range []map[string]any{
		{"code": "*1", "action": "dnd_on"},   // too short for the schema
		{"code": "*abc", "action": "dnd_on"}, // not the code shape
		{"code": "## ", "action": "dnd_on"},  // trailing space
		{"code": "*78", "action": "dial"},    // not an action
		{"code": "*78", "action": "dnd_on", "argument": "has space"},
	} {
		r := put([]map[string]any{bad})
		if r.code != http.StatusBadRequest {
			t.Errorf("bad item %v = %d %s, want 400", bad, r.code, r.body)
		}
	}
	// Duplicate codes in one PUT are rejected.
	if r := put([]map[string]any{{"code": "*78", "action": "dnd_on"}, {"code": "*78", "action": "dnd_off"}}); r.code != http.StatusBadRequest {
		t.Errorf("duplicate codes = %d %s, want 400", r.code, r.body)
	}
	// The rejected PUTs stored nothing new.
	get()
	if len(list.Items) != 1 || list.Items[0]["code"] != "*78" {
		t.Fatalf("list after rejects = %v", list.Items)
	}
}

// TestPresenceAPI reads the device states hello-sip publishes.
func TestPresenceAPI(t *testing.T) {
	addr := testValkeyAddr()
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	vc, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vc.Close)
	ctx := context.Background()
	if err := vc.Do(ctx, vc.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	live := livestate.New(vc)
	e := newEnv(t, live)
	c := e.login()

	if got := string(c.must(http.StatusOK, "GET", "/api/v1/presence", nil).body); got != "{\"items\":[]}\n" {
		t.Fatalf("empty presence = %s", got)
	}
	now := time.Now()
	for _, s := range []livestate.DeviceState{
		{Device: "101-desk", Extension: "101", State: "on-call", UpdatedAt: now},
		{Device: "102-desk", Extension: "102", State: "dnd", UpdatedAt: now},
	} {
		if err := live.SetDeviceState(ctx, s, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	var got struct {
		Items []livestate.DeviceState `json:"items"`
	}
	if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/presence", nil).body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("presence = %+v", got.Items)
	}
	byDevice := map[string]string{}
	for _, s := range got.Items {
		byDevice[s.Device] = s.State
	}
	if byDevice["101-desk"] != "on-call" || byDevice["102-desk"] != "dnd" {
		t.Fatalf("presence = %v", byDevice)
	}
}

// TestExtensionCallFeatures covers the PATCH /extensions/{id} Phase 4
// fields (contract 7).
func TestExtensionCallFeatures(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()
	path := fmt.Sprintf("/api/v1/extensions/%v", e.ext101["id"])

	got := c.must(http.StatusOK, "PATCH", path, map[string]any{"dnd": true, "voicemailEnabled": false}).json(t)
	if got["dnd"] != true || got["voicemailEnabled"] != false {
		t.Fatalf("patched = %v", got)
	}
	got = c.must(http.StatusOK, "PATCH", path, map[string]any{"forwardAlways": "+31201234567"}).json(t)
	if got["forwardAlways"] != "+31201234567" {
		t.Fatalf("forwardAlways = %v", got["forwardAlways"])
	}
	// Clearing a target.
	got = c.must(http.StatusOK, "PATCH", path, map[string]any{"forwardAlways": ""}).json(t)
	if got["forwardAlways"] != "" {
		t.Fatalf("cleared forwardAlways = %q", got["forwardAlways"])
	}
	// Forward targets and DND values are validated.
	for _, bad := range []map[string]any{
		{"forwardAlways": "call me"},
		{"forwardBusy": "!"},
		{"forwardNoAnswer": "1"},
		{"dnd": "yes"},
	} {
		if r := c.do("PATCH", path, bad); r.code != http.StatusBadRequest {
			t.Errorf("PATCH %v = %d %s, want 400", bad, r.code, r.body)
		}
	}
	got = c.must(http.StatusOK, "GET", path, nil).json(t)
	if got["dnd"] != true || got["voicemailEnabled"] != false || got["forwardBusy"] != "" {
		t.Fatalf("get = %v", got)
	}
}
