// momo:fromdb sha256=b921aa957330491c8b3b66b9728475eac7b9f065d0ac33b5b2be9dcf5200ba7d
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ConversationMessage models the table "conversation_messages".
type ConversationMessage struct {
	momo.Model
	ID                int64 `momo:"pk,serial"`
	ConversationID    int64
	ChannelID         int64
	Direction         string
	Kind              string `momo:"default:'message'::text,dbdefault"`
	SenderKind        string
	SenderUserID      *int64
	SenderName        string `momo:"default:''::text,dbdefault"`
	Body              string
	ContentType       string `momo:"default:'text/plain'::text,dbdefault"`
	Status            string `momo:"default:'delivered'::text,dbdefault"`
	ErrorCode         string `momo:"default:''::text,dbdefault"`
	ProviderMessageID *string
	ClientMsgID       *string
	CreatedAt         time.Time `momo:"default:now(),dbdefault"`
	SentAt            *time.Time
	DeliveredAt       *time.Time
	ReadAt            *time.Time
	AttemptID         *string
	ClaimedAt         *time.Time
	NextPollAt        *time.Time
	PollCount         int32 `momo:"default:0,dbdefault"`
	AiRunID           *int64
	RedactedAt        *time.Time

	AiRun        *AiMessageRun `momo:"belongs_to,fk:AiRunID,on_delete:set_null"`
	Conversation *Conversation `momo:"belongs_to,fk:ConversationID,on_delete:cascade"`
	SenderUser   *User         `momo:"belongs_to,fk:SenderUserID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ConversationMessage) MomoConfig(c *momo.Config) {
	c.Unique("conversation_messages_channel_id_provider_message_id_key", &m.ChannelID, &m.ProviderMessageID)
	c.Unique("conversation_messages_conversation_id_client_msg_id_key", &m.ConversationID, &m.ClientMsgID)
	c.CheckIn(&m.Direction,
		"in",
		"out",
	)
	c.CheckIn(&m.Kind,
		"message",
		"note",
		"system",
	)
	c.CheckIn(&m.SenderKind,
		"ai",
		"customer",
		"system",
		"user",
	)
	c.CheckIn(&m.Status,
		"delivered",
		"failed",
		"queued",
		"read",
		"sending",
		"sent",
		"unknown",
	)
	c.Index("conversation_messages_ai", &m.CreatedAt).Where("(sender_kind = 'ai'::text)")
	c.Index("conversation_messages_claimed", &m.ClaimedAt).Where("(claimed_at IS NOT NULL)")
	c.Index("conversation_messages_created", &m.CreatedAt).Where("(kind = 'message'::text)")
	c.Index("conversation_messages_open", &m.ID).Where("(status = ANY (ARRAY['queued'::text, 'sending'::text]))")
	c.Index("conversation_messages_poll", &m.NextPollAt).Where("(next_poll_at IS NOT NULL)")
	c.IndexExpr("conversation_messages_search", "to_tsvector('simple', body)").Method("gin")
	c.Index("conversation_messages_thread", &m.ConversationID, &m.ID).Desc(&m.ID)
}
