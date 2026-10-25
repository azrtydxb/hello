package api

// Authorization and behaviour tests for user accounts and extension
// self-service. They run through Handler over an in-memory store whose
// ownership rules mirror the SQL (internal/store covers the SQL on a real
// database), so they need no services.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// acctFake is a Store with users, extensions, boxes and messages in memory.
type acctFake struct {
	stubStore
	mu      sync.Mutex
	actors  map[string]auth.Actor // by session cookie value
	users   map[int64]*store.User
	hashes  map[int64]string
	links   map[int64][]int64 // user id -> extension ids
	exts    map[int64]*store.Extension
	boxes   map[int64]store.VoicemailBox // by extension id
	msgs    map[int64]store.VoicemailMessage
	calls   []store.CDR
	smtp    store.SMTPSendSettings
	touched []string // mutations that reached the store, "op id"
	audit   []string
	nextID  int64
}

func (f *acctFake) SessionActor(_ context.Context, h []byte) (auth.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.actors[hex.EncodeToString(h)]; ok {
		if a.UserID > 0 {
			if u, ok := f.users[a.UserID]; ok {
				a.Role, a.MustChangePassword = u.Role, u.MustChangePassword
				if !u.Enabled {
					return auth.Actor{}, auth.ErrNoCredentials
				}
			}
		}
		return a, nil
	}
	return auth.Actor{}, auth.ErrNoCredentials
}

func (f *acctFake) TokenActor(_ context.Context, h []byte) (auth.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.actors[hex.EncodeToString(h)]; ok && a.Kind == auth.KindPersonalToken {
		return a, nil
	}
	return auth.Actor{}, auth.ErrNoCredentials
}

func (f *acctFake) Audit(_ context.Context, actor, action, resource, id string) error {
	f.mu.Lock()
	f.audit = append(f.audit, actor+" "+action+" "+resource+" "+id)
	f.mu.Unlock()
	return nil
}

func (f *acctFake) GetUser(_ context.Context, id int64) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	c := *u
	c.ExtensionIDs = slices.Clone(f.links[id])
	c.ExtensionID = nil
	if len(c.ExtensionIDs) > 0 {
		c.ExtensionID = &c.ExtensionIDs[0]
	}
	if c.ExtensionIDs == nil {
		c.ExtensionIDs = []int64{}
	}
	return c, nil
}

func (f *acctFake) ListUsers(ctx context.Context) ([]store.User, error) {
	out := []store.User{}
	for id := int64(1); id <= f.nextID; id++ {
		if u, err := f.GetUser(ctx, id); err == nil {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *acctFake) UserForLogin(_ context.Context, name string) (store.Login, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, u := range f.users {
		if u.Username == name {
			return store.Login{ID: id, Hash: f.hashes[id], Enabled: u.Enabled, Role: u.Role}, nil
		}
	}
	return store.Login{}, store.ErrNotFound
}

func (f *acctFake) CreateSession(context.Context, int64, []byte, time.Time) error { return nil }
func (f *acctFake) UpgradePasswordHash(_ context.Context, id int64, _, nh string) error {
	f.mu.Lock()
	f.hashes[id] = nh
	f.mu.Unlock()
	return nil
}

func (f *acctFake) UserPasswordHash(_ context.Context, id int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hashes[id], nil
}

func (f *acctFake) ChangeOwnPassword(_ context.Context, id int64, nh string, _ []byte) error {
	f.mu.Lock()
	f.hashes[id] = nh
	f.users[id].MustChangePassword = false
	f.mu.Unlock()
	return nil
}

func (f *acctFake) UpdateProfile(_ context.Context, _ string, id int64, c store.ProfileChange) (store.User, error) {
	f.mu.Lock()
	u := f.users[id]
	if c.DisplayName != nil {
		u.DisplayName = *c.DisplayName
	}
	if c.Email != nil {
		u.Email = *c.Email
	}
	if c.Voicemail != nil {
		u.VoicemailEmail = *c.Voicemail
	}
	f.mu.Unlock()
	return f.GetUser(context.Background(), id)
}

func (f *acctFake) enabledAdmins() int {
	n := 0
	for _, u := range f.users {
		if u.Role == auth.RoleAdmin && u.Enabled {
			n++
		}
	}
	return n
}

func (f *acctFake) CreateAccount(_ context.Context, _ string, in store.NewAccount) (store.User, error) {
	f.mu.Lock()
	for _, u := range f.users {
		if u.Username == in.Username {
			f.mu.Unlock()
			return store.User{}, store.ErrConflict
		}
	}
	for _, e := range in.ExtensionIDs {
		if _, ok := f.exts[e]; !ok {
			f.mu.Unlock()
			return store.User{}, &store.ValidationError{Fields: []routing.FieldError{{Path: "extensionIds", Message: "no such extension"}}}
		}
		for _, l := range f.links {
			if slices.Contains(l, e) {
				f.mu.Unlock()
				return store.User{}, &store.InUseError{Msg: "extension is already linked"}
			}
		}
	}
	f.nextID++
	id := f.nextID
	f.users[id] = &store.User{ID: id, Username: in.Username, DisplayName: in.DisplayName, Email: in.Email, AuthMethod: "local",
		Role: in.Role, Enabled: true, MustChangePassword: in.MustChange, VoicemailEmail: in.Voicemail}
	f.hashes[id] = in.PasswordHash
	f.links[id] = in.ExtensionIDs
	f.mu.Unlock()
	return f.GetUser(context.Background(), id)
}

func (f *acctFake) UpdateAccount(_ context.Context, _ string, id int64, c store.AccountChange) (store.User, error) {
	f.mu.Lock()
	u, ok := f.users[id]
	if !ok {
		f.mu.Unlock()
		return store.User{}, store.ErrNotFound
	}
	role, enabled := u.Role, u.Enabled
	if c.Role != nil {
		role = *c.Role
	}
	if c.Enabled != nil {
		enabled = *c.Enabled
	}
	if u.Role == auth.RoleAdmin && u.Enabled && (role != auth.RoleAdmin || !enabled) && f.enabledAdmins() <= 1 {
		f.mu.Unlock()
		return store.User{}, store.ErrLastAdmin
	}
	u.Role, u.Enabled = role, enabled
	if c.PasswordHash != nil {
		f.hashes[id], u.MustChangePassword = *c.PasswordHash, c.MustChange
	}
	if c.Email != nil {
		u.Email = *c.Email
	}
	if c.ExtensionIDs != nil {
		f.links[id] = *c.ExtensionIDs
	}
	if c.Voicemail != nil {
		u.VoicemailEmail = *c.Voicemail
	}
	f.mu.Unlock()
	return f.GetUser(context.Background(), id)
}

func (f *acctFake) DeleteUser(_ context.Context, _ string, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return store.ErrNotFound
	}
	if u.Role == auth.RoleAdmin && u.Enabled && f.enabledAdmins() <= 1 {
		return store.ErrLastAdmin
	}
	delete(f.users, id)
	return nil
}

// owns mirrors the user_extensions join every self-service read goes through.
func (f *acctFake) owns(uid, ext int64) bool { return slices.Contains(f.links[uid], ext) }

func (f *acctFake) UserExtensions(_ context.Context, uid int64) ([]store.Extension, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Extension{}
	for _, e := range f.links[uid] {
		out = append(out, *f.exts[e])
	}
	return out, nil
}

func (f *acctFake) UserExtension(_ context.Context, uid, id int64) (store.Extension, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.owns(uid, id) {
		return store.Extension{}, store.ErrNotFound
	}
	return *f.exts[id], nil
}

func (f *acctFake) UserVoicemailMessage(_ context.Context, uid, id int64) (store.VoicemailMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.msgs[id]
	if !ok {
		return m, store.ErrNotFound
	}
	for ext, b := range f.boxes {
		if b.ID == m.BoxID && f.owns(uid, ext) {
			return m, nil
		}
	}
	return store.VoicemailMessage{}, store.ErrNotFound
}

func (f *acctFake) UserCDRs(_ context.Context, uid, _ int64, limit int) ([]store.CDR, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, "calls "+strconv.FormatInt(uid, 10))
	var out []store.CDR
	for _, c := range f.calls {
		for _, e := range f.links[uid] {
			if n := f.exts[e].Number; c.Source == n || c.Destination == n {
				out = append(out, c)
			}
		}
	}
	return out[:min(limit, len(out))], "", nil
}

