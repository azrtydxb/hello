package api

// TOTP two-factor authentication (spec pbx-gaps S-11, S-12): the login
// second step, a user's own enrolment, the admin policy and reset. Secrets,
// codes and challenge tokens never reach a log or an audit row. 2FA covers
// interactive sessions only (D10): API tokens and service accounts are not
// asked for a code.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// TwoFactorStore is the 2FA persistence; *store.Store satisfies it. Nil
// answers 503 on the 2FA routes and refuses the login of a user who has 2FA
// (it fails closed).
type TwoFactorStore interface {
	TwoFactorStatus(ctx context.Context, id int64) (store.TwoFactorStatus, error)
	BeginTOTPSetup(ctx context.Context, id int64, secretB32 string) error
	EnableTOTP(ctx context.Context, id int64, code string, now time.Time) ([]string, error)
	DisableTOTP(ctx context.Context, id int64, code string, now time.Time) error
	RegenerateRecoveryCodes(ctx context.Context, id int64, code string, now time.Time) ([]string, error)
	CreateMFAChallenge(ctx context.Context, userID int64, hash []byte, expires time.Time) error
	CompleteMFAChallenge(ctx context.Context, hash []byte, code string, now time.Time) (store.MFAResult, error)
	ResetTOTP(ctx context.Context, actor string, id int64, guard bool) error
	GetAuthSettings(ctx context.Context) (store.AuthSettings, error)
	SetRequire2FARoles(ctx context.Context, actor string, setter int64, roles []string) error
}

// NewTwoFactorMetrics registers hello_2fa_logins_total{result} on reg.
func NewTwoFactorMetrics(reg prometheus.Registerer) *prometheus.CounterVec {
	m := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_2fa_logins_total", Help: "Second-factor login steps by result (challenge, ok, recovery, refused).",
	}, []string{"result"})
	reg.MustRegister(m)
	return m
}

func (s *server) twoFactorMetric(result string) {
	if s.TwoFALogins != nil {
		s.TwoFALogins.WithLabelValues(result).Inc()
	}
}

// twoFactorNow is the clock of the code checks; tests replace it.
var twoFactorNow = time.Now

const twoFactorIssuer = "Hello PBX"

func (s *server) twoFactor(w http.ResponseWriter) bool {
	if s.TwoFactor == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "two-factor authentication is not available")
		return false
	}
	return true
}

// startMFA answers a password-verified login of a 2FA user: 202 and a
// challenge token, no session. It fails closed without the 2FA store.
func (s *server) startMFA(w http.ResponseWriter, r *http.Request, userID int64) {
	if s.TwoFactor == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "two-factor authentication is not available")
		return
	}
	plain, hash := auth.NewToken()
	if err := s.TwoFactor.CreateMFAChallenge(r.Context(), userID, hash, twoFactorNow().Add(store.MFAChallengeTTL)); err != nil {
		s.internal(w, "login: create mfa challenge", err)
		return
	}
	s.twoFactorMetric("challenge")
	writeJSON(w, http.StatusAccepted, map[string]string{"mfa": "required", "mfaToken": plain})
}

