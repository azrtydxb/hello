package store

// Check-sync pushes (phone-feature-parity S-5): the queue between
// hello-control (which inserts requests) and hello-sip (which claims them,
// sends the NOTIFY and writes the result).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// PushChannel is the PostgreSQL channel a queued push notifies; SIP nodes
// LISTEN on it to send at once instead of waiting for their poll.
const PushChannel = "hello_checksync"

// ErrPushUnbound is returned for a push to a phone with no device, or one
// that is disabled.
var ErrPushUnbound = errors.New("store: phone has no enabled device to push to")

// Push kinds, sources and statuses.
const (
	PushResync = "resync"
	PushReboot = "reboot"

	PushManual = "manual"
	PushAuto   = "auto"

	PushPending       = "pending"
	PushSending       = "sending"
	PushOK            = "ok"
	PushNotRegistered = "not_registered"
	PushTimeout       = "timeout"
	PushAuthFailed    = "auth_failed"
	PushFailed        = "failed"
)

// pushStaleAfter is how long a claimed push may stay "sending" before it
// is written off (its node died).
const pushStaleAfter = 2 * time.Minute

// PhonePush is one resync or reboot request and its result.
type PhonePush struct {
	ID          int64      `json:"id"`
	PhoneID     int64      `json:"phoneId"`
	Kind        string     `json:"kind"`
	Source      string     `json:"source"`
	Actor       string     `json:"actor"`
	Status      string     `json:"status"`
	Detail      string     `json:"detail"`
	Contacts    int        `json:"contacts"`
	RequestedAt time.Time  `json:"requestedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

const pushCols = `id, phone_id, kind, source, actor, status, detail, contacts, requested_at, finished_at`

func scanPush(r interface{ Scan(...any) error }) (PhonePush, error) {
	var (
		p  PhonePush
		fa sql.NullTime
	)
	if err := r.Scan(&p.ID, &p.PhoneID, &p.Kind, &p.Source, &p.Actor, &p.Status, &p.Detail, &p.Contacts, &p.RequestedAt, &fa); err != nil {
		return p, err
	}
	if fa.Valid {
		p.FinishedAt = &fa.Time
	}
	return p, nil
}

// queuePush inserts one pending push for a bound, enabled phone and
// notifies the SIP nodes. An open push of the same kind for the phone is
// returned instead of adding another. ok is false for a phone that cannot
// be pushed (unbound or disabled).
func queuePush(ctx context.Context, tx *sql.Tx, phoneID int64, kind, source, actor string) (p PhonePush, ok bool, err error) {
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT device_id IS NOT NULL AND enabled FROM phones WHERE id = $1 FOR UPDATE`, phoneID).
		Scan(&eligible); err != nil {
		return p, false, err
	}
	if !eligible {
		return p, false, nil
	}
	p, err = scanPush(tx.QueryRowContext(ctx, `SELECT `+pushCols+` FROM phone_pushes
		WHERE phone_id = $1 AND kind = $2 AND status = 'pending' ORDER BY id LIMIT 1`, phoneID, kind))
	switch {
	case err == nil:
		return p, true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return p, false, err
	}
	p, err = scanPush(tx.QueryRowContext(ctx, `
		INSERT INTO phone_pushes (phone_id, kind, source, actor) VALUES ($1, $2, $3, $4)
		RETURNING `+pushCols, phoneID, kind, source, actor))
	if err != nil {
		return p, false, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_notify($1, $2::text)`, PushChannel, strconv.FormatInt(p.ID, 10)); err != nil {
		return p, false, err
	}
	return p, true, nil
}

// QueuePush queues a resync or reboot of one phone and audits it. It
// returns ErrPushUnbound for a phone with no device or one that is disabled.
func (s *Store) QueuePush(ctx context.Context, actor string, phoneID int64, kind string) (PhonePush, error) {
	var out PhonePush
	err := s.tx(ctx, func(tx *sql.Tx) error {
		p, ok, err := queuePush(ctx, tx, phoneID, kind, PushManual, actor)
		if err != nil {
			return err
		}
		if !ok {
			return ErrPushUnbound
		}
		out = p
		return insertAudit(ctx, tx, actor, kind, "phone", strconv.FormatInt(phoneID, 10))
	})
	return out, err
}

// QueuePushGroup queues the push for every bound, enabled phone of a phone
// group, or of all phones when groupID is 0, and audits it once. Phones
// that cannot be pushed are counted as skipped.
func (s *Store) QueuePushGroup(ctx context.Context, actor string, groupID int64, kind string) (queued, skipped int, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		queued, skipped = 0, 0
		resource, rid := "phone", "all"
		q := `SELECT id FROM phones ORDER BY id`
		args := []any{}
		if groupID != 0 {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phone_groups WHERE id = $1`, groupID).Scan(&one); err != nil {
				return err
			}
			resource, rid = "phone_group", strconv.FormatInt(groupID, 10)
			q = `SELECT id FROM phones WHERE group_id = $1 ORDER BY id`
			args = append(args, groupID)
		}
		rows, err := tx.QueryContext(ctx, q, args...)
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
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			_, ok, err := queuePush(ctx, tx, id, kind, PushManual, actor)
			if err != nil {
				return err
			}
			if ok {
				queued++
			} else {
				skipped++
			}
		}
		return insertAudit(ctx, tx, actor, kind, resource, rid)
	})
	return queued, skipped, err
}

