package store

// Server conference rooms (phone-feature-parity spec S-34). A room is dialled
// by its number; its PINs are sealed under HELLO_SECRET_KEY like trunk
// passwords and never leave the store in clear. Every mutation runs through
// configChange, so it is audited, bumps the configuration revision and
// notifies hello-sip.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/secret"
)

// Conference limits.
const (
	MaxConferenceRooms   = 200
	MinConferenceCap     = 2
	MaxConferenceCap     = 50
	DefaultConferenceCap = 10
)

// Entry and exit sound modes.
const (
	ConferenceSoundNone         = "none"
	ConferenceSoundTone         = "tone"
	ConferenceSoundAnnouncement = "announcement"
)

// Conference is a room as the API shows it. The PINs are write-only: only
// whether each is set is exposed.
type Conference struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	Number            string    `json:"number"`
	HasUserPIN        bool      `json:"hasUserPin"`
	HasLeaderPIN      bool      `json:"hasLeaderPin"`
	MaxParticipants   int       `json:"maxParticipants"`
	WaitForLeader     bool      `json:"waitForLeader"`
	MuteOnEntry       bool      `json:"muteOnEntry"`
	EntryExit         string    `json:"entryExit"`
	EntryAnnouncement string    `json:"entryAnnouncement"`
	ExitAnnouncement  string    `json:"exitAnnouncement"`
	Record            bool      `json:"record"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// ConferenceInput is the writable shape of a room.
type ConferenceInput struct {
	Name              string
	Number            string
	MaxParticipants   int
	WaitForLeader     bool
	MuteOnEntry       bool
	EntryExit         string
	EntryAnnouncement string
	ExitAnnouncement  string
	Record            bool
	Enabled           bool
}

// PINChange says what to do with one PIN: leave it (the zero value), set it
// to Value, or clear it (Set with an empty Value).
type PINChange struct {
	Set   bool
	Value string
}

const conferenceCols = `id, name, number, user_pin_enc IS NOT NULL, leader_pin_enc IS NOT NULL, max_participants,
	wait_for_leader, mute_on_entry, entry_exit, entry_announcement, exit_announcement, record, enabled, created_at, updated_at`

func scanConference(row interface{ Scan(...any) error }) (Conference, error) {
	var c Conference
	err := row.Scan(&c.ID, &c.Name, &c.Number, &c.HasUserPIN, &c.HasLeaderPIN, &c.MaxParticipants, &c.WaitForLeader,
		&c.MuteOnEntry, &c.EntryExit, &c.EntryAnnouncement, &c.ExitAnnouncement, &c.Record, &c.Enabled, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// ListConferences returns every room.
func (s *Store) ListConferences(ctx context.Context) ([]Conference, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+conferenceCols+` FROM conference_rooms ORDER BY number`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Conference{}
	for rows.Next() {
		c, err := scanConference(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConference returns one room.
func (s *Store) GetConference(ctx context.Context, id int64) (Conference, error) {
	c, err := scanConference(s.db.QueryRowContext(ctx, `SELECT `+conferenceCols+` FROM conference_rooms WHERE id = $1`, id))
	return c, mapErr(err)
}

// ConferenceByNumber returns the room dialled as number.
func (s *Store) ConferenceByNumber(ctx context.Context, number string) (Conference, error) {
	c, err := scanConference(s.db.QueryRowContext(ctx, `SELECT `+conferenceCols+` FROM conference_rooms WHERE number = $1`, number))
	return c, mapErr(err)
}

// checkConferenceNumber refuses a room number an extension or a ring group
// already answers to.
func checkConferenceNumber(ctx context.Context, tx *sql.Tx, number string) error {
	var taken string
	switch err := tx.QueryRowContext(ctx, `SELECT 'extension' FROM extensions WHERE number = $1
		UNION ALL SELECT 'ring group' FROM ring_groups WHERE name = $1 LIMIT 1`, number).Scan(&taken); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return fieldError("number", fmt.Sprintf("is already an %s number", taken))
}

// conferenceNumberTaken reports whether a room is dialled as number, for the
// extension and ring group creators.
func conferenceNumberTaken(ctx context.Context, tx *sql.Tx, number string) error {
	var one int
	switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM conference_rooms WHERE number = $1`, number).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	return fieldError("number", "is a conference room number")
}

