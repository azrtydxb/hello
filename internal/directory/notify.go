package directory

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"mime"
	"time"

	"github.com/azrtydxb/hello/internal/mailer"
)

// MailNotifier returns an Engine.Notify that e-mails the administrators when a
// connector has failed three runs in a row. It does nothing while SMTP is not
// configured. The message names the connector and the classified code, never
// a server name, a bind DN or any server text (spec Failure modes).
func MailNotifier(
	admins func(ctx context.Context) ([]string, error),
	settings func(ctx context.Context) (mailer.Settings, bool, error),
	deliver func(ctx context.Context, s mailer.Settings, to string, msg []byte) error,
	log *slog.Logger,
) func(ctx context.Context, c Connector, code string) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if deliver == nil {
		deliver = mailer.Deliver
	}
	return func(ctx context.Context, c Connector, code string) {
		s, ok, err := settings(ctx)
		if err != nil || !ok {
			return
		}
		to, err := admins(ctx)
		if err != nil {
			log.Warn("directory: finding the administrators to e-mail", "error", err)
			return
		}
		for _, addr := range to {
			msg, err := failureMessage(s.From, addr, c, code, time.Now())
			if err != nil {
				continue
			}
			if err := deliver(ctx, s, addr, msg); err != nil {
				log.Warn("directory: e-mailing an administrator", "error", err)
			}
		}
	}
}

func failureMessage(from, to string, c Connector, code string, now time.Time) ([]byte, error) {
	if !mailer.ValidAddress(from) || !mailer.ValidAddress(to) {
		return nil, fmt.Errorf("directory: bad address")
	}
	var b bytes.Buffer
	subject := mime.QEncoding.Encode("utf-8", fmt.Sprintf("Hello PBX: directory sync %q is failing", c.Name))
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n", from, to, subject, now.Format(time.RFC1123Z))
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&b, "The directory connector %q (id %d) has failed its last 3 runs (%s).\r\n\r\n", c.Name, c.ID, code)
	b.WriteString("Nothing was disabled or changed. Open Integrations > Directory, run Test connection and fix the cause; the next successful run resets this.\r\n")
	return b.Bytes(), nil
}
