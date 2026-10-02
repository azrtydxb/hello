package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

var (
	numberRe   = regexp.MustCompile(`^[0-9]{2,10}$`)
	usernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

const maxNameLen = 100

// validName trims a display name and checks it is 1–100 printable runes.
func validName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxNameLen {
		return "", false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return "", false
		}
	}
	return s, true
}

// Auth.

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Username == "" || in.Password == "" {
		badRequest(w, "username and password are required")
		return
	}
	id, hash, err := s.Store.UserByName(r.Context(), in.Username)
	switch {
	case err == nil:
		if !auth.CheckPassword(hash, in.Password) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid username or password")
			return
		}
	case errors.Is(err, store.ErrNotFound):
		auth.SpendPasswordCheck(in.Password)
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid username or password")
		return
	default:
		s.internal(w, "login: look up user", err)
		return
	}
	plain, tokenHash := auth.NewToken()
	expires := time.Now().Add(s.SessionTTL)
	if err := s.Store.CreateSession(r.Context(), id, tokenHash, expires); err != nil {
		s.internal(w, "login: create session", err)
		return
	}
	if err := s.Store.Audit(r.Context(), "user:"+in.Username, "login", "user", strconv.FormatInt(id, 10)); err != nil {
		s.Log.Warn("login: audit", "error", err)
	}
	// Secure follows the request scheme (Shared contracts), so the lab works
	// over plain HTTP; HttpOnly and SameSite=Strict are always set.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is set whenever the request is HTTPS.
		Name:     auth.SessionCookie,
		Value:    plain,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(s.SessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPS(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

// isHTTPS reports whether the client reached us over HTTPS, directly or
// through the UI's reverse proxy.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil && c.Value != "" {
		if err := s.Store.DeleteSession(r.Context(), auth.HashToken(c.Value)); err != nil {
			s.internal(w, "logout: delete session", err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure is set whenever the request is HTTPS.
		Name: auth.SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"username": actor(r).Username})
}

// Tokens.

// createdToken is the only shape that ever carries an API token: the
// create response.
type createdToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	Token      string     `json:"token"`
}

func (s *server) listTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.ListTokens(r.Context(), actor(r).UserID)
	if err != nil {
		s.internal(w, "list tokens", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ts))
}

func (s *server) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	name, ok := validName(in.Name)
	if !ok {
		badRequest(w, "name must be 1-100 printable characters")
		return
	}
	a := actor(r)
	plain, hash := auth.NewToken()
	t, err := s.Store.CreateToken(r.Context(), a.String(), a.UserID, name, hash)
	if err != nil {
		s.storeError(w, "token", err)
		return
	}
	writeJSON(w, http.StatusCreated, createdToken{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, Token: plain})
}

