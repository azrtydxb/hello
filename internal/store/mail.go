package store

// System SMTP settings and the voicemail email queue. The SMTP password is
// sealed with HELLO_SECRET_KEY like a trunk password; the only way to read it
// back is SMTPSendSettings, which hello-control's mailer uses and no API
// route returns.

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// SMTPSettings are the stored SMTP settings without the password.
type SMTPSettings struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// Security is "none", "starttls" or "tls".
	Security    string    `json:"security"`
	Username    string    `json:"username"`
	From        string    `json:"from"`
	PasswordSet bool      `json:"passwordSet"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// SMTPInput is an update of the SMTP settings. A nil Password keeps the
// stored one; a pointer to "" clears it.
type SMTPInput struct {
	Host     string
	Port     int
	Security string
	Username string
	From     string
	Password *string
}

const smtpAAD = "smtp:1"

// GetSMTPSettings returns the stored settings; a deployment that never
// saved any gets the zero host (the mailer then falls back to the
// environment).
func (s *Store) GetSMTPSettings(ctx context.Context) (SMTPSettings, error) {
	var v SMTPSettings
	err := s.db.QueryRowContext(ctx, `
		SELECT host, port, security, username, from_address, password_sealed IS NOT NULL, updated_at
		FROM smtp_settings`).Scan(&v.Host, &v.Port, &v.Security, &v.Username, &v.From, &v.PasswordSet, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SMTPSettings{Port: 587, Security: "starttls"}, nil
	}
	return v, err
}

// SMTPSendSettings is the stored settings with the password in clear, for
// the sender only.
type SMTPSendSettings struct {
	SMTPSettings
	Password string
}

// SMTPSendSettings returns the settings the mailer sends with.
func (s *Store) SMTPSendSettings(ctx context.Context) (SMTPSendSettings, error) {
	v, err := s.GetSMTPSettings(ctx)
	if err != nil || !v.PasswordSet {
		return SMTPSendSettings{SMTPSettings: v}, err
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT password_sealed FROM smtp_settings`).Scan(&sealed); err != nil {
		return SMTPSendSettings{}, err
	}
	if s.box == nil {
		return SMTPSendSettings{}, errors.New("store: no secret box to open the SMTP password")
	}
	pw, err := s.box.Open(sealed, smtpAAD)
	return SMTPSendSettings{SMTPSettings: v, Password: pw}, err
}

// PutSMTPSettings saves the settings and audits the change (never the
// password).
func (s *Store) PutSMTPSettings(ctx context.Context, actor string, in SMTPInput) (SMTPSettings, error) {
	var sealed []byte
	clear := false
	if in.Password != nil {
		if *in.Password == "" {
			clear = true
		} else {
			if s.box == nil {
				return SMTPSettings{}, errors.New("store: no secret box to seal the SMTP password")
			}
			b, err := s.box.Seal(*in.Password, smtpAAD)
			if err != nil {
				return SMTPSettings{}, err
			}
			sealed = b
		}
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO smtp_settings (id, host, port, security, username, from_address, password_sealed)
			VALUES (TRUE, $1, $2, $3, $4, $5, $6)
			ON CONFLICT (id) DO UPDATE SET host = $1, port = $2, security = $3, username = $4, from_address = $5,
				password_sealed = CASE WHEN $7::boolean THEN NULL WHEN $6::bytea IS NOT NULL THEN $6::bytea ELSE smtp_settings.password_sealed END,
				updated_at = now()`,
			in.Host, in.Port, in.Security, in.Username, in.From, sealed, clear); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "smtp_settings", "1")
	})
	if err != nil {
		return SMTPSettings{}, err
	}
	return s.GetSMTPSettings(ctx)
}

// EmailJob is one voicemail notification claimed for sending.
type EmailJob struct {
	ID          int64
	MessageID   int64
	To          string
	Attach      bool
	After       string
	Attempts    int
	Caller      string
	DurationMs  int64
	CreatedAt   time.Time
	Object      string
	QueuedSince time.Time
}

// FanOutEmails turns voicemail messages awaiting email into one notification
// per recipient: every enabled user linked to the box's extension who has an
// email address and notifications on, plus the box's own address. A message
// with no recipient is marked skipped. Rows are taken with SKIP LOCKED, so
// concurrent nodes split the work and no message is fanned out twice.
func (s *Store) FanOutEmails(ctx context.Context, limit int) (int, error) {
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT m.id FROM voicemail_messages m WHERE m.email_status = 'pending'
			ORDER BY m.id LIMIT $1 FOR UPDATE OF m SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil || len(ids) == 0 {
			return err
		}
		// Users first, so their own options win over the box address.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO voicemail_notifications (message_id, user_id, to_email, attach, after_action)
			SELECT m.id, u.id, u.email, u.vm_email_attach, u.vm_email_after
			FROM voicemail_messages m
			JOIN voicemail_boxes b ON b.id = m.box_id
			JOIN user_extensions ue ON ue.extension_id = b.extension_id
			JOIN users u ON u.id = ue.user_id
			WHERE m.id = ANY($1::bigint[]) AND u.enabled AND u.email <> '' AND u.vm_email_notify
			ON CONFLICT DO NOTHING`, ids); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO voicemail_notifications (message_id, to_email)
			SELECT m.id, b.email FROM voicemail_messages m JOIN voicemail_boxes b ON b.id = m.box_id
			WHERE m.id = ANY($1::bigint[]) AND b.email <> ''
			ON CONFLICT DO NOTHING`, ids); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE voicemail_messages m SET email_status = CASE
				WHEN EXISTS (SELECT 1 FROM voicemail_notifications n WHERE n.message_id = m.id) THEN 'queued'
				ELSE 'skipped' END
			WHERE m.id = ANY($1::bigint[])`, ids)
		n = len(ids)
		return err
	})
	return n, err
}

