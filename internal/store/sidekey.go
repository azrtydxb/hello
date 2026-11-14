package store

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/azrtydxb/hello/internal/phonebook"
)

// SetPhoneGroupSideKey sets what the side key of a group's Grandstream WP
// phones does: ptt, alarm or none (phone-feature-parity S-12). A phone with
// its own side key keeps it.
func (s *Store) SetPhoneGroupSideKey(ctx context.Context, actor string, id int64, function string) (phonebook.Group, error) {
	var out phonebook.Group
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE phone_groups SET side_key = $2, updated_at = now() WHERE id = $1`, id, function)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if out, err = getPhoneGroup(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "set-side-key:"+function, "phone_group", strconv.FormatInt(id, 10))
	})
	return out, mapErr(err)
}
