package snapshot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/azrtydxb/hello/internal/secret"
)

// Entry and exit sound modes of a room.
const (
	ConfSoundNone         = "none"
	ConfSoundTone         = "tone"
	ConfSoundAnnouncement = "announcement"
)

// Conference is a server conference room (migration 00034, spec S-34). The
// PINs are opened here, in the node's memory only, like trunk passwords: the
// call path compares them against the digits a caller keys in.
type Conference struct {
	ID     int64
	Name   string
	Number string
	// UserPIN and LeaderPIN are empty when the room has none.
	UserPIN, LeaderPIN string
	MaxParticipants    int
	WaitForLeader      bool
	MuteOnEntry        bool
	// EntryExit is none, tone or announcement; the announcement names are
	// uploaded announcements, and an empty one falls back to the tone.
	EntryExit         string
	EntryAnnouncement string
	ExitAnnouncement  string
	Record            bool
	Enabled           bool
	// Misconfigured is set when a sealed PIN did not open (a changed
	// HELLO_SECRET_KEY): the room refuses callers rather than run unguarded.
	Misconfigured bool
}

// WithConferences installs the conference rooms (tests and the watcher).
func (s *Snapshot) WithConferences(rooms []Conference) *Snapshot {
	if s.confs == nil {
		s.confs = map[string]Conference{}
	}
	for _, r := range rooms {
		s.confs[r.Number] = r
	}
	return s
}

// Conference returns the room dialled as number (exact match).
func (s *Snapshot) Conference(number string) (Conference, bool) {
	c, ok := s.confs[number]
	return c, ok
}

// Conferences lists every room.
func (s *Snapshot) Conferences() []Conference {
	out := make([]Conference, 0, len(s.confs))
	for _, c := range s.confs {
		out = append(out, c)
	}
	return out
}

// loadConferences reads the rooms and opens their PINs.
func loadConferences(ctx context.Context, db Querier, box *secret.Box, log *slog.Logger) ([]Conference, error) {
	if log == nil {
		log = slog.Default()
	}
	rows, err := db.Query(ctx, `SELECT id, name, number, user_pin_enc, leader_pin_enc, max_participants, wait_for_leader,
		mute_on_entry, entry_exit, entry_announcement, exit_announcement, record, enabled FROM conference_rooms ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: conference rooms: %w", err)
	}
	defer rows.Close()
	var out []Conference
	for rows.Next() {
		var (
			c                  Conference
			userEnc, leaderEnc []byte
		)
		if err := rows.Scan(&c.ID, &c.Name, &c.Number, &userEnc, &leaderEnc, &c.MaxParticipants, &c.WaitForLeader,
			&c.MuteOnEntry, &c.EntryExit, &c.EntryAnnouncement, &c.ExitAnnouncement, &c.Record, &c.Enabled); err != nil {
			return nil, fmt.Errorf("snapshot: scan conference room: %w", err)
		}
		for _, p := range []struct {
			enc   []byte
			which string
			dst   *string
		}{{userEnc, "user", &c.UserPIN}, {leaderEnc, "leader", &c.LeaderPIN}} {
			if len(p.enc) == 0 {
				continue
			}
			if box == nil {
				c.Misconfigured = true
				log.Error("conference PIN cannot be opened: no secret key", "room", c.Number, "pin", p.which)
				continue
			}
			pin, err := box.Open(p.enc, secret.ConferencePINAAD(c.ID, p.which))
			if err != nil {
				c.Misconfigured = true
				log.Error("conference PIN does not open", "room", c.Number, "pin", p.which)
				continue
			}
			*p.dst = pin
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
