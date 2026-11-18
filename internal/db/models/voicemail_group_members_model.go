// momo:fromdb sha256=7b227a54043d1707f330e24a7caa20e71ad120ea04fad120ba37105b3729fecd
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// VoicemailGroupMember models the table "voicemail_group_members".
type VoicemailGroupMember struct {
	momo.Model
	GroupID     int64 `momo:"pk"`
	ExtensionID int64 `momo:"pk,index:voicemail_group_members_extension"`

	Extension *Extension      `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
	Group     *VoicemailGroup `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
}
