// momo:fromdb sha256=0be8db9acc96eb148bbea50513c436c4e7910149c6bfd356a85aee933aa21e2a
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// OriginateSetting models the table "originate_settings".
type OriginateSetting struct {
	momo.Model
	ID                *bool    `momo:"pk,default:true,dbdefault"`
	Destinations      string   `momo:"default:'inherit'::text,dbdefault"`
	CountryPrefixes   []string `momo:"default:'{}'::text[],dbdefault"`
	AutoAnswerAllowed bool     `momo:"default:false,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *OriginateSetting) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Destinations,
		"inherit",
		"restricted",
	)
	c.Check("originate_settings_id_check", "id")
}
