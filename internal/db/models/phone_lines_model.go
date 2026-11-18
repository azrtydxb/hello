// momo:fromdb sha256=ccc3866a98de743a2846f91b0859ef974e439953923e36d2b372ef89062665f6
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// PhoneLine models the table "phone_lines".
type PhoneLine struct {
	momo.Model
	PhoneID   int64 `momo:"pk"`
	Line      int16 `momo:"pk"`
	DeviceID  int64
	CreatedAt time.Time `momo:"default:now(),dbdefault"`

	Device *Device `momo:"belongs_to,fk:DeviceID,on_delete:restrict"`
	Phone  *Phone  `momo:"belongs_to,fk:PhoneID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *PhoneLine) MomoConfig(c *momo.Config) {
	c.Unique("phone_lines_device_id_key", &m.DeviceID)
	c.Check("phone_lines_line_check", "((line >= 2) AND (line <= 6))")
}
