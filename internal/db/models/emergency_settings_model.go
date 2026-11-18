// momo:fromdb sha256=c2d97213ff5d4e0d42e9ae5507975bce8b0aa5777cd77a2e5c0d3b451b3a851b
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// EmergencySetting models the table "emergency_settings".
type EmergencySetting struct {
	momo.Model
	ID          *int16    `momo:"pk,default:1,dbdefault"`
	NotifyGroup string    `momo:"default:''::text,dbdefault"`
	UpdatedAt   time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *EmergencySetting) MomoConfig(c *momo.Config) {
	c.Check("emergency_settings_id_check", "(id = 1)")
	c.Check("emergency_settings_notify_group_check", "(notify_group ~ '^[0-9A-Za-z._-]{0,64}$'::text)")
}
