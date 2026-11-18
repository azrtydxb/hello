// momo:fromdb sha256=14b5e8e268e905837c5d9352c8a8c415deecb2e5f681ff8b77b382e550b597af
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Calendar models the table "calendars".
type Calendar struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	Name        string
	Description string    `momo:"default:''::text,dbdefault"`
	CreatedAt   time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt   time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Calendar) MomoConfig(c *momo.Config) {
	c.Unique("calendars_name_key", &m.Name)
	c.Check("calendars_name_check", "(name ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
}
