// momo:fromdb sha256=b8d663d17df76ab4857798f29dcda6696dee746cbdeb91db91be83fdf874c7d3
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// WifiAssignment models the table "wifi_assignments".
type WifiAssignment struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	ProfileID int64 `momo:"index:wifi_assignments_profile"`
	GroupID   *int64
	PhoneID   *int64

	Group   *PhoneGroup  `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
	Phone   *Phone       `momo:"belongs_to,fk:PhoneID,on_delete:cascade"`
	Profile *WifiProfile `momo:"belongs_to,fk:ProfileID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *WifiAssignment) MomoConfig(c *momo.Config) {
	c.Check("wifi_assignments_check", "((group_id IS NULL) <> (phone_id IS NULL))")
	c.Unique("wifi_assignments_group", &m.GroupID, &m.ProfileID).Where("(group_id IS NOT NULL)")
	c.Unique("wifi_assignments_phone", &m.PhoneID, &m.ProfileID).Where("(phone_id IS NOT NULL)")
}