// setConferencePIN seals one PIN for room id, or clears it.
func (s *Store) setConferencePIN(ctx context.Context, tx *sql.Tx, id int64, which string, ch PINChange) error {
	if !ch.Set {
		return nil
	}
	col := "user_pin_enc"
	if which == "leader" {
		col = "leader_pin_enc"
	}
	var sealed []byte
	if ch.Value != "" {
		if s.box == nil {
			return errors.New("store: no secret box configured for conference PINs")
		}
		aad := secret.ConferencePINAAD(id, which)
		var err error
		if sealed, err = s.box.Seal(ch.Value, aad); err != nil {
			return fmt.Errorf("store: seal conference %d %s PIN: %w", id, which, err)
		}
		if back, err := s.box.Open(sealed, aad); err != nil || back != ch.Value {
			return fmt.Errorf("store: conference %d %s PIN does not round-trip under HELLO_SECRET_KEY", id, which)
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE conference_rooms SET `+col+` = $2 WHERE id = $1`, id, sealed)
	return err
}

// ConferencePINs opens a room's PINs inside a change so the caller can
// reject equal PINs; "" means unset.
func (s *Store) conferencePINs(ctx context.Context, tx *sql.Tx, id int64) (user, leader string, err error) {
	var u, l []byte
	if err := tx.QueryRowContext(ctx, `SELECT user_pin_enc, leader_pin_enc FROM conference_rooms WHERE id = $1`, id).Scan(&u, &l); err != nil {
		return "", "", err
	}
	if s.box == nil {
		return "", "", nil
	}
	if len(u) > 0 {
		if user, err = s.box.Open(u, secret.ConferencePINAAD(id, "user")); err != nil {
			return "", "", nil //nolint:nilerr // an unreadable PIN cannot be compared; the caller replaces or ignores it
		}
	}
	if len(l) > 0 {
		if leader, err = s.box.Open(l, secret.ConferencePINAAD(id, "leader")); err != nil {
			return user, "", nil //nolint:nilerr // as above
		}
	}
	return user, leader, nil
}

// checkPINsDiffer refuses a room whose two PINs are equal: the leader PIN
// would be unreachable.
func (s *Store) checkPINsDiffer(ctx context.Context, tx *sql.Tx, id int64) error {
	user, leader, err := s.conferencePINs(ctx, tx, id)
	if err != nil {
		return err
	}
	if user != "" && user == leader {
		return fieldError("leaderPin", "must differ from the user PIN")
	}
	return nil
}

// CreateConference inserts a room and seals its PINs with the new id in the
// same transaction.
func (s *Store) CreateConference(ctx context.Context, actor string, in ConferenceInput, userPIN, leaderPIN PINChange, check Check) (Conference, error) {
	var c Conference
	err := s.configChange(ctx, actor, "create", "conference_room", check, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conference_rooms`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxConferenceRooms {
			return 0, &InUseError{Msg: fmt.Sprintf("at most %d conference rooms", MaxConferenceRooms)}
		}
		if err := checkConferenceNumber(ctx, tx, in.Number); err != nil {
			return 0, err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO conference_rooms (name, number, max_participants, wait_for_leader,
				mute_on_entry, entry_exit, entry_announcement, exit_announcement, record, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			in.Name, in.Number, in.MaxParticipants, in.WaitForLeader, in.MuteOnEntry, in.EntryExit,
			in.EntryAnnouncement, in.ExitAnnouncement, in.Record, in.Enabled).Scan(&id); err != nil {
			return 0, err
		}
		if err := s.setConferencePIN(ctx, tx, id, "user", userPIN); err != nil {
			return id, err
		}
		if err := s.setConferencePIN(ctx, tx, id, "leader", leaderPIN); err != nil {
			return id, err
		}
		if err := s.checkPINsDiffer(ctx, tx, id); err != nil {
			return id, err
		}
		var err error
		c, err = scanConference(tx.QueryRowContext(ctx, `SELECT `+conferenceCols+` FROM conference_rooms WHERE id = $1`, id))
		return id, err
	})
	return c, err
}

// UpdateConference replaces a room's fields; a PIN with Set false is kept.
// Renumbering a room an inbound route targets is an *InUseError naming the
// routes.
func (s *Store) UpdateConference(ctx context.Context, actor string, id int64, in ConferenceInput, userPIN, leaderPIN PINChange, check Check) (Conference, error) {
	var c Conference
	err := s.configChange(ctx, actor, "update", "conference_room", check, func(tx *sql.Tx) (int64, error) {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT number FROM conference_rooms WHERE id = $1 FOR UPDATE`, id).Scan(&old); err != nil {
			return id, mapErr(err)
		}
		if old != in.Number {
			if err := checkConferenceNumber(ctx, tx, in.Number); err != nil {
				return id, err
			}
			routes, err := routeNames(ctx, tx, `SELECT name FROM inbound_routes WHERE destination_kind = 'conference' AND destination = $1`, old)
			if err != nil {
				return id, err
			}
			if len(routes) > 0 {
				return id, inUse("conference room "+strconv.Quote(old), routes)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE conference_rooms SET name = $2, number = $3, max_participants = $4,
				wait_for_leader = $5, mute_on_entry = $6, entry_exit = $7, entry_announcement = $8, exit_announcement = $9,
				record = $10, enabled = $11, updated_at = now() WHERE id = $1`,
			id, in.Name, in.Number, in.MaxParticipants, in.WaitForLeader, in.MuteOnEntry, in.EntryExit,
			in.EntryAnnouncement, in.ExitAnnouncement, in.Record, in.Enabled); err != nil {
			return id, err
		}
		if err := s.setConferencePIN(ctx, tx, id, "user", userPIN); err != nil {
			return id, err
		}
		if err := s.setConferencePIN(ctx, tx, id, "leader", leaderPIN); err != nil {
			return id, err
		}
		if err := s.checkPINsDiffer(ctx, tx, id); err != nil {
			return id, err
		}
		var err error
		c, err = scanConference(tx.QueryRowContext(ctx, `SELECT `+conferenceCols+` FROM conference_rooms WHERE id = $1`, id))
		return id, err
	})
	return c, err
}

// DeleteConference removes a room. A room an inbound route still targets is
// an *InUseError naming the routes.
func (s *Store) DeleteConference(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "conference_room", check, func(tx *sql.Tx) (int64, error) {
		var number, name string
		if err := tx.QueryRowContext(ctx, `SELECT number, name FROM conference_rooms WHERE id = $1 FOR UPDATE`, id).Scan(&number, &name); err != nil {
			return id, mapErr(err)
		}
		routes, err := routeNames(ctx, tx, `SELECT name FROM inbound_routes WHERE destination_kind = 'conference' AND destination = $1`, number)
		if err != nil {
			return id, err
		}
		if len(routes) > 0 {
			return id, inUse("conference room "+strconv.Quote(name), routes)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM conference_rooms WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}
