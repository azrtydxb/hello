package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Transport security modes.
const (
	SecurityNone     = "none"
	SecuritySTARTTLS = "starttls"
	SecurityTLS      = "tls"
	// SecurityOpportunistic upgrades with STARTTLS when the server offers it
	// and otherwise carries on in clear. It exists for the SMTP_* environment
	// of deployments that predate the console settings; the console offers
	// the other three only.
	SecurityOpportunistic = "opportunistic"
)

// Settings are the SMTP settings a send uses.
type Settings struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     string
}

// Validate checks settings the way the console and API accept them.
func (s Settings) Validate() error {
	switch {
	case strings.TrimSpace(s.Host) == "" || strings.ContainsAny(s.Host, " \r\n/"):
		return errors.New("host is required")
	case s.Port < 1 || s.Port > 65535:
		return errors.New("port must be 1-65535")
	case s.Security != SecurityNone && s.Security != SecuritySTARTTLS && s.Security != SecurityTLS && s.Security != SecurityOpportunistic:
		return errors.New("security must be none, starttls or tls")
	case !ValidAddress(s.From):
		return errors.New("from must be an email address")
	case strings.ContainsAny(s.Username, "\r\n") || strings.ContainsAny(s.Password, "\r\n"):
		return errors.New("credentials must not contain line breaks")
	}
	return nil
}

// ValidAddress reports whether v is a bare email address, with no display
// name and no line breaks.
func ValidAddress(v string) bool {
	if v == "" || len(v) > 254 || strings.ContainsAny(v, "\r\n<>\" ,;") {
		return false
	}
	a, err := mail.ParseAddress(v)
	return err == nil && a.Address == v
}

// tlsHook lets tests trust a self-signed server certificate.
var tlsHook func(*tls.Config)

// Deliver sends msg to one recipient over SMTP with the settings' transport
// security. Credentials go only over a TLS-protected connection (or to
// localhost): net/smtp refuses PLAIN auth otherwise, and so does this.
func Deliver(ctx context.Context, s Settings, to string, msg []byte) error {
	if err := s.Validate(); err != nil {
		return fmt.Errorf("smtp settings: %w", err)
	}
	if !ValidAddress(to) {
		return errors.New("recipient is not an email address")
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// Closing the connection aborts a blocked read when ctx ends.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	tlsCfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	if tlsHook != nil {
		tlsHook(tlsCfg)
	}
	if s.Security == SecurityTLS {
		conn = tls.Client(conn, tlsCfg)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("greeting: %w", err)
	}
	defer func() { _ = c.Close() }()
	if s.Security == SecuritySTARTTLS || s.Security == SecurityOpportunistic {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		} else if s.Security == SecuritySTARTTLS {
			return errors.New("server does not offer STARTTLS")
		}
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("data: %w", err)
	}
	return c.Quit()
}
