package store

import (
	"context"
	"database/sql"
)

// SetCalendarDND is the calendar poller's way to set DND (spec integrations
// I-56). Turning it on sets DND with the source "calendar" on the user's
// extensions that have DND off; an extension whose DND is already on (set by
// the user) is left alone, so a manual DND is never overridden. Turning it off
// clears DND only where the calendar set it. It changes the extension rows,
// audits and bumps the revision hello-sip reloads on, in one transaction, like
// every configuration change, and does nothing (no revision bump) when no
// extension would change. changed is the number of extensions changed.
func (s *Store) SetCalendarDND(ctx context.Context, userID int64, on bool) (int, error) {
	const (
		onSQL = `UPDATE extensions e SET dnd = TRUE, dnd_source = 'calendar', updated_at = now()
			FROM user_extensions ue WHERE ue.extension_id = e.id AND ue.user_id = $1 AND e.kind = 'user' AND NOT e.dnd`
		offSQL = `UPDATE extensions e SET dnd = FALSE, dnd_source = 'manual', updated_at = now()
			FROM user_extensions ue WHERE ue.extension_id = e.id AND ue.user_id = $1 AND e.dnd AND e.dnd_source = 'calendar'`
		wouldOn  = `SELECT count(*) FROM extensions e JOIN user_extensions ue ON ue.extension_id = e.id WHERE ue.user_id = $1 AND e.kind = 'user' AND NOT e.dnd`
		wouldOff = `SELECT count(*) FROM extensions e JOIN user_extensions ue ON ue.extension_id = e.id WHERE ue.user_id = $1 AND e.dnd AND e.dnd_source = 'calendar'`
	)
	q, upd := wouldOff, offSQL
	if on {
		q, upd = wouldOn, onSQL
	}
	var n int
	if err := s.q(ctx).QueryRowContext(ctx, q, userID).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	changed := 0
	err := s.configChangeID(ctx, "calendar", "update", "extension", nil, func(tx *sql.Tx) (string, error) {
		res, err := tx.ExecContext(ctx, upd, userID)
		if err != nil {
			return "", err
		}
		c, _ := res.RowsAffected()
		changed = int(c)
		return "", nil
	})
	return changed, err
}
