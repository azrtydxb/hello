package mailer

import (
	"context"
	"net/smtp"
	"strconv"
)

// SMTPSender delivers built messages with net/smtp (spec: SMTP stays
// stdlib).
type SMTPSender struct {
	Addr string // host:port
	Auth smtp.Auth
}

// NewSMTPSender builds the stdlib sender for host:port. Credentials are
// optional: a server without AUTH is used anonymously.
func NewSMTPSender(host string, port int, user, pass string) *SMTPSender {
	s := &SMTPSender{Addr: host + ":" + strconv.Itoa(port)}
	if user != "" {
		s.Auth = smtp.PlainAuth("", user, pass, host)
	}
	return s
}

// Send hands msg to the SMTP server.
func (s *SMTPSender) Send(ctx context.Context, from, to string, msg []byte) error {
	// smtp.SendMail has no context; the worker bounds the call with the
	// shared FetchTimeout, so a hanging server cannot stall the queue.
	return smtp.SendMail(s.Addr, s.Auth, from, []string{to}, msg)
}