// login2FA is POST /api/v1/auth/login/2fa. An unknown, expired or spent
// challenge, a wrong, replayed or rate-limited code all answer the same.
func (s *server) login2FA(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MFAToken string `json:"mfaToken"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.MFAToken == "" || in.Code == "" {
		badRequest(w, "mfaToken and code are required")
		return
	}
	if !s.twoFactor(w) {
		return
	}
	res, err := s.TwoFactor.CompleteMFAChallenge(r.Context(), auth.HashToken(in.MFAToken), in.Code, twoFactorNow())
	if errors.Is(err, store.ErrMFAInvalid) {
		s.twoFactorMetric("refused")
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired code")
		return
	}
	if err != nil {
		s.internal(w, "login: complete mfa challenge", err)
		return
	}
	if res.Recovery {
		s.twoFactorMetric("recovery")
	} else {
		s.twoFactorMetric("ok")
	}
	s.startSession(w, r, res.UserID, res.Username)
}

// twoFactorView is a user's own 2FA state.
type twoFactorView struct {
	Enabled           bool `json:"enabled"`
	Pending           bool `json:"pending"`
	Required          bool `json:"required"`
	RecoveryCodesLeft int  `json:"recoveryCodesLeft"`
}

func (s *server) getTwoFactor(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.selfUser(w, r)
	if !ok || !s.twoFactor(w) {
		return
	}
	st, err := s.TwoFactor.TwoFactorStatus(r.Context(), uid)
	if err != nil {
		s.storeError(w, "user", err)
		return
	}
	writeJSON(w, http.StatusOK, twoFactorView{Enabled: st.Enabled, Pending: st.Pending, Required: st.Required, RecoveryCodesLeft: st.RecoveryLeft})
}

// setupTwoFactor issues a new inactive secret. The secret and URI appear in
// this response only.
func (s *server) setupTwoFactor(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.selfUser(w, r)
	if !ok || !s.twoFactor(w) {
		return
	}
	secret := auth.NewTOTPSecret()
	err := s.TwoFactor.BeginTOTPSetup(r.Context(), uid, secret)
	switch {
	case errors.Is(err, store.ErrTOTPEnrolled):
		writeError(w, http.StatusConflict, "already_enrolled", "two-factor authentication is already active")
		return
	case errors.Is(err, store.ErrNoSecretBox):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "two-factor authentication is not available")
		return
	case err != nil:
		s.storeError(w, "user", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret":     secret,
		"otpauthUri": auth.TOTPURI(twoFactorIssuer, actor(r).Username, secret),
	})
}

type codeIn struct {
	Code     string `json:"code"`
	Password string `json:"password"`
}

func (s *server) enableTwoFactor(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Code == "" {
		badRequest(w, "code is required")
		return
	}
	if !s.twoFactor(w) {
		return
	}
	codes, err := s.TwoFactor.EnableTOTP(r.Context(), uid, in.Code, twoFactorNow())
	if !s.twoFactorResult(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// twoFactorResult maps the store's refusals of an own-2FA action; it reports
// whether the action succeeded.
func (s *server) twoFactorResult(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrMFAInvalid):
		writeError(w, http.StatusUnauthorized, "invalid_code", "invalid code")
	case errors.Is(err, store.ErrNoTOTPSetup):
		writeError(w, http.StatusConflict, "no_setup", "start the setup first")
	case errors.Is(err, store.ErrTOTPEnrolled):
		writeError(w, http.StatusConflict, "already_enrolled", "two-factor authentication is already active")
	case errors.Is(err, store.ErrNoSecretBox):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "two-factor authentication is not available")
	default:
		s.storeError(w, "user", err)
	}
	return false
}

// disableTwoFactor turns the caller's own 2FA off. It needs the password and
// a code, and is refused while the account or its role requires 2FA.
func (s *server) disableTwoFactor(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	var in codeIn
	if !decode(w, r, &in) {
		return
	}
	if in.Code == "" || in.Password == "" {
		badRequest(w, "password and code are required")
		return
	}
	if !s.twoFactor(w) {
		return
	}
	st, err := s.TwoFactor.TwoFactorStatus(r.Context(), uid)
	if err != nil {
		s.storeError(w, "user", err)
		return
	}
	if st.Required {
		writeError(w, http.StatusConflict, "two_factor_required", "two-factor authentication is required for your account")
		return
	}
	hash, err := s.Store.UserPasswordHash(r.Context(), uid)
	if err != nil {
		s.storeError(w, "user", err)
		return
	}
	if !auth.CheckUserPassword(hash, in.Password) {
		writeError(w, http.StatusUnauthorized, "invalid_code", "invalid password or code")
		return
	}
	if !s.twoFactorResult(w, s.TwoFactor.DisableTOTP(r.Context(), uid, in.Code, twoFactorNow())) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Code == "" {
		badRequest(w, "code is required")
		return
	}
	if !s.twoFactor(w) {
		return
	}
	codes, err := s.TwoFactor.RegenerateRecoveryCodes(r.Context(), uid, in.Code, twoFactorNow())
	if !s.twoFactorResult(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// resetUserTwoFactor is DELETE /api/v1/users/{id}/2fa. The reset of the only
// enrolled admin is refused; the CLI is the break-glass form.
func (s *server) resetUserTwoFactor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || !s.twoFactor(w) {
		return
	}
	err := s.TwoFactor.ResetTOTP(r.Context(), actor(r).String(), id, true)
	if errors.Is(err, store.ErrLastEnrolledAdmin) {
		writeError(w, http.StatusConflict, "last_enrolled_admin",
			"this is the only admin with two-factor authentication; use hello-control user 2fa-reset")
		return
	}
	if err != nil {
		s.storeError(w, "user", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getTwoFactorPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.twoFactor(w) {
		return
	}
	p, err := s.TwoFactor.GetAuthSettings(r.Context())
	if err != nil {
		s.internal(w, "2fa policy: read", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// putTwoFactorPolicy replaces the roles that must use 2FA. Naming admin is
// refused unless the caller has active 2FA.
func (s *server) putTwoFactorPolicy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Roles []string `json:"require2faRoles"`
	}
	if !decode(w, r, &in) {
		return
	}
	roles := []string{}
	for _, name := range in.Roles {
		role, err := auth.ParseRole(name)
		if err != nil {
			badRequest(w, "require2faRoles must name roles: user, viewer, operator or admin")
			return
		}
		if !slices.Contains(roles, string(role)) {
			roles = append(roles, string(role))
		}
	}
	if !s.twoFactor(w) {
		return
	}
	err := s.TwoFactor.SetRequire2FARoles(r.Context(), actor(r).String(), actor(r).UserID, roles)
	if errors.Is(err, store.ErrTwoFactorRequired) {
		writeError(w, http.StatusConflict, "two_factor_required", "enrol two-factor authentication yourself before requiring it for admins")
		return
	}
	if err != nil {
		s.internal(w, "2fa policy: write", err)
		return
	}
	s.getTwoFactorPolicy(w, r)
}