func (s *Store) pushes(ctx context.Context, q string, args ...any) ([]PhonePush, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []PhonePush{}
	for rows.Next() {
		p, err := scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPhonePushes returns a phone's most recent pushes, newest first.
func (s *Store) ListPhonePushes(ctx context.Context, phoneID int64, limit int) ([]PhonePush, error) {
	return s.pushes(ctx, `SELECT `+pushCols+` FROM phone_pushes WHERE phone_id = $1 ORDER BY id DESC LIMIT $2`, phoneID, limit)
}

// LatestPhonePushes returns the newest push of every phone that has one.
func (s *Store) LatestPhonePushes(ctx context.Context) ([]PhonePush, error) {
	return s.pushes(ctx, `SELECT DISTINCT ON (phone_id) `+pushCols+` FROM phone_pushes ORDER BY phone_id, id DESC`)
}

// PushJob is a claimed push with what the SIP node needs to send it.
type PushJob struct {
	ID      int64
	PhoneID int64
	Kind    string
	Vendor  string
	Device  string // the bound device's SIP username
}

// pushLockKey serialises claims so the per-minute rate holds across nodes.
const pushLockKey = 0x68656c6c6f707368 // "hellopsh"

// ClaimPushes claims pending pushes for node, oldest first, so that no more
// than perMinute pushes were claimed in the last minute cluster-wide. It
// also writes off pushes whose node died.
func (s *Store) ClaimPushes(ctx context.Context, node string, perMinute int) ([]PushJob, error) {
	var jobs []PushJob
	err := s.tx(ctx, func(tx *sql.Tx) error {
		jobs = nil
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(pushLockKey)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE phone_pushes SET status = 'failed', detail = 'node lost', finished_at = now()
			WHERE status = 'sending' AND claimed_at < now() - make_interval(secs => $1)`, pushStaleAfter.Seconds()); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			WITH used AS (SELECT count(*) AS n FROM phone_pushes WHERE claimed_at > now() - interval '1 minute'),
			     pick AS (SELECT id FROM phone_pushes WHERE status = 'pending' ORDER BY id
			              LIMIT greatest($2::int - (SELECT n FROM used), 0) FOR UPDATE SKIP LOCKED),
			     upd AS (UPDATE phone_pushes SET status = 'sending', claimed_at = now(), claimed_by = $1
			             WHERE id IN (SELECT id FROM pick)
			             RETURNING id, phone_id, kind)
			SELECT u.id, u.phone_id, u.kind, p.vendor, COALESCE(d.sip_username, '')
			FROM upd u JOIN phones p ON p.id = u.phone_id LEFT JOIN devices d ON d.id = p.device_id
			ORDER BY u.id`, node, perMinute)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var j PushJob
			if err := rows.Scan(&j.ID, &j.PhoneID, &j.Kind, &j.Vendor, &j.Device); err != nil {
				return err
			}
			jobs = append(jobs, j)
		}
		return rows.Err()
	})
	return jobs, err
}

// FinishPush records a claimed push's result.
func (s *Store) FinishPush(ctx context.Context, id int64, status, detail string, contacts int) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE phone_pushes SET status = $2, detail = $3, contacts = $4, finished_at = now()
		WHERE id = $1 AND status = 'sending'`, id, status, detail, contacts)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// PhoneSyncHash is a fingerprint of everything a phone's configuration
// renders from: the render data (account, server, BLF keys, book, firmware,
// time) and the template that resolves for it.
func (s *Store) PhoneSyncHash(ctx context.Context, phoneID int64) (string, error) {
	d, t, err := s.RenderInputs(ctx, phoneID)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(struct {
		D any
		T any
	}{d, t})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// AutoSyncPhones lists the bound, enabled phones with their stored sync
// hash (empty before the first scan).
func (s *Store) AutoSyncPhones(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(sync_hash, '') FROM phones WHERE device_id IS NOT NULL AND enabled`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]string{}
	for rows.Next() {
		var (
			id int64
			h  string
		)
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		out[id] = h
	}
	return out, rows.Err()
}

// AutoResync moves a phone's stored hash from old to hash and, unless old
// is empty (first sight of the phone: nothing to compare with), queues a
// resync in the same transaction. Only one control replica wins the change;
// queued is false for the others.
func (s *Store) AutoResync(ctx context.Context, phoneID int64, old, hash string) (queued bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		queued = false
		res, err := tx.ExecContext(ctx, `UPDATE phones SET sync_hash = $3 WHERE id = $1 AND COALESCE(sync_hash, '') = $2`, phoneID, old, hash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 || old == "" {
			return nil
		}
		_, ok, err := queuePush(ctx, tx, phoneID, PushResync, PushAuto, "system")
		if err != nil || !ok {
			return err
		}
		queued = true
		return insertAudit(ctx, tx, "system", "auto_resync", "phone", strconv.FormatInt(phoneID, 10))
	})
	return queued, err
}
