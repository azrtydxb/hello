// momo:fromdb sha256=a995e70c655221ca451ab95c4156856acc422ae0d3a91980b760c1efda698b0f
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// RingGroup models the table "ring_groups".
type RingGroup struct {
	momo.Model
	ID            int64 `momo:"pk,serial"`
	Name          string
	Strategy      string
	Hunt          bool      `momo:"default:false,dbdefault"`
	RingTimeout   *int32    `momo:"default:30,dbdefault"`
	MemberDelay   *int32    `momo:"default:5,dbdefault"`
	IgnoreDnd     bool      `momo:"default:false,dbdefault"`
	FailureKind   string    `momo:"default:'none'::text,dbdefault"`
	FailureTarget string    `momo:"default:''::text,dbdefault"`
	CreatedAt     time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt     time.Time `momo:"default:now(),dbdefault"`
	MohClassID    *int64
	MohRingback   bool `momo:"default:false,dbdefault"`
	RingTag       *string
	IsDoor        bool `momo:"default:false,dbdefault"`

	MohClass   *MohClass `momo:"belongs_to,fk:MohClassID,on_delete:set_null"`
	RingTagRef *RingTag  `momo:"belongs_to,fk:RingTag,on_delete:restrict,on_update:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *RingGroup) MomoConfig(c *momo.Config) {
	c.Unique("ring_groups_name_key", &m.Name)
	c.CheckIn(&m.FailureKind,
		"announcement",
		"directory",
		"extension",
		"external",
		"ivr",
		"none",
		"queue",
		"ring_group",
		"time_condition",
		"voicemail",
	)
	c.Check("ring_groups_member_delay_check", "((member_delay >= 0) AND (member_delay <= 60))")
	c.Check("ring_groups_name_check", "(name ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
	c.Check("ring_groups_ring_timeout_check", "((ring_timeout >= 5) AND (ring_timeout <= 300))")
	c.CheckIn(&m.Strategy,
		"longest-idle",
		"ring-all",
		"round-robin",
		"sequential",
		"weighted",
	)
}
