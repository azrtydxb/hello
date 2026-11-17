// momo:fromdb sha256=e5355d6bc49bff9fb3936ceb26ffff48faa73cc82c37a68a46d7d18e4492fcc6
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// BossLink models the table "boss_links".
type BossLink struct {
	momo.Model
	ID                   int64 `momo:"pk,serial"`
	BossExtensionID      int64
	AssistantExtensionID int64     `momo:"index:boss_links_assistant"`
	RingMode             string    `momo:"default:'both'::text,dbdefault"`
	Screening            bool      `momo:"default:false,dbdefault"`
	Enabled              *bool     `momo:"default:true,dbdefault"`
	DivertOn             bool      `momo:"default:false,dbdefault"`
	OutboundAsBoss       bool      `momo:"default:false,dbdefault"`
	CreatedAt            time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt            time.Time `momo:"default:now(),dbdefault"`

	AssistantExtension *Extension `momo:"belongs_to,fk:AssistantExtensionID,on_delete:cascade"`
	BossExtension      *Extension `momo:"belongs_to,fk:BossExtensionID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *BossLink) MomoConfig(c *momo.Config) {
	c.Unique("boss_links_boss_extension_id_key", &m.BossExtensionID)
	c.Check("boss_links_check", "(boss_extension_id <> assistant_extension_id)")
	c.CheckIn(&m.RingMode,
		"assistant_first",
		"both",
	)
}
