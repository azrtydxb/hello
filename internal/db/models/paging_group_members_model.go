// momo:fromdb sha256=deb6066577b72e28a86b7be82a690653e7cf46b12ca4eaa9884163485e1b6f45
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// PagingGroupMember models the table "paging_group_members".
type PagingGroupMember struct {
	momo.Model
	GroupID     int64 `momo:"pk"`
	ExtensionID int64 `momo:"pk"`
	Position    int32 `momo:"default:0,dbdefault"`

	Extension *Extension   `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
	Group     *PagingGroup `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
}
