package store

// Follow-me (migration 00072, pbx-gaps S-5): one profile per extension with
// up to five targets. Every change runs through configChangeID, so it is
// audited, bumps the configuration revision and reaches hello-sip as a new
// snapshot.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Follow-me strategies and target kinds.
const (
	FollowMeSimultaneous = "simultaneous"
	FollowMeSequential   = "sequential"

	FollowMeExternal  = "external"
	FollowMeExtension = "extension"
	FollowMeRingGroup = "ring_group"

	// FollowMeMaxTargets is how many targets a profile holds.
	FollowMeMaxTargets = 5
)

// FollowMeTarget is one target of a profile; its position is its index.
type FollowMeTarget struct {
	Kind         string `json:"kind"`
	Target       string `json:"target"`
	DelaySeconds int    `json:"delaySeconds"`
}

// FollowMe is the follow-me profile of an extension. An extension that has
// none stored reads as the defaults, off.
type FollowMe struct {
	ExtensionID     int64            `json:"extensionId"`
	Number          string           `json:"number"`
	Enabled         bool             `json:"enabled"`
	Strategy        string           `json:"strategy"`
	RingOwnDevices  bool             `json:"ringOwnDevices"`
	TimeoutSeconds  int              `json:"timeoutSeconds"`
	ConfirmExternal bool             `json:"confirmExternal"`
	DNDBlocks       bool             `json:"dndBlocks"`
	Targets         []FollowMeTarget `json:"targets"`
}

// DefaultFollowMe is the profile of an extension that has none.
func DefaultFollowMe() FollowMe {
	return FollowMe{
		Strategy: FollowMeSimultaneous, RingOwnDevices: true, TimeoutSeconds: 30,
		ConfirmExternal: true, DNDBlocks: true, Targets: []FollowMeTarget{},
	}
}

// GetFollowMe returns the profile of extension id, the defaults when none is
// stored, and ErrNotFound when the extension does not exist.
func (s *Store) GetFollowMe(ctx context.Context, id int64) (FollowMe, error) {
	return getFollowMe(ctx, s.db, id)
}

type fmQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getFollowMe(ctx context.Context, q fmQueryer, id int64) (FollowMe, error) {
	f := DefaultFollowMe()
	f.ExtensionID = id
	if err := q.QueryRowContext(ctx, `SELECT number FROM extensions WHERE id = $1`, id).Scan(&f.Number); err != nil {
		return FollowMe{}, mapErr(err)
	}
	err := q.QueryRowContext(ctx, `
		SELECT enabled, strategy, ring_own_devices, timeout_seconds, confirm_external, dnd_blocks
		FROM follow_me WHERE extension_id = $1`, id).
		Scan(&f.Enabled, &f.Strategy, &f.RingOwnDevices, &f.TimeoutSeconds, &f.ConfirmExternal, &f.DNDBlocks)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return FollowMe{}, err
	}
	rows, err := q.QueryContext(ctx, `
		SELECT kind, target, delay_seconds FROM follow_me_targets WHERE extension_id = $1 ORDER BY position`, id)
	if err != nil {
		return FollowMe{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var t FollowMeTarget
		if err := rows.Scan(&t.Kind, &t.Target, &t.DelaySeconds); err != nil {
			return FollowMe{}, err
		}
		f.Targets = append(f.Targets, t)
	}
	return f, rows.Err()
}

// FollowMeInput is the writable part of a profile.
type FollowMeInput struct {
	Enabled         bool
	Strategy        string
	RingOwnDevices  bool
	TimeoutSeconds  int
	ConfirmExternal bool
	DNDBlocks       bool
	Targets         []FollowMeTarget
}

// PutFollowMe replaces the profile of extension id and returns it. The store
// refuses a target that is the extension itself, an extension or ring group
// that does not exist, and a profile with an unusable shape; the API checks
// the same things first for a precise message.
func (s *Store) PutFollowMe(ctx context.Context, actor string, id int64, in FollowMeInput) (FollowMe, error) {
	err := s.configChangeID(ctx, actor, "update", "follow_me", nil, func(tx *sql.Tx) (string, error) {
		var number string
		if err := tx.QueryRowContext(ctx, `SELECT number FROM extensions WHERE id = $1`, id).Scan(&number); err != nil {
			return "", err
		}
		sid := fmt.Sprint(id)
		if err := validateFollowMe(ctx, tx, number, in); err != nil {
			return sid, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO follow_me (extension_id, enabled, strategy, ring_own_devices, timeout_seconds, confirm_external, dnd_blocks)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (extension_id) DO UPDATE SET enabled = EXCLUDED.enabled, strategy = EXCLUDED.strategy,
				ring_own_devices = EXCLUDED.ring_own_devices, timeout_seconds = EXCLUDED.timeout_seconds,
				confirm_external = EXCLUDED.confirm_external, dnd_blocks = EXCLUDED.dnd_blocks, updated_at = now()`,
			id, in.Enabled, in.Strategy, in.RingOwnDevices, in.TimeoutSeconds, in.ConfirmExternal, in.DNDBlocks); err != nil {
			return sid, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM follow_me_targets WHERE extension_id = $1`, id); err != nil {
			return sid, err
		}
		for i, t := range in.Targets {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO follow_me_targets (extension_id, position, kind, target, delay_seconds)
				VALUES ($1, $2, $3, $4, $5)`, id, i+1, t.Kind, t.Target, t.DelaySeconds); err != nil {
				return sid, err
			}
		}
		return sid, nil
	})
	if err != nil {
		return FollowMe{}, err
	}
	return s.GetFollowMe(ctx, id)
}

func validateFollowMe(ctx context.Context, tx *sql.Tx, own string, in FollowMeInput) error {
	if in.Strategy != FollowMeSimultaneous && in.Strategy != FollowMeSequential {
		return fieldError("strategy", "must be simultaneous or sequential")
	}
	if in.TimeoutSeconds < 5 || in.TimeoutSeconds > 120 {
		return fieldError("timeoutSeconds", "must be 5-120 seconds")
	}
	if len(in.Targets) > FollowMeMaxTargets {
		return fieldError("targets", fmt.Sprintf("at most %d targets", FollowMeMaxTargets))
	}
	for i, t := range in.Targets {
		path := fmt.Sprintf("targets[%d]", i)
		if t.DelaySeconds < 0 || t.DelaySeconds > 60 {
			return fieldError(path+".delaySeconds", "must be 0-60 seconds")
		}
		if t.Target == own && t.Kind != FollowMeRingGroup {
			return fieldError(path+".target", "a target cannot be the extension itself")
		}
		var q string
		switch t.Kind {
		case FollowMeExternal:
			continue
		case FollowMeExtension:
			q = `SELECT count(*) FROM extensions WHERE number = $1`
		case FollowMeRingGroup:
			q = `SELECT count(*) FROM ring_groups WHERE name = $1`
		default:
			return fieldError(path+".kind", "must be external, extension or ring_group")
		}
		var n int
		if err := tx.QueryRowContext(ctx, q, t.Target).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fieldError(path+".target", t.Kind+" "+t.Target+" does not exist")
		}
	}
	return nil
}
