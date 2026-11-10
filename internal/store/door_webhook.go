package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/azrtydxb/hello/internal/secret"
)

// Doorbell webhooks (migration 00061, spec phone-feature-parity S-48). The
// secret is sealed, returned once on create and rotate, and read back only
// by DoorWebhookForTrigger.

// Door webhook actions.
const (
	DoorActionRing  = "ring_door"
	DoorActionChime = "chime_page"
)

// MaxDoorWebhooks bounds how many webhooks exist.
const MaxDoorWebhooks = 50

// DefaultDoorRateLimit is the default number of triggers a webhook accepts
// per minute.
const DefaultDoorRateLimit = 11

var doorWebhookNameRe = regexp.MustCompile(`^[A-Za-z0-9._ -]{1,64}$`)

// DoorWebhook is a webhook as the API shows it; never the secret.
type DoorWebhook struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Action          string     `json:"action"`
	RingGroupID     *int64     `json:"ringGroupId"`
	RingGroup       string     `json:"ringGroup"`
	PagingGroupID   *int64     `json:"pagingGroupId"`
	PagingGroup     string     `json:"pagingGroup"`
	AnnouncementID  *int64     `json:"announcementId"`
	Announcement    string     `json:"announcement"`
	RateLimit       int        `json:"rateLimit"`
	Enabled         bool       `json:"enabled"`
	LastTriggeredAt *time.Time `json:"lastTriggeredAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// DoorWebhookInput is the editable part of a webhook.
type DoorWebhookInput struct {
	Name           string
	Action         string
	RingGroupID    *int64
	PagingGroupID  *int64
	AnnouncementID *int64
	RateLimit      int
	Enabled        bool
}

// DoorTriggerHook is what the trigger endpoint needs of a webhook: the
// opened secret and the names the SIP node resolves.
type DoorTriggerHook struct {
	ID           int64
	Name         string
	Action       string
	Enabled      bool
	RateLimit    int
	Secret       string
	RingGroup    string // empty: every Door group
	PagingCode   string
	Announcement string
}

const doorWebhookCols = `w.id, w.name, w.action, w.ring_group_id, coalesce(g.name, ''), w.paging_group_id, coalesce(p.name, ''),
	w.announcement_id, coalesce(a.name, ''), w.rate_limit, w.enabled, w.last_triggered_at, w.created_at, w.updated_at`

const doorWebhookFrom = ` FROM door_webhooks w
	LEFT JOIN ring_groups g ON g.id = w.ring_group_id
	LEFT JOIN paging_groups p ON p.id = w.paging_group_id
	LEFT JOIN announcements a ON a.id = w.announcement_id`

func scanDoorWebhook(row interface{ Scan(...any) error }) (DoorWebhook, error) {
	var w DoorWebhook
	err := row.Scan(&w.ID, &w.Name, &w.Action, &w.RingGroupID, &w.RingGroup, &w.PagingGroupID, &w.PagingGroup,
		&w.AnnouncementID, &w.Announcement, &w.RateLimit, &w.Enabled, &w.LastTriggeredAt, &w.CreatedAt, &w.UpdatedAt)
	return w, err
}

// ListDoorWebhooks returns every webhook by name.
func (s *Store) ListDoorWebhooks(ctx context.Context) ([]DoorWebhook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+doorWebhookCols+doorWebhookFrom+` ORDER BY w.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []DoorWebhook{}
	for rows.Next() {
		w, err := scanDoorWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetDoorWebhook returns one webhook.
func (s *Store) GetDoorWebhook(ctx context.Context, id int64) (DoorWebhook, error) {
	w, err := scanDoorWebhook(s.db.QueryRowContext(ctx, `SELECT `+doorWebhookCols+doorWebhookFrom+` WHERE w.id = $1`, id))
	return w, mapErr(err)
}

// checkDoorWebhook validates an input against the rest of the configuration.
func checkDoorWebhook(ctx context.Context, tx *sql.Tx, in DoorWebhookInput) error {
	if !doorWebhookNameRe.MatchString(in.Name) {
		return fieldError("name", "must be 1-64 of A-Z a-z 0-9 . _ - and space")
	}
	if in.RateLimit < 1 || in.RateLimit > 600 {
		return fieldError("rateLimit", "must be 1-600 triggers a minute")
	}
	switch in.Action {
	case DoorActionRing:
		if in.PagingGroupID != nil {
			return fieldError("pagingGroupId", "only the chime page action names a paging group")
		}
		if in.RingGroupID != nil {
			var isDoor bool
			err := tx.QueryRowContext(ctx, `SELECT is_door FROM ring_groups WHERE id = $1`, *in.RingGroupID).Scan(&isDoor)
			if errors.Is(err, sql.ErrNoRows) {
				return fieldError("ringGroupId", "ring group does not exist")
			}
			if err != nil {
				return err
			}
			if !isDoor {
				return fieldError("ringGroupId", "must be a Door group")
			}
		}
	case DoorActionChime:
		if in.RingGroupID != nil {
			return fieldError("ringGroupId", "only the ring action names a ring group")
		}
		if in.PagingGroupID == nil {
			return fieldError("pagingGroupId", "the chime page action needs a paging group")
		}
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM paging_groups WHERE id = $1`, *in.PagingGroupID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return fieldError("pagingGroupId", "paging group does not exist")
		}
		if err != nil {
			return err
		}
	default:
		return fieldError("action", "must be ring_door or chime_page")
	}
	if in.AnnouncementID != nil {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM announcements WHERE id = $1`, *in.AnnouncementID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return fieldError("announcementId", "announcement does not exist")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// newDoorSecret is a fresh webhook secret: 32 random bytes in hex.
func newDoorSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("store: door webhook secret: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// sealDoorSecret seals secret for webhook id and checks that it opens again.
func (s *Store) sealDoorSecret(id int64, plain string) ([]byte, error) {
	if s.box == nil {
		return nil, errors.New("store: no secret box configured for door webhooks")
	}
	aad := secret.DoorWebhookAAD(id)
	sealed, err := s.box.Seal(plain, aad)
	if err != nil {
		return nil, fmt.Errorf("store: seal door webhook %d secret: %w", id, err)
	}
	if back, err := s.box.Open(sealed, aad); err != nil || back != plain {
		return nil, fmt.Errorf("store: door webhook %d secret does not round-trip under HELLO_SECRET_KEY", id)
	}
	return sealed, nil
}

// CreateDoorWebhook adds a webhook and returns it with its secret, which is
// shown only here.
func (s *Store) CreateDoorWebhook(ctx context.Context, actor string, in DoorWebhookInput) (DoorWebhook, string, error) {
	plain, err := newDoorSecret()
	if err != nil {
		return DoorWebhook{}, "", err
	}
	var id int64
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM door_webhooks`).Scan(&n); err != nil {
			return err
		}
		if n >= MaxDoorWebhooks {
			return &InUseError{Msg: fmt.Sprintf("at most %d door webhooks", MaxDoorWebhooks)}
		}
		if err := checkDoorWebhook(ctx, tx, in); err != nil {
			return err
		}
		// The row needs its id to seal under; a placeholder is replaced in
		// the same transaction.
		if err := tx.QueryRowContext(ctx, `INSERT INTO door_webhooks (name, secret_enc, action, ring_group_id, paging_group_id,
				announcement_id, rate_limit, enabled) VALUES ($1, '\x00', $2, $3, $4, $5, $6, $7) RETURNING id`,
			in.Name, in.Action, in.RingGroupID, in.PagingGroupID, in.AnnouncementID, in.RateLimit, in.Enabled).Scan(&id); err != nil {
			return err
		}
		sealed, err := s.sealDoorSecret(id, plain)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE door_webhooks SET secret_enc = $2 WHERE id = $1`, id, sealed); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "door_webhook", fmt.Sprint(id))
	})
	if err != nil {
		return DoorWebhook{}, "", err
	}
	w, err := s.GetDoorWebhook(ctx, id)
	return w, plain, err
}

