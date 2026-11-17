// momo:fromdb sha256=089762b7018e3c820d7b60d8757af24c5d1a1efa0b841a566b92f7b5c79ed61d
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// Conversation models the table "conversations".
type Conversation struct {
	momo.Model
	ID              int64 `momo:"pk,serial"`
	ChannelID       int64
	AddressID       int64
	PeerKey         string `momo:"index:conversations_peer"`
	PeerKind        string `momo:"default:'phone'::text,dbdefault"`
	PeerName        string `momo:"default:''::text,dbdefault"`
	PeerNameSource  string `momo:"default:''::text,dbdefault"`
	Status          string `momo:"default:'open'::text,dbdefault"`
	SnoozedUntil    *time.Time
	AssigneeUserID  *int64
	Priority        string   `momo:"default:'normal'::text,dbdefault"`
	Tags            []string `momo:"default:'{}'::text[],dbdefault"`
	LastMessageID   *int64
	LastMessageAt   time.Time `momo:"default:now(),dbdefault"`
	LastInboundAt   *time.Time
	FirstResponseAt *time.Time
	ResolvedAt      *time.Time
	CreatedAt       time.Time `momo:"default:now(),dbdefault,index:conversations_created"`
	Origin          string    `momo:"default:'inbound'::text,dbdefault"`
	CreatedByUserID *int64
	ConsentNote     string `momo:"default:''::text,dbdefault"`
	Offline         bool   `momo:"default:false,dbdefault"`
	QueueID         *int64
	RingGroupID     *int64
	RoutedAt        *time.Time
	AssignedAt      *time.Time
	FrtCounted      bool `momo:"default:false,dbdefault"`
	AiTriage        *types.JSON[json.RawMessage]
	AiApplied       types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	AiSpam          bool                        `momo:"default:false,dbdefault"`
	AiHandoffAt     *time.Time
	AiHandoffReason string `momo:"default:''::text,dbdefault"`

	Address       *MsgAddress `momo:"belongs_to,fk:AddressID,on_delete:cascade"`
	AssigneeUser  *User       `momo:"belongs_to,fk:AssigneeUserID,on_delete:set_null"`
	Channel       *MsgChannel `momo:"belongs_to,fk:ChannelID,on_delete:cascade"`
	CreatedByUser *User       `momo:"belongs_to,fk:CreatedByUserID,on_delete:set_null"`
	Queue         *Queue      `momo:"belongs_to,fk:QueueID,on_delete:set_null"`
	RingGroup     *RingGroup  `momo:"belongs_to,fk:RingGroupID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Conversation) MomoConfig(c *momo.Config) {
	c.Check("conversations_consent_note_check", "(length(consent_note) <= 500)")
	c.CheckIn(&m.Origin,
		"inbound",
		"outbound",
	)
	c.CheckIn(&m.PeerKind,
		"phone",
		"visitor",
	)
	c.CheckIn(&m.PeerNameSource,
		"",
		"company",
		"extensions",
		"named",
		"provider",
		"visitor",
	)
	c.CheckIn(&m.Priority,
		"high",
		"low",
		"normal",
	)
	c.CheckIn(&m.Status,
		"open",
		"pending",
		"resolved",
		"snoozed",
		"spam",
	)
	c.Index("conversations_assignee", &m.AssigneeUserID, &m.Status, &m.LastMessageAt).Desc(&m.LastMessageAt)
	c.Index("conversations_frt", &m.ID).Where("((NOT frt_counted) AND (first_response_at IS NOT NULL))")
	c.Unique("conversations_open_peer", &m.AddressID, &m.PeerKey).Where("(status <> ALL (ARRAY['resolved'::text, 'spam'::text]))")
	c.Index("conversations_outbound_recent", &m.AddressID, &m.CreatedAt).Where("(origin = 'outbound'::text)")
	c.Index("conversations_outbound_user", &m.CreatedByUserID, &m.CreatedAt).Where("(origin = 'outbound'::text)")
	c.Index("conversations_pool", &m.Status, &m.LastMessageAt).Where("(assignee_user_id IS NULL)")
	c.Index("conversations_queue_waiting", &m.QueueID, &m.RoutedAt, &m.ID).Where("((queue_id IS NOT NULL) AND (assignee_user_id IS NULL) AND (status = ANY (ARRAY['open'::text, 'pending'::text])))")
	c.Index("conversations_recent", &m.LastMessageAt, &m.ID).Desc(&m.LastMessageAt).Desc(&m.ID)
	c.Index("conversations_ring_group", &m.RingGroupID).Where("((ring_group_id IS NOT NULL) AND (assignee_user_id IS NULL))")
	c.Index("conversations_snoozed", &m.SnoozedUntil).Where("(status = 'snoozed'::text)")
	c.Index("conversations_unrouted", &m.ID).Where("(routed_at IS NULL)")
}
