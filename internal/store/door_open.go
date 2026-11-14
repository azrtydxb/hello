package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/azrtydxb/hello/internal/secret"
)

// Door-open code (migration 00054, spec phone-feature-parity S-47). The code
// is sealed at rest and never read back: GetDoorOpen says only whether one is
// set.

// MaxDoorCodeLen is the longest door-open code, in characters.
const MaxDoorCodeLen = 8

// DoorOpen is what the API may show of a door station's door-open settings.
type DoorOpen struct {
	CodeSet         bool `json:"codeSet"`
	HangupAfterOpen bool `json:"hangupAfterOpen"`
}

// DoorOpenChange is a change to the settings: a nil field is left alone, a
// Code that points at "" clears the code.
type DoorOpenChange struct {
	Code            *string
	HangupAfterOpen *bool
}

// ValidDoorCode reports whether code is 1 to MaxDoorCodeLen characters of
// 0-9, * and #.
func ValidDoorCode(code string) bool {
	if code == "" || len(code) > MaxDoorCodeLen {
		return false
	}
	for _, r := range code {
		if (r < '0' || r > '9') && r != '*' && r != '#' {
			return false
		}
	}
	return true
}

// GetDoorOpen returns the door-open settings of door station id, without the
// code. A station that was never configured has none set.
func (s *Store) GetDoorOpen(ctx context.Context, id int64) (DoorOpen, error) {
	var kind string
	if err := s.db.QueryRowContext(ctx, `SELECT kind FROM extensions WHERE id = $1`, id).Scan(&kind); err != nil {
		return DoorOpen{}, mapErr(err)
	}
	var d DoorOpen
	err := s.db.QueryRowContext(ctx, `SELECT code_enc IS NOT NULL, hangup_after_open FROM door_open WHERE extension_id = $1`, id).
		Scan(&d.CodeSet, &d.HangupAfterOpen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DoorOpen{}, mapErr(err)
	}
	return d, nil
}

// SetDoorOpen changes the door-open settings of door station id. Only a door
// station has them (a field error otherwise). The audit row names the
// extension and never the code.
func (s *Store) SetDoorOpen(ctx context.Context, actor string, id int64, c DoorOpenChange, check Check) (DoorOpen, error) {
	var out DoorOpen
	err := s.configChange(ctx, actor, "update", "door_open", check, func(tx *sql.Tx) (int64, error) {
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM extensions WHERE id = $1 FOR UPDATE`, id).Scan(&kind); err != nil {
			return id, err
		}
		if kind != ExtensionDoor {
			return id, fieldError("kind", "only a door station has a door-open code")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO door_open (extension_id) VALUES ($1) ON CONFLICT (extension_id) DO NOTHING`, id); err != nil {
			return id, err
		}
		if c.Code != nil {
			var sealed []byte
			if *c.Code != "" {
				if !ValidDoorCode(*c.Code) {
					return id, fieldError("code", fmt.Sprintf("must be 1-%d characters of 0-9, * and #", MaxDoorCodeLen))
				}
				if s.box == nil {
					return id, errors.New("store: no secret box configured for door-open codes")
				}
				aad := secret.DoorOpenAAD(id)
				var err error
				if sealed, err = s.box.Seal(*c.Code, aad); err != nil {
					return id, fmt.Errorf("store: seal door-open code of %d: %w", id, err)
				}
				if back, err := s.box.Open(sealed, aad); err != nil || back != *c.Code {
					return id, fmt.Errorf("store: door-open code of %d does not round-trip under HELLO_SECRET_KEY", id)
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE door_open SET code_enc = $2, updated_at = now() WHERE extension_id = $1`, id, sealed); err != nil {
				return id, err
			}
		}
		if c.HangupAfterOpen != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE door_open SET hangup_after_open = $2, updated_at = now() WHERE extension_id = $1`,
				id, *c.HangupAfterOpen); err != nil {
				return id, err
			}
		}
		return id, tx.QueryRowContext(ctx, `SELECT code_enc IS NOT NULL, hangup_after_open FROM door_open WHERE extension_id = $1`, id).
			Scan(&out.CodeSet, &out.HangupAfterOpen)
	})
	if err != nil {
		return DoorOpen{}, err
	}
	return out, nil
}
