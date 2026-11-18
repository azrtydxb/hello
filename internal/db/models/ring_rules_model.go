// momo:fromdb sha256=3387bdce6329316afc2c207b19fe59d58577d425006fd764332c88102e5ad479
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// RingRule models the table "ring_rules".
type RingRule struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	Position  int32
	Name      string                      `momo:"default:''::text,dbdefault"`
	Match     types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	Tag       string
	CreatedAt time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt time.Time `momo:"default:now(),dbdefault"`

	RingTag *RingTag `momo:"belongs_to,fk:Tag,on_delete:restrict,on_update:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *RingRule) MomoConfig(c *momo.Config) {
	c.Check("ring_rules_name_check", "(length(name) <= 64)")
	c.Check("ring_rules_position_check", "(\"position\" >= 1)")
	c.Index("ring_rules_position", &m.Position, &m.ID)
}
