package api

// The browser softphone's devices (clients-webrtc-mobile-video S-13, S-14,
// S-23, S-24): the /me/devices routes enrol, list, refresh and revoke the
// caller's own web devices, and /me/ice-servers hands out STUN and TURN
// servers with short-lived credentials.
//
// Everything is scoped to the signed-in user and needs a browser session:
// no route takes a user id, an extension or device id is resolved only
// through the caller's own rows (someone else's answers 404, like a missing
// one), and a bearer token or service account cannot mint SIP credentials.
// The SIP password is derived, never stored, and appears only in the
// response that mints it; it is never logged.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/turn"
)

// ClientStore is the web device persistence; *store.Store implements it.
type ClientStore interface {
	CreateClientDevice(ctx context.Context, actor string, in store.NewClientDevice) (store.ClientDevice, error)
	ListClientDevices(ctx context.Context, userID int64) ([]store.ClientDevice, error)
	RefreshClientDevice(ctx context.Context, userID, id int64) (store.ClientDevice, error)
	RevokeClientDevice(ctx context.Context, actor string, userID, id int64) error
}

var _ ClientStore = (*store.Store)(nil)

// ClientConfig is the deployment's softphone settings.
type ClientConfig struct {
	// AuthKey derives the digest secret of a web device (HELLO_CLIENT_AUTH_KEY);
	// empty turns web devices off. hello-sip holds the same key.
	AuthKey string
	// MaxDevices is how many web devices one user may have (default 5).
	MaxDevices int
	// WSSURL is where the softphone registers (HELLO_WSS_URL).
	WSSURL string
	// WebLifetime is how long a web credential lives (default 12h).
	WebLifetime time.Duration
	// ICE is the STUN and TURN settings behind /me/ice-servers.
	ICE turn.Config
	// ICEIssued counts the TURN credentials handed out; nil counts nothing.
	// Issuing is audited by this count, not per credential.
	ICEIssued prometheus.Counter
	// Now replaces the clock in tests.
	Now func() time.Time
}

func (c ClientConfig) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c ClientConfig) lifetime() time.Duration {
	if c.WebLifetime > 0 {
		return c.WebLifetime
	}
	return 12 * time.Hour
}

func (c ClientConfig) maxDevices() int {
	if c.MaxDevices > 0 {
		return c.MaxDevices
	}
	return 5
}

// credentialRate bounds how often one user may enrol, refresh or fetch ICE
// servers: a browser refreshes once per call or per half-life, so this is
// generous for a person and tight for a script.
const credentialRate = 60

// rateLimiter is a fixed-window counter per user, kept in this process.
type rateLimiter struct {
	mu     sync.Mutex
	window time.Time
	counts map[int64]int
}

func (l *rateLimiter) allow(user int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil || now.Sub(l.window) >= time.Minute || now.Before(l.window) {
		l.window, l.counts = now, map[int64]int{}
	}
	l.counts[user]++
	return l.counts[user] <= credentialRate
}

// clientUser resolves the signed-in user for a client route: a browser
// session of a user, with web devices configured and a limit not exceeded.
func (s *server) clientUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return 0, false
	}
	if s.Clients == nil || s.ClientAuth.AuthKey == "" {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "web devices are not configured (HELLO_CLIENT_AUTH_KEY)")
		return 0, false
	}
	if !s.credLimit.allow(uid, s.ClientAuth.now()) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again shortly")
		return 0, false
	}
	return uid, true
}

