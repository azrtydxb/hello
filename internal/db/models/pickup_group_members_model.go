// momo:fromdb sha256=3e43243f5a09cfceafc637e11e06aa28d055b6f9fd8e59edfbd31e364c40aa40
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// PickupGroupMember models the table "pickup_group_members".
type PickupGroupMember struct {
	momo.Model
	GroupID     int64 `momo:"pk"`
	ExtensionID int64 `momo:"pk,index:pickup_group_members_ext"`

	Extension *Extension   `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
	Group     *PickupGroup `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
}
