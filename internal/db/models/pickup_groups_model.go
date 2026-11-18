// momo:fromdb sha256=5328993ade608b42580671b6d5d95ff4234308aed929f24564ef3084e0f68a2c
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// PickupGroup models the table "pickup_groups".
type PickupGroup struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	Name      string
	CreatedAt time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *PickupGroup) MomoConfig(c *momo.Config) {
	c.Unique("pickup_groups_name_key", &m.Name)
	c.Check("pickup_groups_name_check", "(name ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
}