// deviceCredentials is what a client needs to REGISTER.
type deviceCredentials struct {
	DeviceID    int64     `json:"deviceId"`
	SIPUsername string    `json:"sipUsername"`
	SIPPassword string    `json:"sipPassword"`
	Realm       string    `json:"realm"`
	WSSURL      string    `json:"wssUrl"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// mintCredentials derives the expiring username and secret of a web device.
func (s *server) mintCredentials(d store.ClientDevice) deviceCredentials {
	exp := s.ClientAuth.now().Add(s.ClientAuth.lifetime()).Truncate(time.Second)
	name := auth.DerivedUsername(d.SIPUsername, exp)
	return deviceCredentials{
		DeviceID: d.ID, SIPUsername: name, Realm: s.SIPDomain, WSSURL: s.ClientAuth.WSSURL, ExpiresAt: exp.UTC(),
		SIPPassword: auth.DerivedSecret([]byte(s.ClientAuth.AuthKey), name, s.SIPDomain),
	}
}

// deviceView is a device as its owner sees it.
type deviceView struct {
	store.ClientDevice
	// Registered is whether the device has a live registration now; nil when
	// the live state could not be read.
	Registered *bool `json:"registered"`
}

func (s *server) deviceViews(ctx context.Context, ds []store.ClientDevice) []deviceView {
	out := make([]deviceView, len(ds))
	live := map[string]bool{}
	known := false
	if s.Live != nil && len(ds) > 0 {
		lctx, cancel := context.WithTimeout(ctx, liveTimeout)
		defer cancel()
		if bs, err := s.Live.AllBindings(lctx); err == nil {
			known = true
			for _, b := range bs {
				live[b.Device] = true
			}
		}
	}
	for i, d := range ds {
		out[i] = deviceView{ClientDevice: d}
		if known {
			reg := live[d.SIPUsername]
			out[i].Registered = &reg
		}
	}
	return out
}

func (s *server) listMyDevices(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.clientUser(w, r)
	if !ok {
		return
	}
	ds, err := s.Clients.ListClientDevices(r.Context(), uid)
	if err != nil {
		s.storeError(w, "device", err)
		return
	}
	writeJSON(w, http.StatusOK, items(s.deviceViews(r.Context(), ds)))
}

// createMyDevice enrols a web device on one of the caller's extensions.
func (s *server) createMyDevice(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.clientUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Label       string `json:"label"`
		Kind        string `json:"kind"`
		ExtensionID *int64 `json:"extensionId"`
		Platform    string `json:"platform"`
		AppVersion  string `json:"appVersion"`
	}
	if !decode(w, r, &in) {
		return
	}
	var f fieldErrs
	label, labelOK := validName(in.Label)
	if !labelOK || len([]rune(label)) > 64 {
		f.add("label", "must be 1-64 printable characters")
	}
	// The mobile apps are not built yet (decision 2026-10-25): only web.
	if in.Kind != "" && in.Kind != auth.KindWeb {
		f.add("kind", "must be web")
	}
	if in.Platform != "" && !printable(in.Platform, 64, true) {
		f.add("platform", "must be at most 64 printable characters")
	}
	if in.AppVersion != "" && !printable(in.AppVersion, 32, false) {
		f.add("appVersion", "must be at most 32 printable characters without spaces")
	}
	if in.ExtensionID != nil && *in.ExtensionID <= 0 {
		f.add("extensionId", "must be positive")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	ext := int64(0)
	if in.ExtensionID != nil {
		ext = *in.ExtensionID
	}
	exts, err := s.Store.UserExtensions(r.Context(), uid)
	if err != nil {
		s.internal(w, "me: extensions", err)
		return
	}
	if ext == 0 {
		if len(exts) == 0 {
			writeError(w, http.StatusConflict, "no_extension", "you have no extension to attach a device to")
			return
		}
		ext = exts[0].ID
	}
	d, err := s.Clients.CreateClientDevice(r.Context(), actor(r).String(), store.NewClientDevice{
		UserID: uid, ExtensionID: ext, Kind: auth.KindWeb, Label: label, Platform: in.Platform, AppVersion: in.AppVersion,
		Realm: s.SIPDomain, Max: s.ClientAuth.maxDevices(),
	})
	switch {
	case errors.Is(err, store.ErrDeviceLimit):
		writeError(w, http.StatusConflict, "device_limit", "you have the most devices allowed ("+strconv.Itoa(s.ClientAuth.maxDevices())+"); remove one first")
		return
	case err != nil:
		s.storeError(w, "extension", err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		deviceCredentials
		Device deviceView  `json:"device"`
		ICE    turn.Result `json:"ice"`
	}{s.mintCredentials(d), s.deviceViews(r.Context(), []store.ClientDevice{d})[0], s.ClientAuth.ICE.Servers(strconv.FormatInt(d.ID, 10))})
}

// refreshMyDevice mints new credentials for one of the caller's devices.
func (s *server) refreshMyDevice(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.clientUser(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.Clients.RefreshClientDevice(r.Context(), uid, id)
	if err != nil {
		s.storeError(w, "device", err)
		return
	}
	writeJSON(w, http.StatusOK, s.mintCredentials(d))
}

// deleteMyDevice revokes one of the caller's devices.
func (s *server) deleteMyDevice(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.clientUser(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Clients.RevokeClientDevice(r.Context(), actor(r).String(), uid, id); err != nil {
		s.storeError(w, "device", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getICEServers returns STUN and TURN servers with a fresh TURN credential.
// The optional deviceId names one of the caller's own devices; the TURN
// username carries it so coturn's logs tell clients apart.
func (s *server) getICEServers(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	if !s.ClientAuth.ICE.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "STUN and TURN are not configured (HELLO_STUN_URLS, HELLO_TURN_URLS)")
		return
	}
	if !s.credLimit.allow(uid, s.ClientAuth.now()) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again shortly")
		return
	}
	name := "u" + strconv.FormatInt(uid, 10)
	if v := r.URL.Query().Get("deviceId"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 || s.Clients == nil {
			writeError(w, http.StatusNotFound, "not_found", "device: not found")
			return
		}
		ds, err := s.Clients.ListClientDevices(r.Context(), uid)
		if err != nil {
			s.internal(w, "me: devices", err)
			return
		}
		found := false
		for _, d := range ds {
			found = found || d.ID == id
		}
		if !found {
			writeError(w, http.StatusNotFound, "not_found", "device: not found")
			return
		}
		name = v
	}
	if s.ClientAuth.ICEIssued != nil {
		s.ClientAuth.ICEIssued.Inc()
	}
	writeJSON(w, http.StatusOK, s.ClientAuth.ICE.Servers(name))
}
