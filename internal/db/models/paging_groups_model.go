// momo:fromdb sha256=5b6d96e0f19737487f1b525f0ad3236983ad0468ca5db1be12dc2d246fffd7d8
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// PagingGroup models the table "paging_groups".
type PagingGroup struct {
	momo.Model
	ID                 int64 `momo:"pk,serial"`
	Name               string
	Code               string
	Priority           string    `momo:"default:'normal'::text,dbdefault"`
	Enabled            *bool     `momo:"default:true,dbdefault"`
	CreatedAt          time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt          time.Time `momo:"default:now(),dbdefault"`
	MulticastChannelID *int64

	MulticastChannel *MulticastChannel `momo:"belongs_to,fk:MulticastChannelID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *PagingGroup) MomoConfig(c *momo.Config) {
	c.Unique("paging_groups_code_key", &m.Code)
	c.Unique("paging_groups_name_key", &m.Name)
	c.Check("paging_groups_code_check", "(code ~ '^\\*[0-9]{2,4}$'::text)")
	c.Check("paging_groups_name_check", "((length(name) >= 1) AND (length(name) <= 64))")
	c.CheckIn(&m.Priority,
		"emergency",
		"normal",
	)
}