func (f *acctFake) UpdateExtension(_ context.Context, _ string, id int64, c store.ExtensionChange, _ store.Check) (store.Extension, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, "ext "+strconv.FormatInt(id, 10))
	e := f.exts[id]
	if c.Name != nil {
		e.Name = *c.Name
	}
	if c.DND != nil {
		e.DND = *c.DND
	}
	if c.ForwardAlways != nil {
		e.ForwardAlways = *c.ForwardAlways
	}
	return *e, nil
}

func (f *acctFake) GetVoicemailBox(_ context.Context, ext int64) (store.VoicemailBox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.boxes[ext]
	if !ok {
		return b, store.ErrNotFound
	}
	return b, nil
}

func (f *acctFake) UpdateVoicemailBox(_ context.Context, _ string, ext int64, c store.VoicemailBoxChange, _ store.Check) (store.VoicemailBox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, "box "+strconv.FormatInt(ext, 10))
	b := f.boxes[ext]
	if c.Password != nil {
		b.HasPassword = true
	}
	if c.Greeting != nil {
		b.GreetingObject = *c.Greeting
	}
	f.boxes[ext] = b
	return b, nil
}

func (f *acctFake) ListVoicemailMessages(_ context.Context, box int64, _ bool) ([]store.VoicemailMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.VoicemailMessage
	for _, m := range f.msgs {
		if m.BoxID == box {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *acctFake) GetVoicemailMessage(_ context.Context, id int64) (store.VoicemailMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.msgs[id]
	if !ok {
		return m, store.ErrNotFound
	}
	return m, nil
}

func (f *acctFake) UpdateVoicemailMessageHeard(_ context.Context, _ string, id int64, heard bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, "heard "+strconv.FormatInt(id, 10))
	m := f.msgs[id]
	m.Heard = heard
	f.msgs[id] = m
	return nil
}

func (f *acctFake) DeleteVoicemailMessage(_ context.Context, _ string, id int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, "delete "+strconv.FormatInt(id, 10))
	m := f.msgs[id]
	delete(f.msgs, id)
	return m.Object, nil
}

func (f *acctFake) GetSMTPSettings(context.Context) (store.SMTPSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.smtp.SMTPSettings, nil
}

func (f *acctFake) SMTPSendSettings(context.Context) (store.SMTPSendSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.smtp, nil
}

func (f *acctFake) PutSMTPSettings(_ context.Context, _ string, in store.SMTPInput) (store.SMTPSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.smtp.Host, f.smtp.Port, f.smtp.Security, f.smtp.Username, f.smtp.From = in.Host, in.Port, in.Security, in.Username, in.From
	if in.Password != nil {
		f.smtp.Password, f.smtp.PasswordSet = *in.Password, *in.Password != ""
	}
	f.smtp.UpdatedAt = time.Now()
	return f.smtp.SMTPSettings, nil
}

// audioFake serves fixed bytes for any object, or fails.
type audioFake struct {
	Objects
	failOpen, failPut bool
	puts              []string
	removed           []string
}

type nopSeeker struct{ *bytes.Reader }

func (nopSeeker) Close() error { return nil }

func (a *audioFake) OpenAudio(context.Context, string, string) (*AudioObject, error) {
	if a.failOpen {
		return nil, errors.New("minio down")
	}
	b := bytes.Repeat([]byte{7}, 100)
	return &AudioObject{Body: nopSeeker{bytes.NewReader(b)}, Size: int64(len(b)), ContentType: "audio/wav", ModTime: time.Unix(0, 0)}, nil
}

func (a *audioFake) Put(_ context.Context, o string, _ io.Reader, _ int64) error {
	if a.failPut {
		return errors.New("minio down")
	}
	a.puts = append(a.puts, o)
	return nil
}

func (a *audioFake) Remove(_ context.Context, o string) error {
	a.removed = append(a.removed, o)
	return nil
}

// Fixture: alice (user, extension 10) and bob (user, extension 20), an
// extension 30 nobody has, carol (admin), dave (viewer), and a user with no
// extension.
const (
	extAlice, extBob, extFree     = 10, 20, 30
	boxAlice, boxBob              = 100, 200
	msgAlice, msgBob, msgFree     = 1000, 2000, 3000
	uAlice, uBob, uCarol, uDave   = 1, 2, 3, 4
	uNoExt, uMust, uDisabled      = 5, 6, 7
	cookieSuffix                  = "-cookie"
	strongPassword, newStrongPass = "correct horse battery", "another long passphrase"
)

