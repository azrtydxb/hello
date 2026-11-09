package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// tfFake is an in-memory TwoFactorStore with the real code check.
type tfFake struct {
	secret     map[int64]string
	enabled    map[int64]bool
	last       map[int64]int64
	challenges map[string]int64 // hex-free: token hash string -> user
	wrong      map[string]int
	recovery   map[int64]map[string]bool
	policy     []string
	setterOK   map[int64]bool
	users      map[int64]string
	resetErr   error
	required   map[int64]bool
	resets     []int64
}

func newTFFake() *tfFake {
	return &tfFake{secret: map[int64]string{}, enabled: map[int64]bool{}, last: map[int64]int64{},
		challenges: map[string]int64{}, wrong: map[string]int{}, recovery: map[int64]map[string]bool{},
		setterOK: map[int64]bool{}, users: map[int64]string{}, required: map[int64]bool{}}
}

func (f *tfFake) TwoFactorStatus(_ context.Context, id int64) (store.TwoFactorStatus, error) {
	return store.TwoFactorStatus{Enabled: f.enabled[id], Required: f.required[id], Pending: f.secret[id] != "" && !f.enabled[id], RecoveryLeft: len(f.recovery[id])}, nil
}

func (f *tfFake) BeginTOTPSetup(_ context.Context, id int64, s string) error {
	if f.enabled[id] {
		return store.ErrTOTPEnrolled
	}
	f.secret[id] = s
	return nil
}

func (f *tfFake) EnableTOTP(_ context.Context, id int64, code string, now time.Time) ([]string, error) {
	if f.secret[id] == "" {
		return nil, store.ErrNoTOTPSetup
	}
	step, ok := auth.VerifyTOTP(f.secret[id], code, now, f.last[id])
	if !ok {
		return nil, store.ErrMFAInvalid
	}
	f.enabled[id], f.last[id] = true, step
	plain, _ := auth.NewRecoveryCodes(auth.RecoveryCodeCount)
	f.recovery[id] = map[string]bool{}
	for _, c := range plain {
		f.recovery[id][auth.NormalizeRecoveryCode(c)] = true
	}
	return plain, nil
}

func (f *tfFake) DisableTOTP(_ context.Context, id int64, code string, now time.Time) error {
	if _, ok := auth.VerifyTOTP(f.secret[id], code, now, f.last[id]); !ok {
		return store.ErrMFAInvalid
	}
	f.enabled[id], f.secret[id] = false, ""
	return nil
}

func (f *tfFake) RegenerateRecoveryCodes(_ context.Context, id int64, code string, now time.Time) ([]string, error) {
	step, ok := auth.VerifyTOTP(f.secret[id], code, now, f.last[id])
	if !ok {
		return nil, store.ErrMFAInvalid
	}
	f.last[id] = step
	plain, _ := auth.NewRecoveryCodes(auth.RecoveryCodeCount)
	return plain, nil
}

func (f *tfFake) CreateMFAChallenge(_ context.Context, uid int64, h []byte, _ time.Time) error {
	f.challenges[string(h)] = uid
	return nil
}

func (f *tfFake) CompleteMFAChallenge(_ context.Context, h []byte, code string, now time.Time) (store.MFAResult, error) {
	uid, ok := f.challenges[string(h)]
	if !ok {
		return store.MFAResult{}, store.ErrMFAInvalid
	}
	if rc := auth.NormalizeRecoveryCode(code); auth.LooksLikeRecoveryCode(code) && f.recovery[uid][rc] {
		delete(f.recovery[uid], rc)
		delete(f.challenges, string(h))
		return store.MFAResult{UserID: uid, Username: f.users[uid], Recovery: true}, nil
	}
	step, ok := auth.VerifyTOTP(f.secret[uid], code, now, f.last[uid])
	if !ok {
		if f.wrong[string(h)]++; f.wrong[string(h)] >= 5 {
			delete(f.challenges, string(h))
		}
		return store.MFAResult{}, store.ErrMFAInvalid
	}
	f.last[uid] = step
	delete(f.challenges, string(h))
	return store.MFAResult{UserID: uid, Username: f.users[uid]}, nil
}

