// momo:fromdb sha256=ca00aedc11c84b7bb84bc4816cb6baaeeed4fb8e7861f02359fd26e2c83f11ca
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// TrunkDestination models the table "trunk_destinations".
type TrunkDestination struct {
	momo.Model
	ID       int64 `momo:"pk,serial"`
	TrunkID  int64 `momo:"index:trunk_destinations_trunk"`
	Host     string
	Port     int32  `momo:"default:0,dbdefault"`
	Priority int32  `momo:"default:0,dbdefault"`
	Weight   *int32 `momo:"default:1,dbdefault"`

	Trunk *Trunk `momo:"belongs_to,fk:TrunkID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *TrunkDestination) MomoConfig(c *momo.Config) {
	c.Check("trunk_destinations_port_check", "((port >= 0) AND (port <= 65535))")
	c.Check("trunk_destinations_weight_check", "(weight > 0)")
}