func newAcctFake(t *testing.T) (*acctFake, http.Handler, *audioFake, *bytes.Buffer) {
	t.Helper()
	hash, err := auth.HashUserPassword(strongPassword)
	if err != nil {
		t.Fatal(err)
	}
	f := &acctFake{
		actors: map[string]auth.Actor{}, users: map[int64]*store.User{}, hashes: map[int64]string{}, links: map[int64][]int64{},
		exts:  map[int64]*store.Extension{},
		boxes: map[int64]store.VoicemailBox{},
		msgs:  map[int64]store.VoicemailMessage{},
	}
	for _, e := range []store.Extension{{ID: extAlice, Number: "101", Name: "Alice", ExternalNumber: "+3211111", AllowIntercom: true},
		{ID: extBob, Number: "102", Name: "Bob"}, {ID: extFree, Number: "103", Name: "Free"}} {
		e := e
		f.exts[e.ID] = &e
	}
	f.boxes[extAlice] = store.VoicemailBox{ID: boxAlice, ExtensionID: extAlice, Email: "secret-box@example.com", GreetingObject: "box/10/g.wav"}
	f.boxes[extBob] = store.VoicemailBox{ID: boxBob, ExtensionID: extBob}
	f.msgs[msgAlice] = store.VoicemailMessage{ID: msgAlice, BoxID: boxAlice, Caller: "+3200", DurationMs: 5000, EmailStatus: "sent", Object: "box/10/m.wav"}
	f.msgs[msgBob] = store.VoicemailMessage{ID: msgBob, BoxID: boxBob, Caller: "+3201", DurationMs: 6000, EmailStatus: "pending", Object: "box/20/m.wav"}
	f.msgs[msgFree] = store.VoicemailMessage{ID: msgFree, BoxID: 300, Caller: "+3202", EmailStatus: "pending", Object: "box/30/m.wav"}
	f.calls = []store.CDR{
		{ID: 1, Source: "101", Destination: "+3299", Direction: "outbound", Kind: "call", Trunk: "secret-trunk", Route: "secret-route", StartTime: time.Now()},
		{ID: 2, Source: "102", Destination: "+3298", Direction: "outbound", Kind: "call", Trunk: "secret-trunk", StartTime: time.Now()},
	}
	add := func(id int64, name string, role auth.Role, exts ...int64) {
		f.users[id] = &store.User{ID: id, Username: name, AuthMethod: "local", Role: role, Enabled: true,
			VoicemailEmail: store.VoicemailEmailPrefs{Notify: true, Attach: true, After: "none"}}
		f.hashes[id] = hash
		f.links[id] = exts
		f.actors[hex.EncodeToString(auth.HashToken(name+cookieSuffix))] = auth.Actor{UserID: id, Username: name, Role: role}
		if id > f.nextID {
			f.nextID = id
		}
	}
	add(uAlice, "alice", auth.RoleUser, extAlice)
	add(uBob, "bob", auth.RoleUser, extBob)
	add(uCarol, "carol", auth.RoleAdmin)
	add(uDave, "dave", auth.RoleViewer)
	add(uNoExt, "noext", auth.RoleUser)
	add(uMust, "must", auth.RoleUser, 0)
	f.links[uMust] = nil
	f.users[uMust].MustChangePassword = true
	add(uDisabled, "gone", auth.RoleUser)
	f.users[uDisabled].Enabled = false
	f.actors[hex.EncodeToString(auth.HashToken("svc"+cookieSuffix))] = auth.Actor{UserID: 0, Username: "", Role: auth.RoleUser}
	objs := &audioFake{}
	logs := &bytes.Buffer{}
	h := Handler(Config{Store: f, Objects: objs, Log: slog.New(slog.NewTextHandler(logs, nil)), SessionTTL: time.Hour,
		SMTPDeliver: func(context.Context, mailer.Settings, string, []byte) error { return nil }})
	return f, h, objs, logs
}

func acctCall(h http.Handler, who, method, path string, body any) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	if who != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: who + cookieSuffix})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func wantJSON(t *testing.T, rec *httptest.ResponseRecorder, status int, ctx string) map[string]any {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("%s: status %d %s, want %d", ctx, rec.Code, rec.Body, status)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return m
}