// UpdateDoorWebhook replaces a webhook's settings; its secret stays.
func (s *Store) UpdateDoorWebhook(ctx context.Context, actor string, id int64, in DoorWebhookInput) (DoorWebhook, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM door_webhooks WHERE id = $1 FOR UPDATE`, id).Scan(&one); err != nil {
			return err
		}
		if err := checkDoorWebhook(ctx, tx, in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE door_webhooks SET name = $2, action = $3, ring_group_id = $4, paging_group_id = $5,
				announcement_id = $6, rate_limit = $7, enabled = $8, updated_at = now() WHERE id = $1`,
			id, in.Name, in.Action, in.RingGroupID, in.PagingGroupID, in.AnnouncementID, in.RateLimit, in.Enabled); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "door_webhook", fmt.Sprint(id))
	})
	if err != nil {
		return DoorWebhook{}, mapErr(err)
	}
	return s.GetDoorWebhook(ctx, id)
}

// RotateDoorWebhookSecret replaces a webhook's secret and returns the new one,
// shown only here. The old secret stops working at once.
func (s *Store) RotateDoorWebhookSecret(ctx context.Context, actor string, id int64) (string, error) {
	plain, err := newDoorSecret()
	if err != nil {
		return "", err
	}
	sealed, err := s.sealDoorSecret(id, plain)
	if err != nil {
		return "", err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE door_webhooks SET secret_enc = $2, updated_at = now() WHERE id = $1`, id, sealed)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return insertAudit(ctx, tx, actor, "rotate_secret", "door_webhook", fmt.Sprint(id))
	})
	if err != nil {
		return "", err
	}
	return plain, nil
}

// DeleteDoorWebhook removes a webhook.
func (s *Store) DeleteDoorWebhook(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM door_webhooks WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return insertAudit(ctx, tx, actor, "delete", "door_webhook", fmt.Sprint(id))
	})
}

// DoorWebhookForTrigger returns what the trigger endpoint needs of webhook id
// with its secret opened; ok is false when there is no such webhook or its
// secret does not open.
func (s *Store) DoorWebhookForTrigger(ctx context.Context, id int64) (DoorTriggerHook, bool, error) {
	var h DoorTriggerHook
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT w.id, w.name, w.action, w.enabled, w.rate_limit, w.secret_enc,
			coalesce(g.name, ''), coalesce(p.code, ''), coalesce(a.name, '')
		FROM door_webhooks w
		LEFT JOIN ring_groups g ON g.id = w.ring_group_id
		LEFT JOIN paging_groups p ON p.id = w.paging_group_id
		LEFT JOIN announcements a ON a.id = w.announcement_id
		WHERE w.id = $1`, id).
		Scan(&h.ID, &h.Name, &h.Action, &h.Enabled, &h.RateLimit, &enc, &h.RingGroup, &h.PagingCode, &h.Announcement)
	if errors.Is(err, sql.ErrNoRows) {
		return DoorTriggerHook{}, false, nil
	}
	if err != nil {
		return DoorTriggerHook{}, false, err
	}
	if s.box == nil {
		return DoorTriggerHook{}, false, nil
	}
	plain, err := s.box.Open(enc, secret.DoorWebhookAAD(id))
	if err != nil {
		return DoorTriggerHook{}, false, nil //nolint:nilerr // a secret that does not open authenticates nothing
	}
	h.Secret = plain
	return h, true, nil
}

// NoteDoorWebhookTriggered stamps the last trigger time; it is not a
// configuration change.
func (s *Store) NoteDoorWebhookTriggered(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE door_webhooks SET last_triggered_at = now() WHERE id = $1`, id)
	return err
}
