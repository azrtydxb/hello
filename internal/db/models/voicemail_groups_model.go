// momo:fromdb sha256=b318dca5e05335cd14ce7123cff996b8eb1935239b134b650ce897ac1d540ff6
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// VoicemailGroup models the table "voicemail_groups".
type VoicemailGroup struct {
	momo.Model
	ID         int64 `momo:"pk,serial"`
	Number     string
	Name       string
	Enabled    *bool     `momo:"default:true,dbdefault"`
	MaxSeconds *int32    `momo:"default:180,dbdefault"`
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt  time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *VoicemailGroup) MomoConfig(c *momo.Config) {
	c.Unique("voicemail_groups_name_key", &m.Name)
	c.Unique("voicemail_groups_number_key", &m.Number)
	c.Check("voicemail_groups_max_seconds_check", "((max_seconds >= 10) AND (max_seconds <= 600))")
	c.Check("voicemail_groups_name_check", "((char_length(name) >= 1) AND (char_length(name) <= 64))")
	c.Check("voicemail_groups_number_check", "(number ~ '^[0-9]{2,16}$'::text)")
}
