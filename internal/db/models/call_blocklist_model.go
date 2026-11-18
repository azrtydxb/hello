// momo:fromdb sha256=62db3e5edd6298c4a054e28799ceb8bc826ab4f9921889b00e6393a7dcdbf870
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// CallBlocklist models the table "call_blocklist".
type CallBlocklist struct {
	momo.Model     `momo:"table:call_blocklist"`
	ID             int64 `momo:"pk,serial"`
	Pattern        string
	Match          string `momo:"default:'exact'::text,dbdefault"`
	Action         string `momo:"default:'block'::text,dbdefault"`
	TrunkID        *int64
	Reason         string `momo:"default:''::text,dbdefault"`
	AnnouncementID *int64
	Enabled        *bool     `momo:"default:true,dbdefault"`
	CreatedAt      time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt      time.Time `momo:"default:now(),dbdefault"`

	Announcement *Announcement `momo:"belongs_to,fk:AnnouncementID,on_delete:set_null"`
	Trunk        *Trunk        `momo:"belongs_to,fk:TrunkID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CallBlocklist) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Action,
		"allow",
		"block",
	)
	c.CheckIn(&m.Match,
		"exact",
		"prefix",
	)
	c.Check("call_blocklist_pattern_check", "((char_length(pattern) >= 1) AND (char_length(pattern) <= 40))")
	c.Check("call_blocklist_reason_check", "(char_length(reason) <= 120)")
	c.IndexExpr("call_blocklist_pattern", "pattern", "match", "coalesce(trunk_id, 0::bigint)").Unique()
	c.Index("call_blocklist_trunk", &m.TrunkID).Where("(trunk_id IS NOT NULL)")
}