func (f *tfFake) ResetTOTP(_ context.Context, _ string, id int64, _ bool) error {
	if id > 100 {
		return store.ErrNotFound
	}
	f.resets = append(f.resets, id)
	return f.resetErr
}

func (f *tfFake) GetAuthSettings(context.Context) (store.AuthSettings, error) {
	return store.AuthSettings{Require2FARoles: append([]string{}, f.policy...)}, nil
}

func (f *tfFake) SetRequire2FARoles(_ context.Context, _ string, setter int64, roles []string) error {
	for _, r := range roles {
		if r == "admin" && !f.enabled[setter] {
			return store.ErrTwoFactorRequired
		}
	}
	f.policy = roles
	return nil
}

func tfHandler(t *testing.T) (*acctFake, *tfFake, http.Handler) {
	t.Helper()
	f, _, _, _ := newAcctFake(t)
	tf := newTFFake()
	for id, u := range f.users {
		tf.users[id] = u.Username
	}
	h := Handler(Config{Store: f, TwoFactor: tf, Log: slog.New(slog.NewTextHandler(&strings.Builder{}, nil)), SessionTTL: time.Hour})
	return f, tf, h
}

func login(h http.Handler, user string) *httptest.ResponseRecorder {
	return acctCall(h, "", "POST", "/api/v1/auth/login", map[string]any{"username": user, "password": strongPassword})
}

func setCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			return true
		}
	}
	return false
}

// TestTOTPLogin fails if a 2FA user gets a session from the password alone,
// if a wrong, replayed or unknown code is told apart from another, if a
// challenge works twice or after five wrong codes, or if the login of a 2FA
// user succeeds when the 2FA store is missing.
func TestTOTPLogin(t *testing.T) {
	f, tf, h := tfHandler(t)
	f.users[uAlice].TOTPEnabled = true
	secret := auth.NewTOTPSecret()
	tf.secret[uAlice], tf.enabled[uAlice] = secret, true
	tf.recovery[uAlice] = map[string]bool{}
	plain, _ := auth.NewRecoveryCodes(2)
	for _, c := range plain {
		tf.recovery[uAlice][auth.NormalizeRecoveryCode(c)] = true
	}
	now := time.Now()
	twoFactorNow = func() time.Time { return now }
	t.Cleanup(func() { twoFactorNow = time.Now })
	code := func(d int64) string {
		c, _ := auth.TOTPCode(secret, auth.TOTPStep(now)+d)
		return c
	}
	challenge := func() string {
		rec := login(h, "alice")
		m := wantJSON(t, rec, http.StatusAccepted, "password step")
		if m["mfa"] != "required" || setCookie(rec) {
			t.Fatalf("password step = %v, cookie %v", m, setCookie(rec))
		}
		return m["mfaToken"].(string)
	}
	second := func(tok, c string) *httptest.ResponseRecorder {
		return acctCall(h, "", "POST", "/api/v1/auth/login/2fa", map[string]any{"mfaToken": tok, "code": c})
	}

	t.Run("a user without 2FA still gets a session", func(t *testing.T) {
		if rec := login(h, "bob"); rec.Code != http.StatusNoContent || !setCookie(rec) {
			t.Fatalf("bob = %d", rec.Code)
		}
	})
	t.Run("the right code issues the session, once", func(t *testing.T) {
		tok := challenge()
		rec := second(tok, code(0))
		if rec.Code != http.StatusNoContent || !setCookie(rec) {
			t.Fatalf("right code = %d %s", rec.Code, rec.Body)
		}
		if again := second(tok, code(1)); again.Code != http.StatusUnauthorized || setCookie(again) {
			t.Fatalf("spent challenge = %d", again.Code)
		}
	})
	t.Run("a replayed code is refused like a wrong one", func(t *testing.T) {
		rec := second(challenge(), code(0))
		wrong := second(challenge(), "000000")
		unknown := second("no-such-challenge", "000000")
		for _, r := range []*httptest.ResponseRecorder{rec, wrong, unknown} {
			if r.Code != http.StatusUnauthorized || setCookie(r) {
				t.Fatalf("got %d %s", r.Code, r.Body)
			}
		}
		if wrong.Body.String() != unknown.Body.String() || wrong.Body.String() != rec.Body.String() {
			t.Fatalf("refusals differ: %q %q %q", wrong.Body, unknown.Body, rec.Body)
		}
	})
	t.Run("90 s of drift is refused", func(t *testing.T) {
		if r := second(challenge(), code(3)); r.Code != http.StatusUnauthorized {
			t.Fatalf("drifted code = %d", r.Code)
		}
	})
	t.Run("five wrong codes end the challenge", func(t *testing.T) {
		tok := challenge()
		for i := 0; i < 5; i++ {
			second(tok, "000000")
		}
		if r := second(tok, code(1)); r.Code != http.StatusUnauthorized {
			t.Fatalf("challenge survived: %d", r.Code)
		}
	})
	t.Run("a recovery code works once", func(t *testing.T) {
		if r := second(challenge(), plain[0]); r.Code != http.StatusNoContent {
			t.Fatalf("recovery = %d %s", r.Code, r.Body)
		}
		if r := second(challenge(), plain[0]); r.Code != http.StatusUnauthorized {
			t.Fatalf("recovery reused = %d", r.Code)
		}
	})
	t.Run("without the 2FA store a 2FA login fails closed", func(t *testing.T) {
		h2 := Handler(Config{Store: f, Log: slog.New(slog.NewTextHandler(&strings.Builder{}, nil)), SessionTTL: time.Hour})
		rec := login(h2, "alice")
		if rec.Code != http.StatusServiceUnavailable || setCookie(rec) {
			t.Fatalf("got %d", rec.Code)
		}
	})
	t.Run("a body without a code is a 400", func(t *testing.T) {
		if r := second("x", ""); r.Code != http.StatusBadRequest {
			t.Fatalf("got %d", r.Code)
		}
	})
}

