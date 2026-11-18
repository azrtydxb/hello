// momo:fromdb sha256=5b9215b5e170e17832204c13c470146cd3870dd9cf329dda6ce871dc166d8e75
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Schedule models the table "schedules".
type Schedule struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	Name      string
	TimeZone  string
	CreatedAt time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Schedule) MomoConfig(c *momo.Config) {
	c.Unique("schedules_name_key", &m.Name)
	c.Check("schedules_name_check", "(name ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
	c.Check("schedules_time_zone_check", "(time_zone ~ '^[A-Za-z0-9_+/-]{1,64}$'::text)")
}
