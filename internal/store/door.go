package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
)

// Door stations (migration 00053, spec phone-feature-parity S-45 and S-46).

// Extension kinds.
const (
	ExtensionUser = "user"
	ExtensionDoor = "door"
)

// MaxDoorTargets bounds the destinations one door station may list.
const MaxDoorTargets = 50

// DoorTargets are the extra destinations a door station may call, besides
// every Door ring group: extensions (by id) and ring groups (by id).
type DoorTargets struct {
	Extensions []int64 `json:"extensions"`
	RingGroups []int64 `json:"ringGroups"`
}

// Empty reports whether no target is listed.
func (t DoorTargets) Empty() bool { return len(t.Extensions) == 0 && len(t.RingGroups) == 0 }

// JSON is the stored form; the lists are never null.
func (t DoorTargets) JSON() []byte {
	if t.Extensions == nil {
		t.Extensions = []int64{}
	}
	if t.RingGroups == nil {
		t.RingGroups = []int64{}
	}
	b, _ := json.Marshal(t)
	return b
}

// parseDoorTargets reads the stored form; a damaged value reads as empty.
func parseDoorTargets(raw []byte) DoorTargets {
	t := DoorTargets{Extensions: []int64{}, RingGroups: []int64{}}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &t)
	}
	if t.Extensions == nil {
		t.Extensions = []int64{}
	}
	if t.RingGroups == nil {
		t.RingGroups = []int64{}
	}
	return t
}

// checkDoorTargets refuses targets that do not exist, a door station as a
// target (a station dials people, not stations), the station itself (self is
// its id, 0 on create), a duplicate, and more than MaxDoorTargets.
func checkDoorTargets(ctx context.Context, tx *sql.Tx, self int64, t DoorTargets) error {
	if len(t.Extensions)+len(t.RingGroups) > MaxDoorTargets {
		return fieldError("doorTargets", fmt.Sprintf("at most %d targets", MaxDoorTargets))
	}
	for i, id := range t.Extensions {
		path := fmt.Sprintf("doorTargets.extensions[%d]", i)
		if slices.Contains(t.Extensions[:i], id) {
			return fieldError(path, "extension is listed twice")
		}
		if id == self {
			return fieldError(path, "a door station cannot call itself")
		}
		var kind string
		err := tx.QueryRowContext(ctx, `SELECT kind FROM extensions WHERE id = $1`, id).Scan(&kind)
		if err == sql.ErrNoRows {
			return fieldError(path, "extension does not exist")
		}
		if err != nil {
			return err
		}
		if kind == ExtensionDoor {
			return fieldError(path, "a door station cannot be a target")
		}
	}
	for i, id := range t.RingGroups {
		path := fmt.Sprintf("doorTargets.ringGroups[%d]", i)
		if slices.Contains(t.RingGroups[:i], id) {
			return fieldError(path, "ring group is listed twice")
		}
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM ring_groups WHERE id = $1`, id).Scan(&one)
		if err == sql.ErrNoRows {
			return fieldError(path, "ring group does not exist")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// doorChange works out the kind and the door targets an update leaves the
// extension with, and checks them. Becoming a door station is refused while
// the extension is a member of a ring group, paging group, pickup group or
// queue (it can never ring there); it then loses voicemail and intercom and
// is hidden from the extensions phonebook unless the same change says
// otherwise. Turning back into a user drops the targets. c is the caller's
// copy of the change; the forced fields are written to it.
func doorChange(ctx context.Context, tx *sql.Tx, id int64, c *ExtensionChange) (string, DoorTargets, error) {
	var cur string
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT kind, door_targets FROM extensions WHERE id = $1 FOR UPDATE`, id).Scan(&cur, &raw); err != nil {
		return "", DoorTargets{}, err
	}
	kind, targets := cur, parseDoorTargets(raw)
	if c.Kind != nil {
		if *c.Kind != ExtensionUser && *c.Kind != ExtensionDoor {
			return "", DoorTargets{}, fieldError("kind", "must be user or door")
		}
		kind = *c.Kind
	}
	if c.DoorTargets != nil {
		targets = *c.DoorTargets
	}
	if kind == ExtensionUser {
		if c.DoorTargets != nil && !c.DoorTargets.Empty() {
			return "", DoorTargets{}, fieldError("doorTargets", "only a door station has door targets")
		}
		return kind, DoorTargets{}, nil
	}
	if c.VoicemailEnabled != nil && *c.VoicemailEnabled {
		return "", DoorTargets{}, fieldError("voicemailEnabled", "a door station has no voicemail")
	}
	if c.AllowIntercom != nil && *c.AllowIntercom {
		return "", DoorTargets{}, fieldError("allowIntercom", "a door station does not take intercom calls")
	}
	if err := checkDoorTargets(ctx, tx, id, targets); err != nil {
		return "", DoorTargets{}, err
	}
	if cur == ExtensionUser {
		if where, err := memberOf(ctx, tx, id); err != nil {
			return "", DoorTargets{}, err
		} else if where != "" {
			return "", DoorTargets{}, fieldError("kind", "extension is a member of "+where+" and cannot become a door station")
		}
		off := false
		c.VoicemailEnabled, c.AllowIntercom = &off, &off
		if c.DirectoryHidden == nil {
			on := true
			c.DirectoryHidden = &on
		}
	}
	return kind, targets, nil
}

// afterKindChange gives an extension that is a user again its voicemail box
// back (a door station has none).
func afterKindChange(ctx context.Context, tx *sql.Tx, e Extension) error {
	if e.Kind != ExtensionUser {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO voicemail_boxes (extension_id) VALUES ($1) ON CONFLICT (extension_id) DO NOTHING`, e.ID)
	return err
}

// memberOf names the first group, queue or pickup group the extension is a
// member of, or "".
func memberOf(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	for _, q := range []struct{ what, sql string }{
		{"ring group", `SELECT g.name FROM ring_group_members m JOIN ring_groups g ON g.id = m.group_id WHERE m.extension_id = $1 LIMIT 1`},
		{"paging group", `SELECT g.name FROM paging_group_members m JOIN paging_groups g ON g.id = m.group_id WHERE m.extension_id = $1 LIMIT 1`},
		{"pickup group", `SELECT g.name FROM pickup_group_members m JOIN pickup_groups g ON g.id = m.group_id WHERE m.extension_id = $1 LIMIT 1`},
		{"queue", `SELECT g.name FROM queue_members m JOIN queues g ON g.id = m.queue_id WHERE m.extension_id = $1 LIMIT 1`},
	} {
		var name string
		err := tx.QueryRowContext(ctx, q.sql, id).Scan(&name)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s %q", q.what, name), nil
	}
	return "", nil
}

// checkNoDoor refuses a door station among the extension ids a group, queue
// or pickup group lists as members; path is the field to blame.
func checkNoDoor(ctx context.Context, tx *sql.Tx, path string, ids []int64) error {
	for _, id := range ids {
		var number string
		err := tx.QueryRowContext(ctx, `SELECT number FROM extensions WHERE id = $1 AND kind = 'door'`, id).Scan(&number)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		return fieldError(path, "extension "+number+" is a door station and cannot be a member")
	}
	return nil
}
