// momo:fromdb sha256=6420b5ec1b2a39e6d1e54f45d9168ed207919ef02df51862652498bd90463c9f
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// ScimGroupMember models the table "scim_group_members".
type ScimGroupMember struct {
	momo.Model
	GroupID  string `momo:"pk"`
	MemberID string `momo:"pk,index:scim_group_members_member"`

	Group  *ScimResource `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
	Member *ScimResource `momo:"belongs_to,fk:MemberID,on_delete:cascade"`
}
