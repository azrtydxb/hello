// momo:fromdb sha256=2ecf90773a81d1975688f71e1a653a44f93a519e5cdb645b8141b712ffbd62f5
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Message models the table "messages".
type Message struct {
	momo.Model
	ID                    int64  `momo:"pk,serial"`
	Batch                 string `momo:"index:messages_batch"`
	Sender                string
	SenderKind            string
	SenderName            string `momo:"default:''::text,dbdefault"`
	Actor                 string `momo:"default:''::text,dbdefault"`
	Recipient             string
	RecipientKind         string
	Scope                 string `momo:"default:'direct'::text,dbdefault"`
	ScopeRef              string `momo:"default:''::text,dbdefault"`
	TrunkID               *int64
	ContentType           string `momo:"default:'text/plain'::text,dbdefault"`
	BodyText              *string
	BodyRaw               []byte    `momo:"nullable"`
	Status                string    `momo:"default:'pending'::text,dbdefault"`
	Detail                string    `momo:"default:''::text,dbdefault"`
	Attempts              int32     `momo:"default:0,dbdefault"`
	CreatedAt             time.Time `momo:"default:now(),dbdefault,index:messages_created"`
	ExpiresAt             time.Time
	NextAttemptAt         time.Time `momo:"default:now(),dbdefault"`
	ClaimedAt             *time.Time
	ClaimedBy             string `momo:"default:''::text,dbdefault"`
	DeliveredAt           *time.Time
	ConversationMessageID *int64

	ConversationMessage *ConversationMessage `momo:"belongs_to,fk:ConversationMessageID,on_delete:set_null"`
	Trunk               *Trunk               `momo:"belongs_to,fk:TrunkID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Message) MomoConfig(c *momo.Config) {
	c.Check("messages_check", "((body_text IS NULL) <> (body_raw IS NULL))")
	c.Check("messages_content_type_check", "((length(content_type) >= 1) AND (length(content_type) <= 128))")
	c.CheckIn(&m.RecipientKind,
		"extension",
		"external",
	)
	c.CheckIn(&m.Scope,
		"broadcast",
		"direct",
		"paging_group",
		"ring_group",
	)
	c.CheckIn(&m.SenderKind,
		"extension",
		"external",
		"system",
	)
	c.CheckIn(&m.Status,
		"delivered",
		"failed",
		"pending",
		"sending",
	)
	c.Index("messages_conversation_message", &m.ConversationMessageID).Where("(conversation_message_id IS NOT NULL)")
	c.Index("messages_pending", &m.RecipientKind, &m.Recipient, &m.ID).Where("(status = 'pending'::text)")
	c.Index("messages_recipient", &m.Recipient, &m.ID).Desc(&m.ID)
	c.Index("messages_sender", &m.Sender, &m.ID).Desc(&m.ID)
	c.Index("messages_sending", &m.ClaimedAt).Where("(status = 'sending'::text)")
}
