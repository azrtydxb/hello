package store

// Call park lots (phone-feature-parity spec S-6). A lot is a range of slot
// numbers; hello-sip parks a call in a free slot and a phone retrieves it by
// dialling the retrieve code and the slot, or the slot itself. Every change
// runs through configChange, so it is audited, bumps the configuration
// revision and notifies hello-sip.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// MaxParkLots and MaxParkSlots bound a configuration (a slot is a number a
// BLF key can watch, so the totals stay small).
const (
	MaxParkLots     = 10
	MaxParkLotSlots = 100
)

// ParkLot is a range of park slots. ReturnSecs is how long a parked call
// waits before it rings the parker again; VoicemailSecs how long it then
// rings before it goes to the parker's voicemail (0: hang up instead).
type ParkLot struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	SlotStart     int       `json:"slotStart"`
	SlotEnd       int       `json:"slotEnd"`
	ReturnSecs    int       `json:"returnSecs"`
	VoicemailSecs int       `json:"voicemailSecs"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ParkLotInput is the writable shape of a lot; an update replaces it whole.
type ParkLotInput struct {
	Name          string
	SlotStart     int
	SlotEnd       int
	ReturnSecs    int
	VoicemailSecs int
	Enabled       bool
}

const parkLotCols = `id, name, slot_start, slot_end, return_secs, voicemail_secs, enabled, created_at, updated_at`

func scanParkLot(r interface{ Scan(...any) error }) (ParkLot, error) {
	var l ParkLot
	err := r.Scan(&l.ID, &l.Name, &l.SlotStart, &l.SlotEnd, &l.ReturnSecs, &l.VoicemailSecs, &l.Enabled, &l.CreatedAt, &l.UpdatedAt)
	return l, err
}

// ListParkLots returns every lot in slot order.
func (s *Store) ListParkLots(ctx context.Context) ([]ParkLot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+parkLotCols+` FROM park_lots ORDER BY slot_start, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ParkLot{}
	for rows.Next() {
		l, err := scanParkLot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetParkLot returns one lot.
func (s *Store) GetParkLot(ctx context.Context, id int64) (ParkLot, error) {
	l, err := scanParkLot(s.db.QueryRowContext(ctx, `SELECT `+parkLotCols+` FROM park_lots WHERE id = $1`, id))
	return l, mapErr(err)
}

// CreateParkLot inserts a lot. A range that overlaps another lot, an
// extension number or a ring group name is a validation error.
func (s *Store) CreateParkLot(ctx context.Context, actor string, in ParkLotInput, check Check) (ParkLot, error) {
	var l ParkLot
	err := s.configChange(ctx, actor, "create", "park_lot", check, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM park_lots`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxParkLots {
			return 0, &InUseError{Msg: fmt.Sprintf("at most %d park lots", MaxParkLots)}
		}
		err := dialGuard(ctx, tx, "slotStart", func() error {
			var err error
			l, err = scanParkLot(tx.QueryRowContext(ctx, `INSERT INTO park_lots
				(name, slot_start, slot_end, return_secs, voicemail_secs, enabled)
				VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+parkLotCols,
				in.Name, in.SlotStart, in.SlotEnd, in.ReturnSecs, in.VoicemailSecs, in.Enabled))
			return err
		})
		return l.ID, err
	})
	return l, err
}

// UpdateParkLot replaces a lot. Parked calls keep the slot they hold even if
// the range shrinks past it, until they are retrieved or time out.
func (s *Store) UpdateParkLot(ctx context.Context, actor string, id int64, in ParkLotInput, check Check) (ParkLot, error) {
	var l ParkLot
	err := s.configChange(ctx, actor, "update", "park_lot", check, func(tx *sql.Tx) (int64, error) {
		err := dialGuard(ctx, tx, "slotStart", func() error {
			var err error
			l, err = scanParkLot(tx.QueryRowContext(ctx, `UPDATE park_lots SET name = $2, slot_start = $3, slot_end = $4,
				return_secs = $5, voicemail_secs = $6, enabled = $7, updated_at = now()
				WHERE id = $1 RETURNING `+parkLotCols,
				id, in.Name, in.SlotStart, in.SlotEnd, in.ReturnSecs, in.VoicemailSecs, in.Enabled))
			return err
		})
		return id, err
	})
	return l, err
}

// DeleteParkLot removes a lot.
func (s *Store) DeleteParkLot(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "park_lot", check, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM park_lots WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// checkExtensionNotSlot refuses an extension number that a park lot's slots
// already use (a slot is dialled like an extension).
func checkExtensionNotSlot(ctx context.Context, tx *sql.Tx, number string) error {
	return checkNotSlot(ctx, tx, "number", number)
}

// checkNotSlot refuses a dialled value (field names it in the error) that a
// park lot's slots already use.
func checkNotSlot(ctx context.Context, tx *sql.Tx, field, number string) error {
	n, err := strconv.Atoi(number)
	if err != nil || strconv.Itoa(n) != number {
		return nil // not a plain number: no slot is called that
	}
	var name string
	err = tx.QueryRowContext(ctx, `SELECT name FROM park_lots WHERE $1 BETWEEN slot_start AND slot_end LIMIT 1`, n).Scan(&name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return fieldError(field, fmt.Sprintf("is a slot of park lot %s", name))
}