// ClaimEmails takes up to limit notifications that are due (or whose sender
// went away: claimed longer than stale ago) for this node. The claim is one
// UPDATE over rows locked with SKIP LOCKED, so one node holds each
// notification at a time.
func (s *Store) ClaimEmails(ctx context.Context, limit int, stale time.Duration) ([]EmailJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH c AS (
			UPDATE voicemail_notifications n SET status = 'sending', claimed_at = now(), attempts = n.attempts + 1
			WHERE n.id IN (
				SELECT id FROM voicemail_notifications
				WHERE (status = 'pending' AND next_attempt_at <= now())
				   OR (status = 'sending' AND claimed_at < now() - make_interval(secs => $2))
				ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED)
			RETURNING n.id, n.message_id, n.to_email, n.attach, n.after_action, n.attempts, n.created_at)
		SELECT c.id, c.message_id, c.to_email, c.attach, c.after_action, c.attempts, c.created_at,
			m.caller, m.duration_ms, m.created_at, m.minio_object
		FROM c JOIN voicemail_messages m ON m.id = c.message_id ORDER BY c.id`, limit, stale.Seconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []EmailJob{}
	for rows.Next() {
		var j EmailJob
		if err := rows.Scan(&j.ID, &j.MessageID, &j.To, &j.Attach, &j.After, &j.Attempts, &j.QueuedSince,
			&j.Caller, &j.DurationMs, &j.CreatedAt, &j.Object); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// EmailResult is how one send ended.
type EmailResult struct {
	// Err is the failure text ("" for success); it must not contain
	// credentials.
	Err string
	// Final gives up: no further attempt.
	Final bool
	// RetryAt is when the next attempt is due after a non-final failure.
	RetryAt time.Time
}

// FinishEmail records the result of sending notification id. When it was
// the last open notification of its message, the message's email status is
// settled and the after-send action of the recipients applies: delete wins
// over mark-as-read. It returns the audio object to remove when the message
// was deleted ("" otherwise).
func (s *Store) FinishEmail(ctx context.Context, id int64, r EmailResult) (removeObject string, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var msg int64
		switch {
		case r.Err == "":
			err = tx.QueryRowContext(ctx, `UPDATE voicemail_notifications SET status = 'sent', last_error = '' WHERE id = $1 RETURNING message_id`, id).Scan(&msg)
		case r.Final:
			err = tx.QueryRowContext(ctx, `UPDATE voicemail_notifications SET status = 'failed', last_error = $2 WHERE id = $1 RETURNING message_id`,
				id, truncate(r.Err, 500)).Scan(&msg)
		default:
			err = tx.QueryRowContext(ctx, `UPDATE voicemail_notifications SET status = 'pending', next_attempt_at = $3, last_error = $2 WHERE id = $1 RETURNING message_id`,
				id, truncate(r.Err, 500), r.RetryAt).Scan(&msg)
			if err == nil {
				return nil
			}
		}
		if err != nil {
			return mapErr(err)
		}
		return settleMessage(ctx, tx, msg, &removeObject)
	})
	return removeObject, err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func settleMessage(ctx context.Context, tx *sql.Tx, msg int64, remove *string) error {
	var open, failed, del, read int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE status IN ('pending', 'sending')),
			count(*) FILTER (WHERE status = 'failed'),
			count(*) FILTER (WHERE status = 'sent' AND after_action = 'delete'),
			count(*) FILTER (WHERE status = 'sent' AND after_action = 'mark_read')
		FROM voicemail_notifications WHERE message_id = $1`, msg).Scan(&open, &failed, &del, &read); err != nil {
		return err
	}
	if open > 0 {
		return nil
	}
	status := "sent"
	if failed > 0 {
		status = "failed"
	}
	var box int64
	var object string
	err := tx.QueryRowContext(ctx, `UPDATE voicemail_messages SET email_status = $2 WHERE id = $1 RETURNING box_id, minio_object`, msg, status).
		Scan(&box, &object)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case del > 0:
		if _, err := tx.ExecContext(ctx, `DELETE FROM voicemail_messages WHERE id = $1`, msg); err != nil {
			return err
		}
		*remove = object
		if err := insertAudit(ctx, tx, "system:mailer", "delete-after-email", "voicemail_message", itoa(msg)); err != nil {
			return err
		}
		return notifyMailbox(ctx, tx, box)
	case read > 0:
		if _, err := tx.ExecContext(ctx, `UPDATE voicemail_messages SET heard = TRUE WHERE id = $1`, msg); err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, "system:mailer", "mark-read-after-email", "voicemail_message", itoa(msg)); err != nil {
			return err
		}
		return notifyMailbox(ctx, tx, box)
	}
	return nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
