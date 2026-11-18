// momo:fromdb sha256=84bf7461ef4b8fa1d3b181953c6bf2edf077171b4b7526554d17e2e4ce7a3e83
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// DoorWebhook models the table "door_webhooks".
type DoorWebhook struct {
	momo.Model
	ID              int64 `momo:"pk,serial"`
	Name            string
	SecretEnc       []byte
	Action          string
	RingGroupID     *int64
	PagingGroupID   *int64
	AnnouncementID  *int64
	RateLimit       *int32 `momo:"default:11,dbdefault"`
	Enabled         *bool  `momo:"default:true,dbdefault"`
	LastTriggeredAt *time.Time
	CreatedAt       time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt       time.Time `momo:"default:now(),dbdefault"`

	Announcement *Announcement `momo:"belongs_to,fk:AnnouncementID,on_delete:set_null"`
	PagingGroup  *PagingGroup  `momo:"belongs_to,fk:PagingGroupID,on_delete:restrict"`
	RingGroup    *RingGroup    `momo:"belongs_to,fk:RingGroupID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *DoorWebhook) MomoConfig(c *momo.Config) {
	c.Unique("door_webhooks_name_key", &m.Name)
	c.CheckIn(&m.Action,
		"chime_page",
		"ring_door",
	)
	c.Check("door_webhooks_check", "((action = 'chime_page'::text) OR (paging_group_id IS NULL))")
	c.Check("door_webhooks_check1", "((action = 'ring_door'::text) OR (ring_group_id IS NULL))")
	c.Check("door_webhooks_check2", "((action <> 'chime_page'::text) OR (paging_group_id IS NOT NULL))")
	c.Check("door_webhooks_name_check", "(name ~ '^[A-Za-z0-9._ -]{1,64}$'::text)")
	c.Check("door_webhooks_rate_limit_check", "((rate_limit >= 1) AND (rate_limit <= 600))")
}
