// momo:fromdb sha256=6bf474f45402ab0d5b1266ae2fa77e2730ffc9114dde7efcef424bd2dd940445
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// PagingGroupCaller models the table "paging_group_callers".
type PagingGroupCaller struct {
	momo.Model
	GroupID     int64 `momo:"pk"`
	ExtensionID int64 `momo:"pk"`

	Extension *Extension   `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
	Group     *PagingGroup `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
}