func acctErrCode(rec *httptest.ResponseRecorder) string {
	var b struct{ Error struct{ Code string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	return b.Error.Code
}

func nextOf(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, v := range m["items"].([]any) {
		out = append(out, v.(map[string]any))
	}
	return out
}

// TestSelfServiceIsolation fails if a user with role user can reach any
// route that is not a self-service route, or reach another extension's data
// through any /me route, including by guessing extension or message ids.
func TestSelfServiceIsolation(t *testing.T) {
	f, h, _, _ := newAcctFake(t)

	t.Run("a user is refused every route but the self-service ones", func(t *testing.T) {
		for _, rt := range Routes() {
			if rt.Public || rt.Role == auth.RoleUser {
				continue
			}
			path := strings.NewReplacer("{id}", "1", "{vendor}", "yealink", "{ip}", "1.2.3.4", "{name}", "x", "{entryId}", "1", "{secretId}", "1").Replace(rt.Pattern)
			rec := acctCall(h, "alice", rt.Method, path, nil)
			if rec.Code != http.StatusForbidden || acctErrCode(rec) != "forbidden_role" {
				t.Errorf("alice (user) %s %s = %d %s, want 403 forbidden_role", rt.Method, rt.Pattern, rec.Code, rec.Body)
			}
		}
	})

	t.Run("alice sees only her own extension and nothing of its system settings", func(t *testing.T) {
		m := wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/me/extensions", nil), 200, "list")
		got := nextOf(m)
		if len(got) != 1 || got[0]["number"] != "101" {
			t.Fatalf("extensions = %v", got)
		}
		for _, leak := range []string{"externalNumber", "allowIntercom", "recordDefault", "directoryHidden", "secret", "password"} {
			if _, ok := got[0][leak]; ok {
				t.Errorf("self extension view carries %q", leak)
			}
		}
		me := wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/auth/me", nil), 200, "me")
		if me["role"] != "user" || len(me["extensions"].([]any)) != 1 {
			t.Fatalf("me = %v", me)
		}
	})

	t.Run("other extensions are 404 on every extension route", func(t *testing.T) {
		for _, ext := range []int{extBob, extFree, 99999, 0, -1} {
			base := fmt.Sprintf("/api/v1/me/extensions/%d", ext)
			for _, c := range []struct{ method, path string }{
				{"PATCH", base}, {"GET", base + "/voicemail"}, {"PUT", base + "/voicemail"}, {"GET", base + "/voicemail/messages"},
			} {
				var body any
				if c.method == "PATCH" {
					body = map[string]any{"dnd": true}
				}
				if c.method == "PUT" {
					body = map[string]any{"pin": "1234"}
				}
				rec := acctCall(h, "alice", c.method, c.path, body)
				if rec.Code != http.StatusNotFound {
					t.Errorf("%s %s = %d %s, want 404", c.method, c.path, rec.Code, rec.Body)
				}
			}
		}
		if len(f.touched) != 0 {
			t.Fatalf("a refused request reached the store: %v", f.touched)
		}
		if f.exts[extBob].DND || f.boxes[extBob].HasPassword {
			t.Fatal("bob's data changed")
		}
	})

	t.Run("message ids cannot be guessed", func(t *testing.T) {
		for id := 1; id <= 3100; id++ {
			for _, c := range []struct{ method, suffix string }{{"GET", "/audio"}, {"POST", "/heard"}, {"DELETE", ""}} {
				if id == msgAlice && c.method == "DELETE" {
					continue // her own: tested below
				}
				rec := acctCall(h, "alice", c.method, fmt.Sprintf("/api/v1/me/voicemail/messages/%d%s", id, c.suffix), nil)
				switch {
				case id == msgAlice && c.method == "GET":
					if rec.Code != 200 {
						t.Fatalf("own audio = %d", rec.Code)
					}
				case id == msgAlice && c.method == "POST":
					if rec.Code != 200 {
						t.Fatalf("own heard = %d", rec.Code)
					}
				case rec.Code != http.StatusNotFound:
					t.Fatalf("%s message %d%s = %d, want 404", c.method, id, c.suffix, rec.Code)
				}
			}
		}
		for _, op := range f.touched {
			if op != "heard "+strconv.Itoa(msgAlice) {
				t.Fatalf("store mutation %q reached with a foreign id", op)
			}
		}
		for _, id := range []string{"abc", "-5", "0", "1e3", "99999999999999999999"} {
			if rec := acctCall(h, "alice", "DELETE", "/api/v1/me/voicemail/messages/"+id, nil); rec.Code != http.StatusNotFound {
				t.Errorf("DELETE message %q = %d", id, rec.Code)
			}
		}
		if _, ok := f.msgs[msgBob]; !ok {
			t.Fatal("bob's message was deleted")
		}
	})

	t.Run("lists are scoped to the caller", func(t *testing.T) {
		m := wantJSON(t, acctCall(h, "alice", "GET", fmt.Sprintf("/api/v1/me/extensions/%d/voicemail/messages", extAlice), nil), 200, "messages")
		if got := nextOf(m); len(got) != 1 || got[0]["id"] != float64(msgAlice) {
			t.Fatalf("alice's messages = %v", got)
		}
		m = wantJSON(t, acctCall(h, "bob", "GET", fmt.Sprintf("/api/v1/me/extensions/%d/voicemail/messages", extBob), nil), 200, "bob messages")
		if got := nextOf(m); len(got) != 1 || got[0]["id"] != float64(msgBob) {
			t.Fatalf("bob's messages = %v", got)
		}
		calls := wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/me/calls", nil), 200, "calls")
		got := nextOf(calls)
		if len(got) != 1 || got[0]["source"] != "101" {
			t.Fatalf("alice's calls = %v", got)
		}
		for _, leak := range []string{"trunk", "route", "sipCallId", "correlationId", "failureReason"} {
			if _, ok := got[0][leak]; ok {
				t.Errorf("call view carries %q", leak)
			}
		}
		box := wantJSON(t, acctCall(h, "alice", "GET", fmt.Sprintf("/api/v1/me/extensions/%d/voicemail", extAlice), nil), 200, "box")
		if box["hasGreeting"] != true || box["hasPin"] != false {
			t.Fatalf("box = %v", box)
		}
		if strings.Contains(fmt.Sprint(box), "secret-box@example.com") || strings.Contains(fmt.Sprint(box), "box/10") {
			t.Fatalf("box view leaks the admin's address or object keys: %v", box)
		}
	})

	t.Run("a user without an extension has nothing", func(t *testing.T) {
		wantJSON(t, acctCall(h, "noext", "GET", "/api/v1/me/extensions", nil), 200, "list")
		if m := nextOf(wantJSON(t, acctCall(h, "noext", "GET", "/api/v1/me/extensions", nil), 200, "list")); len(m) != 0 {
			t.Fatalf("extensions = %v", m)
		}
		for _, ext := range []int{extAlice, extBob} {
			if rec := acctCall(h, "noext", "GET", fmt.Sprintf("/api/v1/me/extensions/%d/voicemail", ext), nil); rec.Code != 404 {
				t.Errorf("ext %d = %d", ext, rec.Code)
			}
		}
		wantJSON(t, acctCall(h, "noext", "GET", "/api/v1/me/calls", nil), 200, "calls")
	})

	t.Run("a credential that is not a user is refused", func(t *testing.T) {
		for _, p := range []string{"/api/v1/me/extensions", "/api/v1/me/calls", "/api/v1/me/extensions/10/voicemail"} {
			if rec := acctCall(h, "svc", "GET", p, nil); rec.Code != 403 || acctErrCode(rec) != "no_user" {
				t.Errorf("GET %s = %d %s", p, rec.Code, rec.Body)
			}
		}
		if rec := acctCall(h, "svc", "PATCH", "/api/v1/me/profile", map[string]any{"email": "a@b.co"}); rec.Code != 403 {
			t.Errorf("profile = %d", rec.Code)
		}
		if rec := acctCall(h, "svc", "PATCH", "/api/v1/me/extensions/10", map[string]any{"dnd": true}); rec.Code != 403 {
			t.Errorf("ext = %d", rec.Code)
		}
		if rec := acctCall(h, "svc", "GET", "/api/v1/me/extensions/10/voicemail/messages", nil); rec.Code != 403 {
			t.Errorf("messages = %d", rec.Code)
		}
		for _, c := range [][2]string{{"POST", "/api/v1/me/voicemail/messages/1000/heard"}, {"DELETE", "/api/v1/me/voicemail/messages/1000"}, {"GET", "/api/v1/me/voicemail/messages/1000/audio"}, {"PUT", "/api/v1/me/extensions/10/voicemail"}} {
			if rec := acctCall(h, "svc", c[0], c[1], nil); rec.Code != 403 {
				t.Errorf("%s %s = %d", c[0], c[1], rec.Code)
			}
		}
	})

	t.Run("anonymous requests are refused", func(t *testing.T) {
		for _, p := range []string{"/api/v1/me/extensions", "/api/v1/me/calls", "/api/v1/auth/me"} {
			if rec := acctCall(h, "", "GET", p, nil); rec.Code != 401 {
				t.Errorf("GET %s = %d", p, rec.Code)
			}
		}
	})
}

// TestSelfServiceActions fails if the user's own address book, voicemail,
// forwarding, history or profile cannot be managed, or a bad value is
// accepted.
func TestSelfServiceActions(t *testing.T) {
	f, h, objs, _ := newAcctFake(t)
	ext := fmt.Sprintf("/api/v1/me/extensions/%d", extAlice)

	t.Run("label, DND and forwarding", func(t *testing.T) {
		got := wantJSON(t, acctCall(h, "alice", "PATCH", ext, map[string]any{"name": " Alice B ", "dnd": true, "forwardAlways": "+3212345"}), 200, "patch")
		if got["name"] != "Alice B" || got["dnd"] != true || got["forwardAlways"] != "+3212345" {
			t.Fatalf("extension = %v", got)
		}
		for _, c := range []struct {
			body map[string]any
			code int
		}{
			{map[string]any{}, 400},
			{map[string]any{"forwardBusy": "not a number"}, 400},
			{map[string]any{"name": ""}, 400},
			{map[string]any{"externalNumber": "+3299"}, 400},
			{map[string]any{"allowIntercom": false}, 400},
			{map[string]any{"number": "999"}, 400},
		} {
			if rec := acctCall(h, "alice", "PATCH", ext, c.body); rec.Code != c.code {
				t.Errorf("%v = %d %s, want %d", c.body, rec.Code, rec.Body, c.code)
			}
		}
		if f.exts[extAlice].ExternalNumber != "+3211111" || f.exts[extAlice].Number != "101" {
			t.Fatal("a field the user may not change was changed")
		}
	})

	t.Run("voicemail PIN and greetings", func(t *testing.T) {
		got := wantJSON(t, acctCall(h, "alice", "PUT", ext+"/voicemail", map[string]any{"pin": "4321"}), 200, "pin")
		if got["hasPin"] != true {
			t.Fatalf("box = %v", got)
		}
		for _, pin := range []string{"12", "12ab", "12345678901", "abcd"} {
			if rec := acctCall(h, "alice", "PUT", ext+"/voicemail", map[string]any{"pin": pin}); rec.Code != 400 {
				t.Errorf("pin %q = %d", pin, rec.Code)
			}
		}
		// The admin's fields are not the user's.
		for _, body := range []map[string]any{{"password": "4321"}, {"email": "x@example.com"}, {}} {
			if rec := acctCall(h, "alice", "PUT", ext+"/voicemail", body); rec.Code != 400 {
				t.Errorf("%v = %d, want 400", body, rec.Code)
			}
		}
		put := func(parts map[string][]byte) *httptest.ResponseRecorder {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			for name, data := range parts {
				w, _ := mw.CreateFormFile(name, name+".wav")
				_, _ = w.Write(data)
			}
			_ = mw.Close()
			r := httptest.NewRequest("PUT", ext+"/voicemail", &buf)
			r.Header.Set("Content-Type", mw.FormDataContentType())
			r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "alice" + cookieSuffix})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			return rec
		}
		wav := append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 64)...)
		if rec := put(map[string][]byte{"greeting": wav}); rec.Code != 200 || len(objs.puts) != 1 || !strings.HasPrefix(objs.puts[0], fmt.Sprintf("box/%d/greeting-", extAlice)) {
			t.Fatalf("greeting upload = %d %s puts %v", rec.Code, rec.Body, objs.puts)
		}
		if len(objs.removed) != 1 || objs.removed[0] != "box/10/g.wav" {
			t.Fatalf("the replaced greeting was not removed: %v", objs.removed)
		}
		if rec := put(map[string][]byte{"greeting": []byte("not a wav at all")}); rec.Code != 400 {
			t.Errorf("non-WAV greeting = %d", rec.Code)
		}
		objs.failPut = true
		if rec := put(map[string][]byte{"greeting": wav}); rec.Code != 502 {
			t.Errorf("object store failure = %d", rec.Code)
		}
		objs.failPut = false
	})

	t.Run("messages: list, play, download, heard, delete", func(t *testing.T) {
		m := wantJSON(t, acctCall(h, "alice", "GET", ext+"/voicemail/messages?unheard=true", nil), 200, "list")
		if len(nextOf(m)) != 1 {
			t.Fatalf("messages = %v", m)
		}
		if rec := acctCall(h, "alice", "GET", ext+"/voicemail/messages?unheard=maybe", nil); rec.Code != 400 {
			t.Errorf("bad unheard = %d", rec.Code)
		}
		audio := "/api/v1/me/voicemail/messages/1000/audio"
		rec := acctCall(h, "alice", "GET", audio, nil)
		if rec.Code != 200 || rec.Header().Get("Content-Disposition") != "" || rec.Body.Len() != 100 {
			t.Fatalf("play = %d %v", rec.Code, rec.Header())
		}
		rec = acctCall(h, "alice", "GET", audio+"?download=1", nil)
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), `filename="voicemail-1000.wav"`) {
			t.Fatalf("download = %d %v", rec.Code, rec.Header())
		}
		r := httptest.NewRequest("GET", audio, nil)
		r.Header.Set("Range", "bytes=0-9")
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "alice" + cookieSuffix})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		if rr.Code != http.StatusPartialContent || rr.Body.Len() != 10 {
			t.Fatalf("range = %d len %d", rr.Code, rr.Body.Len())
		}
		objs.failOpen = true
		if rec := acctCall(h, "alice", "GET", audio, nil); rec.Code != 502 {
			t.Errorf("object store down = %d", rec.Code)
		}
		objs.failOpen = false
		got := wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/voicemail/messages/1000/heard", map[string]any{"heard": true}), 200, "heard")
		if got["heard"] != true || !f.msgs[msgAlice].Heard {
			t.Fatalf("heard = %v", got)
		}
		if rec := acctCall(h, "alice", "POST", "/api/v1/me/voicemail/messages/1000/heard", map[string]any{"heard": "yes"}); rec.Code != 400 {
			t.Errorf("bad heard = %d", rec.Code)
		}
		if rec := acctCall(h, "alice", "DELETE", "/api/v1/me/voicemail/messages/1000", nil); rec.Code != 204 {
			t.Fatalf("delete = %d", rec.Code)
		}
		if _, ok := f.msgs[msgAlice]; ok || len(objs.removed) < 2 || objs.removed[len(objs.removed)-1] != "box/10/m.wav" {
			t.Fatalf("message or audio not removed: %v", objs.removed)
		}
	})

	t.Run("call history paging parameters", func(t *testing.T) {
		if rec := acctCall(h, "alice", "GET", "/api/v1/me/calls?limit=0", nil); rec.Code != 400 {
			t.Errorf("limit 0 = %d", rec.Code)
		}
	})

	t.Run("profile and voicemail email options", func(t *testing.T) {
		got := wantJSON(t, acctCall(h, "alice", "PATCH", "/api/v1/me/profile", map[string]any{
			"displayName": "Alice A", "email": "alice@example.com", "voicemailEmail": map[string]any{"attach": false, "after": "mark_read"}}), 200, "profile")
		prefs := got["voicemailEmail"].(map[string]any)
		if got["email"] != "alice@example.com" || got["displayName"] != "Alice A" || prefs["attach"] != false || prefs["notify"] != true || prefs["after"] != "mark_read" {
			t.Fatalf("profile = %v", got)
		}
		for _, body := range []map[string]any{{}, {"email": "not-an-email"}, {"voicemailEmail": map[string]any{"after": "burn"}},
			{"role": "admin"}, {"enabled": false}, {"extensionIds": []int{20}}} {
			if rec := acctCall(h, "alice", "PATCH", "/api/v1/me/profile", body); rec.Code != 400 {
				t.Errorf("%v = %d, want 400", body, rec.Code)
			}
		}
		if f.users[uAlice].Role != auth.RoleUser || !slices.Equal(f.links[uAlice], []int64{extAlice}) {
			t.Fatal("a user changed their own role or extensions")
		}
	})
}

