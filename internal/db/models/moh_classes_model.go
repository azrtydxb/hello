// momo:fromdb sha256=02ce2d845e008b3efb8de5e3ffd314e426d20cf8e205a706a476123150dc2af8
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// MohClass models the table "moh_classes".
type MohClass struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	Name      string
	Mode      string    `momo:"default:'loop'::text,dbdefault"`
	IsDefault bool      `momo:"default:false,dbdefault"`
	CreatedAt time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MohClass) MomoConfig(c *momo.Config) {
	c.Unique("moh_classes_name_key", &m.Name)
	c.CheckIn(&m.Mode,
		"loop",
		"shuffle",
	)
	c.Check("moh_classes_name_check", "((char_length(name) >= 1) AND (char_length(name) <= 64))")
	c.IndexExpr("moh_classes_one_default", "true").Unique().Where("is_default")
}