// TestTOTPEnrolment fails if enrolment does not return the secret and URI,
// accepts a wrong code, or if the 2FA routes take a token instead of a
// session.
func TestTOTPEnrolment(t *testing.T) {
	_, tf, h := tfHandler(t)
	now := time.Now()
	twoFactorNow = func() time.Time { return now }
	t.Cleanup(func() { twoFactorNow = time.Now })

	m := wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/2fa/setup", nil), 200, "setup")
	sec, _ := m["secret"].(string)
	if sec == "" || !strings.HasPrefix(m["otpauthUri"].(string), "otpauth://totp/") || !strings.Contains(m["otpauthUri"].(string), sec) {
		t.Fatalf("setup = %v", m)
	}
	if rec := acctCall(h, "alice", "POST", "/api/v1/me/2fa/enable", map[string]any{"code": "000000"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code = %d", rec.Code)
	}
	c, _ := auth.TOTPCode(sec, auth.TOTPStep(now))
	m = wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/2fa/enable", map[string]any{"code": c}), 200, "enable")
	if codes, _ := m["recoveryCodes"].([]any); len(codes) != 10 {
		t.Fatalf("recovery codes = %v", m)
	}
	if !tf.enabled[uAlice] {
		t.Fatal("not enabled")
	}
	if rec := acctCall(h, "alice", "POST", "/api/v1/me/2fa/setup", nil); rec.Code != http.StatusConflict {
		t.Fatalf("setup while active = %d", rec.Code)
	}
	st := wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/me/2fa", nil), 200, "status")
	if st["enabled"] != true || st["recoveryCodesLeft"] != float64(10) {
		t.Fatalf("status = %v", st)
	}
	me := wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/auth/me", nil), 200, "me")
	if tfv, _ := me["twoFactor"].(map[string]any); tfv["enabled"] != true {
		t.Fatalf("me = %v", me)
	}
	// Every /me/2fa route needs the session scope, so no token reaches it.
	for _, rt := range Routes() {
		if strings.HasPrefix(rt.Pattern, "/api/v1/me/2fa") && rt.Scope != auth.ScopeSession {
			t.Errorf("%s %s scope = %q, want session", rt.Method, rt.Pattern, rt.Scope)
		}
	}
}

// TestTOTPPolicy fails if requiring 2FA for admins is accepted from an admin
// without 2FA, if the last enrolled admin can be reset over the API, or if a
// required, unenrolled user can reach anything but the enrolment.
func TestTOTPPolicy(t *testing.T) {
	f, tf, h := tfHandler(t)

	t.Run("requiring 2FA for admins is refused without an enrolled setter", func(t *testing.T) {
		rec := acctCall(h, "carol", "PUT", "/api/v1/auth/2fa-policy", map[string]any{"require2faRoles": []string{"admin"}})
		if rec.Code != http.StatusConflict || acctErrCode(rec) != "two_factor_required" {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
		if len(tf.policy) != 0 {
			t.Fatalf("policy changed: %v", tf.policy)
		}
		if rec := acctCall(h, "carol", "PUT", "/api/v1/auth/2fa-policy", map[string]any{"require2faRoles": []string{"viewer"}}); rec.Code != 200 {
			t.Fatalf("require viewer = %d %s", rec.Code, rec.Body)
		}
		tf.enabled[uCarol] = true
		m := wantJSON(t, acctCall(h, "carol", "PUT", "/api/v1/auth/2fa-policy", map[string]any{"require2faRoles": []string{"admin", "admin"}}), 200, "enrolled setter")
		if r, _ := m["require2faRoles"].([]any); len(r) != 1 {
			t.Fatalf("policy = %v", m)
		}
		m = wantJSON(t, acctCall(h, "carol", "GET", "/api/v1/auth/2fa-policy", nil), 200, "read policy")
		if r, _ := m["require2faRoles"].([]any); len(r) != 1 {
			t.Fatalf("read policy = %v", m)
		}
	})
	t.Run("an unknown role is a 400", func(t *testing.T) {
		if rec := acctCall(h, "carol", "PUT", "/api/v1/auth/2fa-policy", map[string]any{"require2faRoles": []string{"root"}}); rec.Code != 400 {
			t.Fatalf("got %d", rec.Code)
		}
	})
	t.Run("the policy is admin only", func(t *testing.T) {
		for _, who := range []string{"alice", "dave"} {
			if rec := acctCall(h, who, "GET", "/api/v1/auth/2fa-policy", nil); rec.Code != http.StatusForbidden {
				t.Errorf("%s got %d", who, rec.Code)
			}
			if rec := acctCall(h, who, "DELETE", "/api/v1/users/1/2fa", nil); rec.Code != http.StatusForbidden {
				t.Errorf("%s reset got %d", who, rec.Code)
			}
		}
	})
	t.Run("the last enrolled admin cannot be reset over the API", func(t *testing.T) {
		tf.resetErr = store.ErrLastEnrolledAdmin
		rec := acctCall(h, "carol", "DELETE", "/api/v1/users/"+strconv.Itoa(uCarol)+"/2fa", nil)
		if rec.Code != http.StatusConflict || acctErrCode(rec) != "last_enrolled_admin" {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
		tf.resetErr = nil
		if rec := acctCall(h, "carol", "DELETE", "/api/v1/users/"+strconv.Itoa(uAlice)+"/2fa", nil); rec.Code != http.StatusNoContent {
			t.Fatalf("reset alice = %d", rec.Code)
		}
	})
	t.Run("a required, unenrolled user reaches only the enrolment", func(t *testing.T) {
		f.users[uAlice].TOTPRequired = true
		for _, c := range []struct{ method, path string }{
			{"GET", "/api/v1/me/extensions"}, {"GET", "/api/v1/me/contacts"}, {"GET", "/api/v1/me/calls"},
		} {
			rec := acctCall(h, "alice", c.method, c.path, nil)
			if rec.Code != http.StatusForbidden || acctErrCode(rec) != "two_factor_enrolment_required" {
				t.Errorf("%s %s = %d %s", c.method, c.path, rec.Code, rec.Body)
			}
		}
		for _, p := range []string{"/api/v1/auth/me", "/api/v1/me/2fa"} {
			if rec := acctCall(h, "alice", "GET", p, nil); rec.Code != 200 {
				t.Errorf("GET %s = %d %s", p, rec.Code, rec.Body)
			}
		}
		if rec := acctCall(h, "alice", "POST", "/api/v1/me/2fa/setup", nil); rec.Code != 200 {
			t.Errorf("setup = %d", rec.Code)
		}
		f.users[uAlice].TOTPEnabled = true
		if rec := acctCall(h, "alice", "GET", "/api/v1/me/extensions", nil); rec.Code != 200 {
			t.Errorf("after enrolment = %d %s", rec.Code, rec.Body)
		}
	})
}

// TestTOTPRouteStatuses produces every documented status of the own-2FA
// routes that the other tests do not (the documented-status check needs each
// one observed), and checks the refusals are the right ones.
func TestTOTPRouteStatuses(t *testing.T) {
	_, tf, h := tfHandler(t)
	now := time.Now()
	twoFactorNow = func() time.Time { return now }
	t.Cleanup(func() { twoFactorNow = time.Now })

	// A credential that is not a user is refused on every own-2FA route.
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/me/2fa"}, {"POST", "/api/v1/me/2fa/setup"}, {"POST", "/api/v1/me/2fa/enable"},
		{"POST", "/api/v1/me/2fa/disable"}, {"POST", "/api/v1/me/2fa/recovery-codes"},
	} {
		if rec := acctCall(h, "svc", c.method, c.path, map[string]any{"code": "1"}); rec.Code != http.StatusForbidden {
			t.Errorf("svc %s %s = %d", c.method, c.path, rec.Code)
		}
	}
	// Empty bodies are 400s.
	for _, p := range []string{"enable", "disable", "recovery-codes"} {
		if rec := acctCall(h, "bob", "POST", "/api/v1/me/2fa/"+p, map[string]any{}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s with no code = %d", p, rec.Code)
		}
	}
	// Enable before setup is a 409.
	if rec := acctCall(h, "bob", "POST", "/api/v1/me/2fa/enable", map[string]any{"code": "123456"}); rec.Code != http.StatusConflict {
		t.Fatalf("enable before setup = %d", rec.Code)
	}
	// Enrol bob, then regenerate is refused with a bad code and disable needs both secrets.
	wantJSON(t, acctCall(h, "bob", "POST", "/api/v1/me/2fa/setup", nil), 200, "setup")
	sec := tf.secret[uBob]
	c, _ := auth.TOTPCode(sec, auth.TOTPStep(now))
	wantJSON(t, acctCall(h, "bob", "POST", "/api/v1/me/2fa/enable", map[string]any{"code": c}), 200, "enable")
	if rec := acctCall(h, "bob", "POST", "/api/v1/me/2fa/recovery-codes", map[string]any{"code": "000000"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("regenerate with a bad code = %d", rec.Code)
	}
	now = now.Add(31 * time.Second)
	c, _ = auth.TOTPCode(sec, auth.TOTPStep(now))
	m := wantJSON(t, acctCall(h, "bob", "POST", "/api/v1/me/2fa/recovery-codes", map[string]any{"code": c}), 200, "regenerate")
	if codes, _ := m["recoveryCodes"].([]any); len(codes) != 10 {
		t.Fatalf("regenerate = %v", m)
	}
	tf.required[uBob] = true
	rec := acctCall(h, "bob", "POST", "/api/v1/me/2fa/disable", map[string]any{"password": strongPassword, "code": c})
	if rec.Code != http.StatusConflict || acctErrCode(rec) != "two_factor_required" {
		t.Fatalf("disable while required = %d %s", rec.Code, rec.Body)
	}
	tf.required[uBob] = false
	if rec := acctCall(h, "bob", "POST", "/api/v1/me/2fa/disable", map[string]any{"password": "wrong-password-1", "code": c}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disable with a wrong password = %d", rec.Code)
	}
	now = now.Add(31 * time.Second)
	c, _ = auth.TOTPCode(sec, auth.TOTPStep(now))
	if rec := acctCall(h, "bob", "POST", "/api/v1/me/2fa/disable", map[string]any{"password": strongPassword, "code": c}); rec.Code != http.StatusNoContent {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body)
	}
	if tf.enabled[uBob] {
		t.Fatal("still enabled")
	}
	// Reset of an unknown user is 404.
	if rec := acctCall(h, "carol", "DELETE", "/api/v1/users/999/2fa", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("reset unknown = %d", rec.Code)
	}
}
