// momo:fromdb sha256=5060cb7460167691fc6937e3afa89664a9bba493e87c3d081cb5d94667737eb2
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// OutboundRouteTrunk models the table "outbound_route_trunks".
type OutboundRouteTrunk struct {
	momo.Model
	RouteID  int64 `momo:"pk"`
	TrunkID  int64
	Position int32 `momo:"pk"`

	Route *OutboundRoute `momo:"belongs_to,fk:RouteID,on_delete:cascade"`
	Trunk *Trunk         `momo:"belongs_to,fk:TrunkID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *OutboundRouteTrunk) MomoConfig(c *momo.Config) {
	c.Unique("outbound_route_trunks_route_id_trunk_id_key", &m.RouteID, &m.TrunkID)
}
