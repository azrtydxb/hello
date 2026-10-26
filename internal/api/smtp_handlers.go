package api

// System SMTP settings (Settings → Platform → Email). The password is sealed
// in the database like a trunk password; no response carries it and no log
// line mentions it. Voicemail notifications are sent from these settings by
// the mailer worker, never from a request.

import (
	"context"
	"net/http"
	"time"

	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/store"
)

// SMTPStore is the SMTP settings persistence; *store.Store implements it.
type SMTPStore interface {
	GetSMTPSettings(ctx context.Context) (store.SMTPSettings, error)
	SMTPSendSettings(ctx context.Context) (store.SMTPSendSettings, error)
	PutSMTPSettings(ctx context.Context, actor string, in store.SMTPInput) (store.SMTPSettings, error)
}

// SMTP sources, as reported to the console.
const (
	SMTPFromDatabase    = "database"
	SMTPFromEnvironment = "environment"
	SMTPNone            = "none"
)

// EffectiveSMTP returns the settings sends use: the saved ones, or else the
// SMTP_* environment of the deployment; source says which, "none" when
// neither is set.
func EffectiveSMTP(ctx context.Context, st SMTPStore, env mailer.Settings) (mailer.Settings, string, error) {
	v, err := st.SMTPSendSettings(ctx)
	if err != nil {
		return mailer.Settings{}, SMTPNone, err
	}
	if v.Host != "" {
		return mailer.Settings{Host: v.Host, Port: v.Port, Security: v.Security, Username: v.Username,
			Password: v.Password, From: v.From}, SMTPFromDatabase, nil
	}
	if env.Host != "" {
		return env, SMTPFromEnvironment, nil
	}
	return mailer.Settings{}, SMTPNone, nil
}

// emailDelivery reports whether the system can send voicemail email: the
// deployment says so, or SMTP settings are saved.
func (s *server) emailDelivery(ctx context.Context) bool {
	if s.EmailDelivery {
		return true
	}
	st, ok := s.Store.(SMTPStore)
	if !ok {
		return false
	}
	v, err := st.GetSMTPSettings(ctx)
	return err == nil && v.Host != ""
}

type smtpView struct {
	Host        string     `json:"host"`
	Port        int        `json:"port"`
	Security    string     `json:"security"`
	Username    string     `json:"username"`
	From        string     `json:"from"`
	PasswordSet bool       `json:"passwordSet"`
	Source      string     `json:"source"`
	UpdatedAt   *time.Time `json:"updatedAt"`
}

func (s *server) smtpStore(w http.ResponseWriter) (SMTPStore, bool) {
	st, ok := s.Store.(SMTPStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "email settings are not available")
	}
	return st, ok
}

func (s *server) viewSMTP(ctx context.Context, st SMTPStore) (smtpView, error) {
	stored, err := st.GetSMTPSettings(ctx)
	if err != nil {
		return smtpView{}, err
	}
	if stored.Host != "" {
		return smtpView{Host: stored.Host, Port: stored.Port, Security: stored.Security, Username: stored.Username,
			From: stored.From, PasswordSet: stored.PasswordSet, Source: SMTPFromDatabase, UpdatedAt: &stored.UpdatedAt}, nil
	}
	if e := s.SMTPEnv; e.Host != "" {
		return smtpView{Host: e.Host, Port: e.Port, Security: e.Security, Username: e.Username, From: e.From,
			PasswordSet: e.Password != "", Source: SMTPFromEnvironment}, nil
	}
	return smtpView{Port: 587, Security: mailer.SecuritySTARTTLS, Source: SMTPNone}, nil
}

func (s *server) getSMTP(w http.ResponseWriter, r *http.Request) {
	st, ok := s.smtpStore(w)
	if !ok {
		return
	}
	v, err := s.viewSMTP(r.Context(), st)
	if err != nil {
		s.internal(w, "get smtp settings", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) putSMTP(w http.ResponseWriter, r *http.Request) {
	st, ok := s.smtpStore(w)
	if !ok {
		return
	}
	var in struct {
		Host     string  `json:"host"`
		Port     int     `json:"port"`
		Security string  `json:"security"`
		Username string  `json:"username"`
		From     string  `json:"from"`
		Password *string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Host != "" {
		if in.Security == mailer.SecurityOpportunistic {
			writeFields(w, smtpFieldError("security must be none, starttls or tls"))
			return
		}
		probe := mailer.Settings{Host: in.Host, Port: in.Port, Security: in.Security, Username: in.Username, From: in.From}
		if err := probe.Validate(); err != nil {
			writeFields(w, smtpFieldError(err.Error()))
			return
		}
		if in.Password != nil && len(*in.Password) > 512 {
			writeFields(w, smtpFieldError("password: must be at most 512 characters"))
			return
		}
	} else {
		// An empty host clears the saved settings (the deployment's
		// environment, if any, applies again).
		in = struct {
			Host     string  `json:"host"`
			Port     int     `json:"port"`
			Security string  `json:"security"`
			Username string  `json:"username"`
			From     string  `json:"from"`
			Password *string `json:"password"`
		}{Port: 587, Security: mailer.SecuritySTARTTLS, Password: ptr("")}
	}
	if _, err := st.PutSMTPSettings(r.Context(), actor(r).String(), store.SMTPInput{
		Host: in.Host, Port: in.Port, Security: in.Security, Username: in.Username, From: in.From, Password: in.Password}); err != nil {
		s.storeError(w, "smtp settings", err)
		return
	}
	v, err := s.viewSMTP(r.Context(), st)
	if err != nil {
		s.internal(w, "get smtp settings", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func ptr[T any](v T) *T { return &v }

// smtpFieldError maps a Settings.Validate message to the field it names.
func smtpFieldError(msg string) fieldErrs {
	var f fieldErrs
	field := "host"
	for _, k := range []string{"port", "security", "from", "credentials", "password"} {
		if len(msg) >= len(k) && msg[:len(k)] == k {
			field = k
		}
	}
	if field == "credentials" {
		field = "username"
	}
	f.add(field, "%s", msg)
	return f
}

// testSMTP is POST /api/v1/settings/smtp/test: it sends one test email with
// the settings in force, now, and reports the outcome.
func (s *server) testSMTP(w http.ResponseWriter, r *http.Request) {
	st, ok := s.smtpStore(w)
	if !ok {
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !mailer.ValidAddress(in.To) {
		writeFields(w, fieldErrs{{Path: "to", Message: "must be an email address"}})
		return
	}
	cfg, _, err := EffectiveSMTP(r.Context(), st, s.SMTPEnv)
	if err != nil {
		s.internal(w, "test smtp: read settings", err)
		return
	}
	if cfg.Host == "" {
		writeError(w, http.StatusConflict, "not_configured", "email is not configured")
		return
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusConflict, "not_configured", "the saved settings are not usable: "+err.Error())
		return
	}
	msg, err := mailer.BuildTest(cfg.From, in.To, time.Now())
	if err != nil {
		s.internal(w, "test smtp: build", err)
		return
	}
	deliver := s.SMTPDeliver
	if deliver == nil {
		deliver = mailer.Deliver
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := deliver(ctx, cfg, in.To, msg); err != nil {
		s.Log.Warn("smtp test failed", "error", err)
		writeError(w, http.StatusBadGateway, "smtp_failed", "sending failed: "+err.Error())
		return
	}
	if err := s.Store.Audit(r.Context(), actor(r).String(), "test-email", "smtp_settings", "1"); err != nil {
		s.Log.Warn("smtp test: audit", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
