package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// DiagnosticsLive is the live state the Diagnostics view reads;
// *LazyValkey implements it.
type DiagnosticsLive interface {
	Bindings(ctx context.Context, aor string) ([]livestate.Binding, error)
	RegisterAttempts(ctx context.Context, device string) ([]livestate.RegisterAttempt, error)
	AuthFailures(ctx context.Context, ip string) (livestate.AuthFailure, bool, error)
	AllAuthFailures(ctx context.Context) ([]livestate.AuthFailure, error)
	ClearAuthFailures(ctx context.Context, ip string) error
}

// defaultAuthFailLimit is hello-sip's HELLO_SIP_AUTH_FAIL_LIMIT default.
const defaultAuthFailLimit = 10

func (s *server) authFailLimit() int64 {
	if s.AuthFailLimit > 0 {
		return int64(s.AuthFailLimit)
	}
	return defaultAuthFailLimit
}

// authSource is a source IP's failed-auth counter and whether it is at the
// limit, i.e. hello-sip answers its requests 403 without checking them.
type authSource struct {
	livestate.AuthFailure
	Blocked bool `json:"blocked"`
}

// registerVerdict says why a device is not registered, from what Hello
// measured; it is absent while the device is registered.
type registerVerdict struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type deviceDiagnostics struct {
	DeviceID    int64  `json:"deviceId"`
	Device      string `json:"device"`
	ExtensionID int64  `json:"extensionId"`
	Enabled     bool   `json:"enabled"`
	AOR         string `json:"aor"`
	Registered  bool   `json:"registered"`
	// Bindings are the device's current contacts; Attempts its recent
	// REGISTERs, newest first, kept for AttemptsRetentionSeconds after the
	// last one.
	Bindings                 []livestate.Binding         `json:"bindings"`
	Attempts                 []livestate.RegisterAttempt `json:"attempts"`
	AttemptsRetentionSeconds int64                       `json:"attemptsRetentionSeconds"`
	// Source is the failed-auth counter of the last attempt's IP; absent
	// when there was no attempt or the IP has no failures in its window.
	Source        *authSource      `json:"source"`
	AuthFailLimit int64            `json:"authFailLimit"`
	Verdict       *registerVerdict `json:"verdict"`
}

