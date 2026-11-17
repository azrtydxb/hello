// momo:fromdb sha256=dc043f27d14a428c64b7e6cc039c6299630f6e372a60871dd7d87686e36ea657
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// FlowCanvasNode models the table "flow_canvas_nodes".
type FlowCanvasNode struct {
	momo.Model
	CanvasID  int64  `momo:"pk"`
	NodeID    string `momo:"pk"`
	X         float32
	Y         float32
	Collapsed bool `momo:"default:false,dbdefault"`

	Canvas *FlowCanvas `momo:"belongs_to,fk:CanvasID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *FlowCanvasNode) MomoConfig(c *momo.Config) {
	c.Check("flow_canvas_nodes_node_id_check", "(node_id ~ '^[A-Za-z]+:.{1,200}$'::text)")
}
