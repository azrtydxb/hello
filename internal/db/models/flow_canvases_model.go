// momo:fromdb sha256=8bf4273f920c7033a2a452c3f140b7d128032790dd2092e8be55c3b055070e92
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// FlowCanvas models the table "flow_canvases".
type FlowCanvas struct {
	momo.Model
	ID            int64 `momo:"pk,serial"`
	Name          string
	Description   string `momo:"default:''::text,dbdefault"`
	Viewport      *types.JSON[json.RawMessage]
	LayoutVersion int64     `momo:"default:0,dbdefault"`
	CreatedBy     string    `momo:"default:''::text,dbdefault"`
	CreatedAt     time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt     time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *FlowCanvas) MomoConfig(c *momo.Config) {
	c.Unique("flow_canvases_name_key", &m.Name)
	c.Check("flow_canvases_description_check", "(char_length(description) <= 500)")
	c.Check("flow_canvases_name_check", "((char_length(name) >= 1) AND (char_length(name) <= 64))")
}