// deviceDiagnostics is GET /api/v1/diagnostics/devices/{id}: the device's
// bindings, its recent REGISTER attempts and the failed-auth state of its
// last source, with a verdict computed only from those facts.
func (s *server) deviceDiagnostics(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.Store.GetDevice(r.Context(), id)
	if err != nil {
		s.storeError(w, "device", err)
		return
	}
	if s.Diagnostics == nil {
		s.liveDown(w, "device diagnostics", errors.New("no diagnostics store configured"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	aor := "sip:" + d.SIPUsername + "@" + s.SIPDomain
	bs, err := s.Diagnostics.Bindings(ctx, aor)
	if err != nil {
		s.liveDown(w, "device diagnostics", err)
		return
	}
	attempts, err := s.Diagnostics.RegisterAttempts(ctx, d.SIPUsername)
	if err != nil {
		s.liveDown(w, "device diagnostics", err)
		return
	}
	var src *authSource
	if len(attempts) > 0 && attempts[0].IP != "" {
		f, found, err := s.Diagnostics.AuthFailures(ctx, attempts[0].IP)
		if err != nil {
			s.liveDown(w, "device diagnostics", err)
			return
		}
		if found {
			src = &authSource{AuthFailure: f, Blocked: f.Failures >= s.authFailLimit()}
		}
	}
	for i := range bs {
		bs[i].Path = redactPath(bs[i].Path)
	}
	if bs == nil {
		bs = []livestate.Binding{}
	}
	if attempts == nil {
		attempts = []livestate.RegisterAttempt{}
	}
	writeJSON(w, http.StatusOK, deviceDiagnostics{
		DeviceID: d.ID, Device: d.SIPUsername, ExtensionID: d.ExtensionID, Enabled: d.Enabled, AOR: aor,
		Registered: len(bs) > 0, Bindings: bs, Attempts: attempts,
		AttemptsRetentionSeconds: int64(livestate.RegisterAttemptsTTL.Seconds()),
		Source:                   src, AuthFailLimit: s.authFailLimit(),
		Verdict: verdictFor(d, aor, s.SIPDomain, len(bs) > 0, attempts, src, s.authFailLimit()),
	})
}

// verdictFor explains a missing registration from the measured facts, most
// decisive first. It never guesses past them: a code Hello cannot explain
// is reported as it was answered.
func verdictFor(d store.Device, aor, domain string, registered bool, attempts []livestate.RegisterAttempt,
	src *authSource, limit int64) *registerVerdict {
	v := func(code, format string, args ...any) *registerVerdict {
		return &registerVerdict{Code: code, Message: fmt.Sprintf(format, args...)}
	}
	switch {
	case registered:
		return nil
	case !d.Enabled:
		return v("disabled", "%s is disabled. Hello does not accept a REGISTER from a disabled device; enable it to register.", d.SIPUsername)
	case src != nil && src.Blocked:
		return v("throttled", "Source %s is blocked by failed-auth throttling: %d failed attempts in the current window (limit %d). Hello answers 403 Forbidden to it until the window ends.",
			src.IP, src.Failures, limit)
	case len(attempts) == 0:
		return v("no_register", "Hello has received no REGISTER for %s in the last %s.", aor, retention())
	}
	last := attempts[0]
	switch {
	case last.Code >= 200 && last.Code < 300:
		return v("expired", "The last REGISTER was accepted (%d %s), but its binding has since expired or been removed.", last.Code, last.Reason)
	case last.Code == http.StatusUnauthorized && last.Stale:
		return v("stale_nonce", "The last REGISTER was answered 401 with stale=true: Hello did not accept its nonce, and the phone has not retried with a fresh one.")
	case last.Code == http.StatusUnauthorized && last.Credentials:
		return v("auth_failed", "Hello rejected the credentials in the last REGISTER (401 Unauthorized after a challenge). Check the SIP username, password and domain %s on the phone.", domain)
	case last.Code == http.StatusUnauthorized:
		return v("challenge_unanswered", "The phone did not answer Hello's digest challenge (401 Unauthorized) to its last REGISTER.")
	case last.Code == http.StatusForbidden:
		return v("forbidden", "Hello refused the last REGISTER (403 %s).", last.Reason)
	case last.Code == 423:
		return v("interval_too_brief", "The phone asked for a registration interval below Hello's minimum (423 %s).", last.Reason)
	case last.Code == http.StatusServiceUnavailable:
		return v("unavailable", "Hello could not serve the last REGISTER (503 %s): the node was not ready or live state was unreachable.", last.Reason)
	default:
		return v("rejected", "Hello answered the last REGISTER %d %s.", last.Code, last.Reason)
	}
}

// retention is the attempt history's lifetime in words ("1 h").
func retention() string {
	return fmt.Sprintf("%d h", int(livestate.RegisterAttemptsTTL.Hours()))
}

// listAuthFailures is GET /api/v1/diagnostics/auth-failures: every source
// IP with failed authentications in its current window, and whether it is
// blocked.
func (s *server) listAuthFailures(w http.ResponseWriter, r *http.Request) {
	if s.Diagnostics == nil {
		s.liveDown(w, "auth failures", errors.New("no diagnostics store configured"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	fs, err := s.Diagnostics.AllAuthFailures(ctx)
	if err != nil {
		s.liveDown(w, "auth failures", err)
		return
	}
	out := make([]authSource, 0, len(fs))
	for _, f := range fs {
		out = append(out, authSource{AuthFailure: f, Blocked: f.Failures >= s.authFailLimit()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "limit": s.authFailLimit()})
}

// clearAuthFailures is DELETE /api/v1/diagnostics/auth-failures/{ip}: it
// resets the source's counter, which unblocks it at once.
func (s *server) clearAuthFailures(w http.ResponseWriter, r *http.Request) {
	ip, err := netip.ParseAddr(r.PathValue("ip"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "source: not found")
		return
	}
	if s.Diagnostics == nil {
		s.liveDown(w, "clear auth failures", errors.New("no diagnostics store configured"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	if err := s.Diagnostics.ClearAuthFailures(ctx, ip.Unmap().String()); err != nil {
		s.liveDown(w, "clear auth failures", err)
		return
	}
	s.Log.Info("failed-auth counter cleared", "ip", ip.String())
	w.WriteHeader(http.StatusNoContent)
}
