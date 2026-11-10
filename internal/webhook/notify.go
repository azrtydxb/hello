package webhook

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"mime"
	"time"

	"github.com/azrtydxb/hello/internal/mailer"
)

// MailNotifier returns a Dispatcher.Notify that e-mails the administrators
// when an endpoint is switched off. It does nothing while SMTP is not
// configured. The message names the endpoint and the reason, never its URL
// (which may carry a token) or any event data.
func MailNotifier(
	admins func(ctx context.Context) ([]string, error),
	settings func(ctx context.Context) (mailer.Settings, bool, error),
	deliver func(ctx context.Context, s mailer.Settings, to string, msg []byte) error,
	log *slog.Logger,
) func(ctx context.Context, e Endpoint, reason string) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if deliver == nil {
		deliver = mailer.Deliver
	}
	return func(ctx context.Context, e Endpoint, reason string) {
		s, ok, err := settings(ctx)
		if err != nil || !ok {
			return
		}
		to, err := admins(ctx)
		if err != nil {
			log.Warn("webhooks: finding the administrators to e-mail", "error", err)
			return
		}
		for _, addr := range to {
			msg, err := disabledMessage(s.From, addr, e, reason, time.Now())
			if err != nil {
				continue
			}
			if err := deliver(ctx, s, addr, msg); err != nil {
				log.Warn("webhooks: e-mailing an administrator", "error", err)
			}
		}
	}
}

func reasonText(reason string) string {
	switch reason {
	case ReasonGone:
		return "the receiver answered 410 Gone"
	case ReasonFailures:
		return fmt.Sprintf("%d deliveries in a row failed", MaxConsecutiveFailures)
	}
	return reason
}

// disabledMessage renders the notification.
func disabledMessage(from, to string, e Endpoint, reason string, now time.Time) ([]byte, error) {
	if !mailer.ValidAddress(from) || !mailer.ValidAddress(to) {
		return nil, fmt.Errorf("webhook: bad address")
	}
	var b bytes.Buffer
	subject := mime.QEncoding.Encode("utf-8", fmt.Sprintf("Hello PBX: webhook endpoint %q was switched off", e.Name))
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n", from, to, subject, now.Format(time.RFC1123Z))
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&b, "The webhook endpoint %q (id %d) was switched off automatically: %s.\r\n\r\n", e.Name, e.ID, reasonText(reason))
	b.WriteString("Events are not queued for it while it is off. Fix the receiver, then enable the endpoint again under Integrations > Webhooks; dead letters can be redelivered there.\r\n")
	return b.Bytes(), nil
}