func (s *server) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a := actor(r)
	if err := s.Store.DeleteToken(r.Context(), a.String(), a.UserID, id); err != nil {
		s.storeError(w, "token", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Extensions.

func (s *server) listExtensions(w http.ResponseWriter, r *http.Request) {
	es, err := s.Store.ListExtensions(r.Context())
	if err != nil {
		s.internal(w, "list extensions", err)
		return
	}
	writeJSON(w, http.StatusOK, items(es))
}

func (s *server) getExtension(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	e, err := s.Store.GetExtension(r.Context(), id)
	if err != nil {
		s.storeError(w, "extension", err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

const (
	numberRule         = "number must be 2-10 digits"
	externalNumberRule = "externalNumber must be empty or 2-20 digits with an optional leading +"
)

func (s *server) createExtension(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Number         string `json:"number"`
		Name           string `json:"name"`
		ExternalNumber string `json:"externalNumber"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !numberRe.MatchString(in.Number) {
		badRequest(w, numberRule)
		return
	}
	name, ok := validName(in.Name)
	if !ok {
		badRequest(w, "name must be 1-100 printable characters")
		return
	}
	if !callerIDRe.MatchString(in.ExternalNumber) {
		badRequest(w, externalNumberRule)
		return
	}
	e, err := s.Store.CreateExtension(r.Context(), actor(r).String(), in.Number, name, in.ExternalNumber, s.check())
	if err != nil {
		s.configError(w, "extension", err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *server) updateExtension(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Number         *string `json:"number"`
		Name           *string `json:"name"`
		ExternalNumber *string `json:"externalNumber"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Number == nil && in.Name == nil && in.ExternalNumber == nil {
		badRequest(w, "number, name or externalNumber is required")
		return
	}
	if in.ExternalNumber != nil && !callerIDRe.MatchString(*in.ExternalNumber) {
		badRequest(w, externalNumberRule)
		return
	}
	if in.Number != nil && !numberRe.MatchString(*in.Number) {
		badRequest(w, numberRule)
		return
	}
	if in.Name != nil {
		name, ok := validName(*in.Name)
		if !ok {
			badRequest(w, "name must be 1-100 printable characters")
			return
		}
		in.Name = &name
	}
	e, err := s.Store.UpdateExtension(r.Context(), actor(r).String(), id,
		store.ExtensionChange{Number: in.Number, Name: in.Name, ExternalNumber: in.ExternalNumber}, s.check())
	if err != nil {
		s.configError(w, "extension", err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) deleteExtension(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteExtension(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "extension", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Devices.

// deviceWithSecret is the only shape that ever carries a device secret: the
// create and rotate-secret responses.
type deviceWithSecret struct {
	store.Device
	Secret string `json:"secret"`
}

func (s *server) listDevices(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.ListDevices(r.Context())
	if err != nil {
		s.internal(w, "list devices", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ds))
}

func (s *server) getDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.Store.GetDevice(r.Context(), id)
	if err != nil {
		s.storeError(w, "device", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) createDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExtensionID int64  `json:"extensionId"`
		SIPUsername string `json:"sipUsername"`
		Enabled     *bool  `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExtensionID <= 0 {
		badRequest(w, "extensionId is required")
		return
	}
	if !usernameRe.MatchString(in.SIPUsername) {
		badRequest(w, "sipUsername must be 1-64 of A-Z a-z 0-9 . _ -")
		return
	}
	enabled := in.Enabled == nil || *in.Enabled
	secret := auth.NewDeviceSecret()
	d, err := s.Store.CreateDevice(r.Context(), actor(r).String(), store.NewDevice{
		ExtensionID: in.ExtensionID, SIPUsername: in.SIPUsername, Enabled: enabled,
		Realm: s.SIPDomain, Secret: secret,
	})
	if err != nil {
		s.storeError(w, "device", err)
		return
	}
	writeJSON(w, http.StatusCreated, deviceWithSecret{Device: d, Secret: secret})
}

func (s *server) updateDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled     *bool  `json:"enabled"`
		ExtensionID *int64 `json:"extensionId"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil && in.ExtensionID == nil {
		badRequest(w, "enabled or extensionId is required")
		return
	}
	if in.ExtensionID != nil && *in.ExtensionID <= 0 {
		badRequest(w, "extensionId must be positive")
		return
	}
	d, err := s.Store.UpdateDevice(r.Context(), actor(r).String(), id, in.Enabled, in.ExtensionID)
	if err != nil {
		s.storeError(w, "device", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) rotateSecret(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	secret := auth.NewDeviceSecret()
	d, err := s.Store.RotateDeviceSecret(r.Context(), actor(r).String(), id, s.SIPDomain, secret)
	if err != nil {
		s.storeError(w, "device", err)
		return
	}
	writeJSON(w, http.StatusOK, deviceWithSecret{Device: d, Secret: secret})
}

func (s *server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteDevice(r.Context(), actor(r).String(), id); err != nil {
		s.storeError(w, "device", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Live state and history.

func (s *server) registrations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	bs, err := s.Live.AllBindings(ctx)
	if err != nil {
		s.liveDown(w, "list registrations", err)
		return
	}
	for i := range bs {
		bs[i].Path = redactPath(bs[i].Path)
	}
	writeJSON(w, http.StatusOK, items(bs))
}

// liveDown answers a live view while Valkey is unreachable: the rest of
// management keeps working, so this is 503 for the view, not node failure.
func (s *server) liveDown(w http.ResponseWriter, what string, err error) {
	s.Log.Warn("live state unavailable", "op", what, "error", err)
	writeError(w, http.StatusServiceUnavailable, "unavailable", "live state (Valkey) is unavailable; try again shortly")
}

// hflowRe matches the edge flow token in a Path URI; it is a routing
// credential for the SIP nodes and is not shown to API clients.
var hflowRe = regexp.MustCompile(`hflow=[^;>]*`)

func redactPath(path []string) []string {
	out := make([]string, len(path))
	for i, p := range path {
		out[i] = hflowRe.ReplaceAllString(p, "hflow=REDACTED")
	}
	return out
}

func (s *server) calls(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	cs, err := s.Live.Calls(ctx)
	if err != nil {
		s.liveDown(w, "list calls", err)
		return
	}
	writeJSON(w, http.StatusOK, items(cs))
}

const (
	defaultCDRLimit = 50
	maxCDRLimit     = 200
)

func (s *server) cdrs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var before int64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "before must be a positive CDR id")
			return
		}
		before = n
	}
	limit := defaultCDRLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxCDRLimit {
			badRequest(w, "limit must be 1-200")
			return
		}
		limit = n
	}
	cs, next, err := s.Store.ListCDRs(r.Context(), before, limit)
	if err != nil {
		s.internal(w, "list cdrs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": cs, "next": next})
}