// TestPasswordLifecycle fails if an admin-set password does not force a
// change before anything else works, a user can change their password
// without the current one, a weak or unchanged one is accepted, a token
// (not a session) may change it, or a disabled user can sign in.
func TestPasswordLifecycle(t *testing.T) {
	f, h, _, _ := newAcctFake(t)

	t.Run("forced change blocks everything else", func(t *testing.T) {
		for _, p := range []string{"/api/v1/me/extensions", "/api/v1/me/calls", "/api/v1/me/contacts"} {
			if rec := acctCall(h, "must", "GET", p, nil); rec.Code != 403 || acctErrCode(rec) != "password_change_required" {
				t.Errorf("GET %s = %d %s", p, rec.Code, rec.Body)
			}
		}
		// The gate holds for every role, a viewer's whole read access included.
		f.users[uDave].MustChangePassword = true
		if rec := acctCall(h, "dave", "GET", "/api/v1/extensions", nil); rec.Code != 403 || acctErrCode(rec) != "password_change_required" {
			t.Errorf("viewer with a forced change: GET /extensions = %d %s", rec.Code, rec.Body)
		}
		f.users[uDave].MustChangePassword = false
		me := wantJSON(t, acctCall(h, "must", "GET", "/api/v1/auth/me", nil), 200, "me")
		if me["mustChangePassword"] != true {
			t.Fatalf("me = %v", me)
		}
	})

	t.Run("change", func(t *testing.T) {
		for _, c := range []struct {
			body map[string]any
			code int
		}{
			{map[string]any{"currentPassword": "wrong", "newPassword": newStrongPass}, 400},
			{map[string]any{"currentPassword": strongPassword, "newPassword": "short"}, 400},
			{map[string]any{"currentPassword": strongPassword, "newPassword": strongPassword}, 400},
			{map[string]any{"newPassword": newStrongPass}, 400},
			{map[string]any{"currentPassword": strongPassword, "newPassword": strings.Repeat("x", 200)}, 400},
		} {
			if rec := acctCall(h, "must", "POST", "/api/v1/auth/password", c.body); rec.Code != c.code {
				t.Errorf("%v = %d %s", c.body, rec.Code, rec.Body)
			}
		}
		if rec := acctCall(h, "must", "POST", "/api/v1/auth/password", map[string]any{"currentPassword": strongPassword, "newPassword": newStrongPass}); rec.Code != 204 {
			t.Fatalf("change = %d %s", rec.Code, rec.Body)
		}
		if !auth.CheckUserPassword(f.hashes[uMust], newStrongPass) || !strings.HasPrefix(f.hashes[uMust], "$argon2id$") {
			t.Fatal("password not stored as argon2id of the new one")
		}
		wantJSON(t, acctCall(h, "must", "GET", "/api/v1/me/extensions", nil), 200, "after change")
	})

	t.Run("only a session may change it", func(t *testing.T) {
		f.mu.Lock()
		f.actors[hex.EncodeToString(auth.HashToken("pat"))] = auth.Actor{UserID: uAlice, Username: "alice", Role: auth.RoleUser, Kind: auth.KindPersonalToken, Scopes: auth.Scopes{auth.ScopeRead, auth.ScopeWrite}}
		f.mu.Unlock()
		// Tokens resolve through TokenActor, which the fake answers for this one.
		r := httptest.NewRequest("POST", "/api/v1/auth/password", strings.NewReader(`{"currentPassword":"`+strongPassword+`","newPassword":"`+newStrongPass+`"}`))
		r.Header.Set("Authorization", "Bearer pat")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 403 {
			t.Fatalf("token password change = %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("login", func(t *testing.T) {
		login := func(user, pw string) int {
			return acctCall(h, "", "POST", "/api/v1/auth/login", map[string]any{"username": user, "password": pw}).Code
		}
		if c := login("alice", strongPassword); c != 204 {
			t.Errorf("login = %d", c)
		}
		if c := login("alice", "wrong password!"); c != 401 {
			t.Errorf("wrong password = %d", c)
		}
		if c := login("gone", strongPassword); c != 401 {
			t.Errorf("disabled user = %d", c)
		}
		if c := login("nobody", strongPassword); c != 401 {
			t.Errorf("unknown user = %d", c)
		}
		// A legacy bcrypt hash still logs in and is upgraded.
		legacy, _ := auth.HashPassword("legacy password 1")
		f.mu.Lock()
		f.hashes[uBob] = legacy
		f.mu.Unlock()
		if c := login("bob", "legacy password 1"); c != 204 {
			t.Fatalf("legacy login = %d", c)
		}
		if !strings.HasPrefix(f.hashes[uBob], "$argon2id$") {
			t.Fatal("legacy hash not upgraded to argon2id")
		}
		// A disabled user's open session stops working.
		if rec := acctCall(h, "gone", "GET", "/api/v1/auth/me", nil); rec.Code != 401 {
			t.Errorf("disabled user's session = %d", rec.Code)
		}
	})
}

// TestUserAdmin fails if an admin cannot create, edit, disable or delete a
// user, the API accepts a weak password or a bad field, answers with a
// password or hash, or lets the last admin go.
func TestUserAdmin(t *testing.T) {
	f, h, _, logs := newAcctFake(t)
	create := func(body map[string]any) *httptest.ResponseRecorder {
		return acctCall(h, "carol", "POST", "/api/v1/users", body)
	}

	t.Run("create", func(t *testing.T) {
		rec := create(map[string]any{"username": "erin@example.com", "role": "user", "displayName": "Erin", "email": "erin@example.com",
			"password": "initial password 1", "extensionIds": []int{extFree}, "voicemailEmail": map[string]any{"attach": false}})
		got := wantJSON(t, rec, 201, "create")
		for _, banned := range []string{"password", "hash", "initial password 1", "$argon2"} {
			if strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(banned)) && banned != "password" {
				t.Errorf("response contains %q: %s", banned, rec.Body)
			}
		}
		if _, ok := got["password"]; ok || got["role"] != "user" || got["mustChangePassword"] != true || got["extensionId"] != float64(extFree) {
			t.Fatalf("user = %v", got)
		}
		id := int64(got["id"].(float64))
		if !strings.HasPrefix(f.hashes[id], "$argon2id$") || !auth.CheckUserPassword(f.hashes[id], "initial password 1") {
			t.Fatal("initial password not stored as argon2id")
		}
		if p := got["voicemailEmail"].(map[string]any); p["attach"] != false || p["notify"] != true {
			t.Fatalf("prefs = %v", p)
		}
		if strings.Contains(logs.String(), "initial password 1") || strings.Contains(logs.String(), "$argon2") {
			t.Fatal("a password or hash reached the log")
		}
		for _, a := range f.audit {
			if strings.Contains(a, "initial") || strings.Contains(a, "$argon2") {
				t.Fatalf("audit carries a secret: %q", a)
			}
		}
		if rec := create(map[string]any{"username": "erin@example.com", "role": "user", "password": "initial password 1"}); rec.Code != 409 {
			t.Errorf("duplicate login = %d", rec.Code)
		}
		if rec := create(map[string]any{"username": "frank", "role": "user", "password": "initial password 1", "extensionIds": []int{extFree}}); rec.Code != 409 {
			t.Errorf("taken extension = %d %s", rec.Code, rec.Body)
		}
		for _, bad := range []map[string]any{
			{"username": "x y", "role": "user", "password": "initial password 1"},
			{"username": "frank", "role": "root", "password": "initial password 1"},
			{"username": "frank", "role": "user", "password": "short"},
			{"username": "frank", "role": "user", "password": "initial password 1", "email": "nope"},
			{"username": "frank", "role": "user", "password": "initial password 1", "authMethod": "oidc"},
			{"username": "frank", "role": "user", "password": "initial password 1", "voicemailEmail": map[string]any{"after": "x"}},
			{"username": "frank", "role": "user", "password": "initial password 1", "extensionIds": []int{99999}},
		} {
			if rec := create(bad); rec.Code != 400 {
				t.Errorf("%v = %d %s, want 400", bad, rec.Code, rec.Body)
			}
		}
	})

	t.Run("only an admin may manage users", func(t *testing.T) {
		for _, who := range []string{"alice", "dave"} {
			if rec := acctCall(h, who, "POST", "/api/v1/users", map[string]any{"username": "z", "role": "admin", "password": "initial password 1"}); rec.Code != 403 {
				t.Errorf("%s create = %d", who, rec.Code)
			}
			if rec := acctCall(h, who, "GET", "/api/v1/users", nil); rec.Code != 403 {
				t.Errorf("%s list = %d", who, rec.Code)
			}
			if rec := acctCall(h, who, "DELETE", "/api/v1/users/1", nil); rec.Code != 403 {
				t.Errorf("%s delete = %d", who, rec.Code)
			}
		}
	})

	t.Run("list and get never carry secrets", func(t *testing.T) {
		rec := acctCall(h, "carol", "GET", "/api/v1/users", nil)
		wantJSON(t, rec, 200, "list")
		if strings.Contains(rec.Body.String(), "argon2") || strings.Contains(rec.Body.String(), "passwordHash") {
			t.Fatalf("list leaks a hash: %s", rec.Body)
		}
		wantJSON(t, acctCall(h, "carol", "GET", "/api/v1/users/1", nil), 200, "get")
		if rec := acctCall(h, "carol", "GET", "/api/v1/users/999", nil); rec.Code != 404 {
			t.Errorf("get unknown = %d", rec.Code)
		}
	})

	t.Run("edit", func(t *testing.T) {
		u := "/api/v1/users/" + strconv.Itoa(uBob)
		got := wantJSON(t, acctCall(h, "carol", "PATCH", u, map[string]any{"email": "bob@example.com", "extensionIds": []int{extBob, extFree}}), 200, "edit")
		if got["email"] != "bob@example.com" || len(got["extensionIds"].([]any)) != 2 {
			t.Fatalf("user = %v", got)
		}
		got = wantJSON(t, acctCall(h, "carol", "PATCH", u, map[string]any{"password": "reset password 99"}), 200, "reset")
		if got["mustChangePassword"] != true || !auth.CheckUserPassword(f.hashes[uBob], "reset password 99") {
			t.Fatalf("reset = %v", got)
		}
		got = wantJSON(t, acctCall(h, "carol", "PATCH", u, map[string]any{"extensionId": nil}), 200, "unlink")
		if len(got["extensionIds"].([]any)) != 0 || got["extensionId"] != nil {
			t.Fatalf("unlink = %v", got)
		}
		got = wantJSON(t, acctCall(h, "carol", "PATCH", u, map[string]any{"extensionId": extBob}), 200, "legacy link")
		if got["extensionId"] != float64(extBob) {
			t.Fatalf("legacy link = %v", got)
		}
		wantJSON(t, acctCall(h, "carol", "PATCH", u, map[string]any{"enabled": false}), 200, "disable")
		if rec := acctCall(h, "bob", "GET", "/api/v1/me/extensions", nil); rec.Code != 401 {
			t.Errorf("disabled user's session = %d", rec.Code)
		}
		wantJSON(t, acctCall(h, "carol", "PATCH", u, map[string]any{"enabled": true, "role": "viewer"}), 200, "enable")
		for _, bad := range []map[string]any{{}, {"role": "root"}, {"password": "x"}, {"mustChangePassword": true}, {"email": "no"},
			{"extensionId": 1, "extensionIds": []int{1}}, {"extensionId": "x"}, {"password": "valid password 123", "enabled": "yes"}} {
			if rec := acctCall(h, "carol", "PATCH", u, bad); rec.Code != 400 {
				t.Errorf("%v = %d %s", bad, rec.Code, rec.Body)
			}
		}
		if rec := acctCall(h, "carol", "PATCH", "/api/v1/users/999", map[string]any{"enabled": true}); rec.Code != 404 {
			t.Errorf("unknown user = %d", rec.Code)
		}
	})

	t.Run("nobody locks themselves or the system out", func(t *testing.T) {
		carol := "/api/v1/users/" + strconv.Itoa(uCarol)
		for _, c := range []struct {
			method string
			body   any
			code   int
			err    string
		}{
			{"PATCH", map[string]any{"enabled": false}, 409, "self_disable"},
			{"PATCH", map[string]any{"role": "operator"}, 409, "last_admin"},
			{"DELETE", nil, 409, "self_delete"},
		} {
			if rec := acctCall(h, "carol", c.method, carol, c.body); rec.Code != c.code || acctErrCode(rec) != c.err {
				t.Errorf("%s %v = %d %s, want %s", c.method, c.body, rec.Code, rec.Body, c.err)
			}
		}
		// A second admin deleting the first is fine only while one remains.
		id := int64(wantJSON(t, create(map[string]any{"username": "admin2", "role": "admin", "password": "initial password 1", "mustChangePassword": false}), 201, "admin2")["id"].(float64))
		f.mu.Lock()
		f.actors[hex.EncodeToString(auth.HashToken("admin2"+cookieSuffix))] = auth.Actor{UserID: id, Username: "admin2", Role: auth.RoleAdmin}
		f.mu.Unlock()
		if rec := acctCall(h, "admin2", "DELETE", carol, nil); rec.Code != 204 {
			t.Fatalf("delete other admin = %d %s", rec.Code, rec.Body)
		}
		if rec := acctCall(h, "admin2", "PATCH", "/api/v1/users/"+strconv.FormatInt(id, 10), map[string]any{"role": "viewer"}); rec.Code != 409 || acctErrCode(rec) != "last_admin" {
			t.Errorf("demote last admin = %d %s", rec.Code, rec.Body)
		}
		if rec := acctCall(h, "admin2", "DELETE", "/api/v1/users/999", nil); rec.Code != 404 {
			t.Errorf("delete unknown = %d", rec.Code)
		}
	})
}

// TestSMTPSettings fails if the SMTP password is ever returned or logged, a
// bad setting is accepted, the test email is not sent with the stored
// settings, or only an admin may use these routes is not enforced.
func TestSMTPSettings(t *testing.T) {
	f, h, _, logs := newAcctFake(t)
	var sentWith mailer.Settings
	var sentTo string
	fail := false
	rebuild := func() http.Handler {
		return Handler(Config{Store: f, Log: slog.New(slog.NewTextHandler(logs, nil)),
			SMTPDeliver: func(_ context.Context, s mailer.Settings, to string, _ []byte) error {
				sentWith, sentTo = s, to
				if fail {
					return errors.New("535 authentication failed")
				}
				return nil
			}})
	}
	h = rebuild()
	const pw = "s3cr3t-smtp-pw"

	got := wantJSON(t, acctCall(h, "carol", "GET", "/api/v1/settings/smtp", nil), 200, "get")
	if got["source"] != "none" || got["passwordSet"] != false {
		t.Fatalf("initial = %v", got)
	}
	if rec := acctCall(h, "carol", "POST", "/api/v1/settings/smtp/test", map[string]any{"to": "me@example.com"}); rec.Code != 409 {
		t.Errorf("test without settings = %d", rec.Code)
	}
	body := map[string]any{"host": "smtp.example.com", "port": 587, "security": "starttls", "username": "pbx", "from": "pbx@example.com", "password": pw}
	rec := acctCall(h, "carol", "PUT", "/api/v1/settings/smtp", body)
	got = wantJSON(t, rec, 200, "put")
	if got["passwordSet"] != true || got["source"] != "database" || strings.Contains(rec.Body.String(), pw) {
		t.Fatalf("put = %v", got)
	}
	if rec := acctCall(h, "carol", "GET", "/api/v1/settings/smtp", nil); strings.Contains(rec.Body.String(), pw) {
		t.Fatalf("get returns the password: %s", rec.Body)
	}
	// Omitting the password keeps it; "" clears it.
	delete(body, "password")
	wantJSON(t, acctCall(h, "carol", "PUT", "/api/v1/settings/smtp", body), 200, "keep")
	if f.smtp.Password != pw {
		t.Fatal("omitted password was not kept")
	}
	if rec := acctCall(h, "carol", "POST", "/api/v1/settings/smtp/test", map[string]any{"to": "me@example.com"}); rec.Code != 204 {
		t.Fatalf("test = %d %s", rec.Code, rec.Body)
	}
	if sentTo != "me@example.com" || sentWith.Host != "smtp.example.com" || sentWith.Password != pw || sentWith.Security != "starttls" {
		t.Fatalf("test sent with %+v to %q", sentWith, sentTo)
	}
	fail = true
	rec = acctCall(h, "carol", "POST", "/api/v1/settings/smtp/test", map[string]any{"to": "me@example.com"})
	if rec.Code != 502 || strings.Contains(rec.Body.String(), pw) {
		t.Fatalf("failing test = %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []map[string]any{
		{"host": "smtp.example.com", "port": 0, "security": "tls", "from": "pbx@example.com"},
		{"host": "smtp.example.com", "port": 587, "security": "ssl", "from": "pbx@example.com"},
		{"host": "smtp.example.com", "port": 587, "security": "opportunistic", "from": "pbx@example.com"},
		{"host": "smtp.example.com", "port": 587, "security": "tls", "from": "not an address"},
		{"host": "a/b", "port": 587, "security": "tls", "from": "pbx@example.com"},
		{"host": "smtp.example.com", "port": 587, "security": "tls", "from": "pbx@example.com", "username": "a\nb"},
	} {
		if rec := acctCall(h, "carol", "PUT", "/api/v1/settings/smtp", bad); rec.Code != 400 {
			t.Errorf("%v = %d", bad, rec.Code)
		}
	}
	for _, bad := range []map[string]any{{"to": "nope"}, {"to": "a@b.co\r\nBcc: x@y.zz"}, {}} {
		if rec := acctCall(h, "carol", "POST", "/api/v1/settings/smtp/test", bad); rec.Code != 400 {
			t.Errorf("test %v = %d", bad, rec.Code)
		}
	}
	for _, who := range []string{"alice", "dave"} {
		for _, c := range [][2]string{{"GET", "/api/v1/settings/smtp"}, {"PUT", "/api/v1/settings/smtp"}, {"POST", "/api/v1/settings/smtp/test"}} {
			if rec := acctCall(h, who, c[0], c[1], nil); rec.Code != 403 {
				t.Errorf("%s %s %s = %d", who, c[0], c[1], rec.Code)
			}
		}
	}
	if strings.Contains(logs.String(), pw) {
		t.Fatal("the SMTP password reached the log")
	}
	for _, a := range f.audit {
		if strings.Contains(a, pw) {
			t.Fatalf("audit carries the password: %q", a)
		}
	}
	// An empty host clears the saved settings.
	got = wantJSON(t, acctCall(h, "carol", "PUT", "/api/v1/settings/smtp", map[string]any{"host": "", "port": 587, "security": "starttls", "from": ""}), 200, "clear")
	if got["source"] != "none" || got["passwordSet"] != false {
		t.Fatalf("cleared = %v", got)
	}
}

// TestAccountRoutesDocumentedStatuses runs last among the account tests and
// fails if a documented status of an account route was never produced by
// them (the package conformance check does this over the whole suite when
// the databases are set; this keeps these routes honest without them).
func TestAccountRoutesDocumentedStatuses(t *testing.T) {
	if conf == nil {
		t.Skip("conformance validator not installed")
	}
	prefixes := []string{"/api/v1/me/", "/api/v1/settings/smtp", "/api/v1/auth/password"}
	conf.mu.Lock()
	defer conf.mu.Unlock()
	for _, op := range conf.spec.Operations() {
		match := op.Path == "/api/v1/users" || strings.HasPrefix(op.Path, "/api/v1/users/")
		for _, p := range prefixes {
			match = match || strings.HasPrefix(op.Path, p)
		}
		if !match || strings.Contains(op.Path, "/me/contacts") {
			continue
		}
		for status := range conf.spec.Responses(op.ID) {
			switch status {
			case "401", "500", "503", "default":
				continue
			}
			if !conf.observed[op.ID+" "+status] {
				t.Errorf("%s %s (%s): documented %s never observed", op.Method, op.Path, op.ID, status)
			}
		}
	}
}
