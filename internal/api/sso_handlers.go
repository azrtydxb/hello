package api

// Single sign-on (spec integrations P11, I-26 to I-30): the OIDC sign-in
// through Google or Entra, the identities linked to users, the SSO settings
// with sso_required, and the Google administrator consent of a directory
// connector. The callbacks are cross-site top-level redirects, so the
// SameSite=Strict session cookie cannot carry their state: it lives in
// Valkey, single use, and is bound to the browser by the short-lived
// hello_oidc cookie (SameSite=Lax).

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/directory"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/sso"
	"github.com/azrtydxb/hello/internal/store"
)

// SSOService is single sign-on; *sso.Service satisfies it. Nil hides the
// providers and answers 503 on the SSO routes.
type SSOService interface {
	PublicProviders(ctx context.Context) ([]sso.PublicProvider, error)
	Settings(ctx context.Context) (sso.Settings, error)
	PutSettings(ctx context.Context, actor string, in sso.SettingsInput) (sso.Settings, error)
	TestProvider(ctx context.Context, actor, name string) (sso.TestResult, error)
	Start(ctx context.Context, name string, userID int64, next string) (string, string, error)
	Callback(ctx context.Context, name string, q url.Values, binding string) (sso.Outcome, error)
	AllowOIDC(ctx context.Context, addr string) bool
	LoginMetric(provider, result string)
	BindLogin(ctx context.Context, connectorID int64, externalID string, userID int64, password, addr string) error
	Identities(ctx context.Context, userID int64) ([]sso.Identity, error)
	Unlink(ctx context.Context, actor string, userID int64, provider string) error
	StartConsent(ctx context.Context, c sso.Consents, actor string, id int64) (string, string, error)
	FinishConsent(ctx context.Context, c sso.Consents, q url.Values, binding string) (int64, error)
}

// oidcCookie binds a callback to the browser that started it.
const oidcCookie = "hello_oidc"

func setOIDCCookie(w http.ResponseWriter, r *http.Request, v string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is set whenever the request is HTTPS.
		Name: oidcCookie, Value: v, Path: "/api/v1/", MaxAge: int(sso.StateTTL / time.Second),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
}

func clearOIDCCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is set whenever the request is HTTPS.
		Name: oidcCookie, Value: "", Path: "/api/v1/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
}

func oidcBinding(r *http.Request) string {
	if c, err := r.Cookie(oidcCookie); err == nil {
		return c.Value
	}
	return ""
}

// listSSOProviders is GET /auth/oidc/providers: the login page's buttons.
func (s *server) listSSOProviders(w http.ResponseWriter, r *http.Request) {
	out := []sso.PublicProvider{}
	if s.SSO != nil {
		ps, err := s.SSO.PublicProviders(r.Context())
		if err != nil {
			s.internal(w, "sso providers", err)
			return
		}
		out = ps
	}
	writeJSON(w, http.StatusOK, items(out))
}

// oidcStart is GET /auth/oidc/{provider}/start: a top-level navigation from
// the login page to the IdP.
func (s *server) oidcStart(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if s.SSO == nil {
		redirectLogin(w, r, "unavailable")
		return
	}
	if !s.SSO.AllowOIDC(r.Context(), callerAddr(r)) {
		redirectLogin(w, r, "throttled")
		return
	}
	u, binding, err := s.SSO.Start(r.Context(), provider, 0, r.URL.Query().Get("next"))
	if err != nil {
		s.SSO.LoginMetric(provider, "start_failed")
		redirectLogin(w, r, ssoErrorCode(err))
		return
	}
	setOIDCCookie(w, r, binding)
	w.Header().Set("Cache-Control", "no-store")
	// u is the pinned provider's authorization endpoint from its discovery
	// document, with parameters Hello chose; nothing of the request is in it.
	http.Redirect(w, r, u, http.StatusSeeOther) //nolint:gosec // G710: not request-controlled, see above

}

// redirectLogin sends the browser back to the login page with a generic code
// the page turns into a message.
func redirectLogin(w http.ResponseWriter, r *http.Request, code string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/login?sso_error="+url.QueryEscape(code), http.StatusSeeOther)
}

// ssoErrorCode is the generic code of a refused sign-in: it says whether to
// try again or ask an administrator, never which check failed.
func ssoErrorCode(err error) string {
	switch {
	case errors.Is(err, sso.ErrUnavailable), errors.Is(err, sso.ErrProviderOff), errors.Is(err, sso.ErrNotFound):
		return "unavailable"
	case errors.Is(err, sso.ErrState), errors.Is(err, sso.ErrDenied):
		return "retry"
	case errors.Is(err, sso.ErrThrottled):
		return "throttled"
	}
	return "failed"
}

