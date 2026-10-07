package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, "correct horse") || !strings.HasPrefix(h, "$2") {
		t.Fatalf("hash %q is not a bcrypt hash of the password", h)
	}
	if !CheckPassword(h, "correct horse") {
		t.Fatal("correct password rejected")
	}
	if CheckPassword(h, "correct horsE") {
		t.Fatal("wrong password accepted")
	}
}

func TestNewToken(t *testing.T) {
	plain, hash := NewToken()
	raw, err := base64.RawURLEncoding.DecodeString(plain)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token %q: %d bytes, %v; want 32 base64url bytes", plain, len(raw), err)
	}
	if !bytes.Equal(hash, HashToken(plain)) || len(hash) != 32 {
		t.Fatalf("hash is not SHA-256 of the token")
	}
	if p2, _ := NewToken(); p2 == plain {
		t.Fatal("two tokens are equal")
	}
	// Known SHA-256 vector, so HashToken cannot silently become another hash.
	if got := hex.EncodeToString(HashToken("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("HashToken(abc) = %s", got)
	}
}

func TestDeviceSecretAndHA1(t *testing.T) {
	s := NewDeviceSecret()
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) != 24 || strings.Contains(s, "=") {
		t.Fatalf("secret %q: %d bytes, %v; want 24 base64url bytes without padding", s, len(raw), err)
	}
	m, sh := HA1("alice", "hello.test", "s3cret")
	if m != "60a5e5ef560bbfcc8500d2c6f8cf92a4" {
		t.Fatalf("MD5 HA1 = %s", m)
	}
	if sh != "b4e3344965b29ec4148df63fbd5153578c43bea537f51245c46a04129dd5f30d" {
		t.Fatalf("SHA-256 HA1 = %s", sh)
	}
}

type fakeLookup struct {
	sessions map[string]Actor // keyed by hex hash
	tokens   map[string]Actor
	err      error
}

func (f fakeLookup) find(m map[string]Actor, hash []byte) (Actor, error) {
	if f.err != nil {
		return Actor{}, f.err
	}
	if a, ok := m[hex.EncodeToString(hash)]; ok {
		return a, nil
	}
	return Actor{}, ErrNoCredentials
}

func (f fakeLookup) SessionActor(_ context.Context, h []byte) (Actor, error) {
	return f.find(f.sessions, h)
}

func (f fakeLookup) TokenActor(_ context.Context, h []byte) (Actor, error) {
	return f.find(f.tokens, h)
}

func TestMiddleware(t *testing.T) {
	alice := Actor{UserID: 1, Username: "alice"}
	bot := Actor{UserID: 1, Username: "alice", TokenID: 7}
	l := fakeLookup{
		sessions: map[string]Actor{hex.EncodeToString(HashToken("sess-1")): alice},
		tokens:   map[string]Actor{hex.EncodeToString(HashToken("tok-1")): bot},
	}
	// Sessions carry every scope plus session; unprefixed tokens are legacy
	// tokens with every scope.
	aliceSession := alice
	aliceSession.Kind, aliceSession.Scopes = KindSession, append(slices.Clone(AllScopes), ScopeSession)
	botLegacy := bot
	botLegacy.Kind, botLegacy.Scopes = KindLegacyToken, slices.Clone(AllScopes)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var got Actor
	h := Middleware(l, Options{Cookies: true}, log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := ActorFrom(r.Context())
		if !ok {
			t.Error("no actor in context")
		}
		got = a
		w.WriteHeader(http.StatusTeapot)
	}))

	cases := []struct {
		name   string
		cookie string
		authz  string
		want   int
		actor  Actor
	}{
		{name: "none", want: 401},
		{name: "session", cookie: "sess-1", want: 418, actor: aliceSession},
		{name: "unknown session", cookie: "sess-2", want: 401},
		{name: "empty session", cookie: "", authz: "", want: 401},
		{name: "bearer", authz: "Bearer tok-1", want: 418, actor: botLegacy},
		{name: "unknown bearer", authz: "Bearer tok-2", want: 401},
		{name: "not bearer", authz: "Basic tok-1", want: 401},
		{name: "empty bearer", authz: "Bearer ", want: 401},
		{name: "bad bearer ignores good cookie", cookie: "sess-1", authz: "Bearer nope", want: 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got = Actor{}
			r := httptest.NewRequest(http.MethodGet, "/api/v1/extensions", nil)
			if c.cookie != "" {
				r.AddCookie(&http.Cookie{Name: SessionCookie, Value: c.cookie})
			}
			if c.authz != "" {
				r.Header.Set("Authorization", c.authz)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
			if c.want == 401 && !strings.Contains(rec.Body.String(), `"code":"unauthorized"`) {
				t.Fatalf("401 body = %s", rec.Body)
			}
			if !reflect.DeepEqual(got, c.actor) {
				t.Fatalf("actor = %+v, want %+v", got, c.actor)
			}
		})
	}

	// A lookup failure is an internal error, not an anonymous pass.
	h = Middleware(fakeLookup{err: errors.New("db down")}, Options{Cookies: true}, log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "sess-1"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 500 {
		t.Fatalf("lookup error: status = %d, want 500", rec.Code)
	}

	// Without Cookies (the /mcp transport) a valid session cookie is not a
	// credential.
	h = Middleware(l, Options{}, log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	r = httptest.NewRequest(http.MethodPost, "/mcp", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "sess-1"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 401 {
		t.Fatalf("cookie without Options.Cookies: status = %d, want 401", rec.Code)
	}
}

func TestActorString(t *testing.T) {
	if s := (Actor{Username: "alice"}).String(); s != "user:alice" {
		t.Fatal(s)
	}
	if s := (Actor{Username: "alice", TokenID: 9}).String(); s != "token:9" {
		t.Fatal(s)
	}
}

type fakeUsers struct {
	n       int
	created []string
}

func (f *fakeUsers) CountUsers(context.Context) (int, error) { return f.n, nil }
func (f *fakeUsers) CreateFirstUser(_ context.Context, u, h string) (bool, error) {
	f.created = append(f.created, u+" "+h)
	return true, nil
}

func TestBootstrap(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	ctx := context.Background()

	empty := &fakeUsers{}
	if err := Bootstrap(ctx, empty, "", log); err != nil || len(empty.created) != 0 {
		t.Fatalf("no password: created %v, %v", empty.created, err)
	}
	existing := &fakeUsers{n: 1}
	if err := Bootstrap(ctx, existing, "pw-Secret-123", log); err != nil || len(existing.created) != 0 {
		t.Fatalf("users exist: created %v, %v", existing.created, err)
	}
	if err := Bootstrap(ctx, empty, "pw-Secret-123", log); err != nil || len(empty.created) != 1 {
		t.Fatalf("bootstrap: created %v, %v", empty.created, err)
	}
	user, hash, _ := strings.Cut(empty.created[0], " ")
	if user != "admin" || !CheckPassword(hash, "pw-Secret-123") {
		t.Fatalf("created %q with a hash that does not match the password", user)
	}
	if strings.Contains(buf.String(), "pw-Secret-123") || !strings.Contains(buf.String(), "bootstrap admin user created") {
		t.Fatalf("log = %s", buf.String())
	}
}
