// momo:fromdb sha256=df670702e211680aa328b0cb5d30e122fb4d827a320c57c1a0aa6b2e9cd844df
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// MessageSetting models the table "message_settings".
type MessageSetting struct {
	momo.Model
	ID                    *bool  `momo:"pk,default:true,dbdefault"`
	Enabled               *bool  `momo:"default:true,dbdefault"`
	TTLHours              *int32 `momo:"default:24,dbdefault"`
	RetentionDays         *int32 `momo:"default:30,dbdefault"`
	RatePerMinute         *int32 `momo:"default:30,dbdefault"`
	MaxQueuedPerExtension *int32 `momo:"default:100,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MessageSetting) MomoConfig(c *momo.Config) {
	c.Check("message_settings_id_check", "id")
	c.Check("message_settings_max_queued_per_extension_check", "((max_queued_per_extension >= 1) AND (max_queued_per_extension <= 10000))")
	c.Check("message_settings_rate_per_minute_check", "((rate_per_minute >= 1) AND (rate_per_minute <= 1000))")
	c.Check("message_settings_retention_days_check", "((retention_days >= 1) AND (retention_days <= 3650))")
	c.Check("message_settings_ttl_hours_check", "((ttl_hours >= 1) AND (ttl_hours <= 720))")
}