// ssoResult names a sign-in outcome for the metric.
func ssoResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, sso.ErrState):
		return "bad_state"
	case errors.Is(err, sso.ErrDenied):
		return "denied"
	case errors.Is(err, sso.ErrToken):
		return "bad_token"
	case errors.Is(err, sso.ErrNoUser):
		return "no_user"
	case errors.Is(err, sso.ErrUserDisabled):
		return "disabled"
	case errors.Is(err, sso.ErrLinked):
		return "already_linked"
	case errors.Is(err, sso.ErrUnavailable), errors.Is(err, sso.ErrProviderOff):
		return "unavailable"
	}
	return "error"
}

// oidcCallback is GET /auth/oidc/{provider}/callback.
func (s *server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	clearOIDCCookie(w, r)
	if s.SSO == nil {
		redirectLogin(w, r, "unavailable")
		return
	}
	if !s.SSO.AllowOIDC(r.Context(), callerAddr(r)) {
		redirectLogin(w, r, "throttled")
		return
	}
	out, err := s.SSO.Callback(r.Context(), provider, r.URL.Query(), oidcBinding(r))
	s.SSO.LoginMetric(provider, ssoResult(err))
	if err != nil {
		// The audit names the provider and the outcome, never the identity
		// that tried (spec Audit).
		if aerr := s.Store.Audit(r.Context(), "sso:"+provider, "login-failed:"+ssoResult(err), "user", ""); aerr != nil {
			s.Log.Warn("sso: audit", "error", aerr)
		}
		if !errors.Is(err, sso.ErrState) && !errors.Is(err, sso.ErrUnavailable) {
			s.Log.Info("sso: sign-in refused", "provider", provider, "result", ssoResult(err))
		}
		redirectLogin(w, r, ssoErrorCode(err))
		return
	}
	if out.Kind == sso.KindLink {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/me/profile?sso_linked="+url.QueryEscape(provider), http.StatusSeeOther)
		return
	}
	l, err := s.Store.UserForLogin(r.Context(), out.Username)
	if err != nil || !l.Enabled || l.ID != out.UserID {
		redirectLogin(w, r, "failed")
		return
	}
	if l.TOTPEnabled {
		// The second factor still applies: the login page continues with
		// the challenge, carried in the fragment so it never reaches a
		// server log or a Referer.
		plain, ok := s.createMFAChallenge(w, r, l.ID)
		if !ok {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/login#mfa="+url.QueryEscape(plain), http.StatusSeeOther)
		return
	}
	if !s.issueSession(w, r, l.ID, out.Username, "login-sso:"+provider) {
		return
	}
	next := out.Next
	if next == "" {
		next = "/"
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *server) ssoDisabled(w http.ResponseWriter) bool {
	if s.SSO == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "single sign-on is not configured")
		return true
	}
	return false
}

// ssoError answers a failed SSO administration call.
func (s *server) ssoError(w http.ResponseWriter, what string, err error) {
	var v *sso.ValidationError
	switch {
	case errors.As(err, &v):
		fields := make([]routing.FieldError, len(v.Fields))
		for i, f := range v.Fields {
			fields[i] = routing.FieldError{Path: f.Path, Message: f.Message}
		}
		writeFields(w, fields)
	case errors.Is(err, sso.ErrNoBreakGlass):
		writeError(w, http.StatusConflict, "no_break_glass", "flag at least one enabled local admin as break-glass before requiring single sign-on")
	case errors.Is(err, sso.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, sso.ErrLinked):
		writeError(w, http.StatusConflict, "already_linked", "that identity is linked to another user, or this user already has one at the provider")
	case errors.Is(err, sso.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "single sign-on needs HELLO_PUBLIC_URL and Valkey")
	case errors.Is(err, sso.ErrProviderOff):
		writeError(w, http.StatusConflict, "provider_disabled", "the provider is not configured and enabled")
	default:
		s.internal(w, what, err)
	}
}

func (s *server) getSSOSettings(w http.ResponseWriter, r *http.Request) {
	if s.ssoDisabled(w) {
		return
	}
	st, err := s.SSO.Settings(r.Context())
	if err != nil {
		s.ssoError(w, "sso settings", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) putSSOSettings(w http.ResponseWriter, r *http.Request) {
	if s.ssoDisabled(w) {
		return
	}
	var in sso.SettingsInput
	if !decode(w, r, &in) {
		return
	}
	if in.SetsSecret() && !actor(r).Scopes.Has(auth.ScopeSecrets) {
		writeError(w, http.StatusForbidden, "insufficient_scope", "setting a client secret needs the secrets scope")
		return
	}
	st, err := s.SSO.PutSettings(r.Context(), actor(r).String(), in)
	if err != nil {
		s.ssoError(w, "sso settings", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) testSSOProvider(w http.ResponseWriter, r *http.Request) {
	if s.ssoDisabled(w) {
		return
	}
	res, err := s.SSO.TestProvider(r.Context(), actor(r).String(), r.PathValue("provider"))
	if err != nil {
		s.ssoError(w, "sso provider", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// sessionUser is the signed-in user of a browser session, or 403.
func sessionUser(w http.ResponseWriter, r *http.Request) (auth.Actor, bool) {
	a := actor(r)
	if a.Kind != auth.KindSession || a.UserID <= 0 {
		writeError(w, http.StatusForbidden, "session_required", "identities are managed from a signed-in browser session")
		return a, false
	}
	return a, true
}

func (s *server) listMyIdentities(w http.ResponseWriter, r *http.Request) {
	a, ok := sessionUser(w, r)
	if !ok || s.ssoDisabled(w) {
		return
	}
	ids, err := s.SSO.Identities(r.Context(), a.UserID)
	if err != nil {
		s.ssoError(w, "identities", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ids))
}

// linkMyIdentity is POST /me/identities/{provider}/link: it answers the IdP
// URL the console navigates to, and sets the binding cookie. Linking is how a
// local user, admins included, gets SSO (T-7): never by email.
func (s *server) linkMyIdentity(w http.ResponseWriter, r *http.Request) {
	a, ok := sessionUser(w, r)
	if !ok || s.ssoDisabled(w) {
		return
	}
	u, binding, err := s.SSO.Start(r.Context(), r.PathValue("provider"), a.UserID, "")
	if err != nil {
		s.ssoError(w, "sso provider", err)
		return
	}
	setOIDCCookie(w, r, binding)
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
}

func (s *server) unlinkMyIdentity(w http.ResponseWriter, r *http.Request) {
	a, ok := sessionUser(w, r)
	if !ok || s.ssoDisabled(w) {
		return
	}
	if err := s.SSO.Unlink(r.Context(), a.String(), a.UserID, r.PathValue("provider")); err != nil {
		s.ssoError(w, "identity", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listUserIdentities(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.ssoDisabled(w) {
		return
	}
	ids, err := s.SSO.Identities(r.Context(), id)
	if err != nil {
		s.ssoError(w, "identities", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ids))
}

func (s *server) unlinkUserIdentity(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.ssoDisabled(w) {
		return
	}
	if err := s.SSO.Unlink(r.Context(), actor(r).String(), id, r.PathValue("provider")); err != nil {
		s.ssoError(w, "identity", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// startDirectoryConsent is POST /directory/connectors/{id}/consent: the
// Google administrator consent of an oauth_admin connector.
func (s *server) startDirectoryConsent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.directoryDisabled(w) || s.ssoDisabled(w) {
		return
	}
	if _, ok := sessionUser(w, r); !ok {
		return
	}
	u, binding, err := s.SSO.StartConsent(r.Context(), s.Directory, actor(r).String(), id)
	if err != nil {
		if errors.Is(err, sso.ErrUnavailable) {
			s.ssoError(w, "consent", err)
			return
		}
		s.directoryError(w, "directory connector", err)
		return
	}
	setOIDCCookie(w, r, binding)
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
}

// directoryConsentCallback is GET /directory/oauth/callback.
func (s *server) directoryConsentCallback(w http.ResponseWriter, r *http.Request) {
	clearOIDCCookie(w, r)
	w.Header().Set("Cache-Control", "no-store")
	if s.SSO == nil || s.Directory == nil {
		http.Redirect(w, r, "/integrations/directory?consent=failed", http.StatusSeeOther)
		return
	}
	if !s.SSO.AllowOIDC(r.Context(), callerAddr(r)) {
		http.Redirect(w, r, "/integrations/directory?consent=failed", http.StatusSeeOther)
		return
	}
	id, err := s.SSO.FinishConsent(r.Context(), s.Directory, r.URL.Query(), oidcBinding(r))
	if err != nil {
		var de *directory.Error
		code := "failed"
		if errors.As(err, &de) {
			code = de.Code
		}
		s.Log.Info("directory: consent refused", "connector", id, "result", code)
		q := "consent=failed&reason=" + url.QueryEscape(code)
		if id > 0 {
			q += "&connector=" + strconv.FormatInt(id, 10)
		}
		http.Redirect(w, r, "/integrations/directory?"+q, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/integrations/directory?consent=ok&connector="+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// directoryLogin is the password form for a synced user: an LDAP bind for
// the users of an AD connector with loginMethod ldap_bind (I-30), a generic
// refusal for everyone else. There is never a local-password fallback.
func (s *server) directoryLogin(w http.ResponseWriter, r *http.Request, l store.Login, username, password string) {
	refuse := func() {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid username or password")
	}
	if s.SSO == nil || !l.Enabled {
		auth.SpendUserPasswordCheck(password)
		refuse()
		return
	}
	err := s.SSO.BindLogin(r.Context(), l.ConnectorID, l.ExternalID, l.ID, password, callerAddr(r))
	if err != nil {
		var de *directory.Error
		if errors.As(err, &de) && !errors.Is(err, directory.ErrBindRefused) {
			s.Log.Warn("login: ldap bind login failed", "connector", l.ConnectorID, "code", de.Code)
		}
		refuse()
		return
	}
	if l.TOTPEnabled {
		s.startMFA(w, r, l.ID)
		return
	}
	if s.issueSession(w, r, l.ID, username, "login-ldap") {
		w.WriteHeader(http.StatusNoContent)
	}
}
