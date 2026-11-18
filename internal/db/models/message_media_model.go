// momo:fromdb sha256=788be73631ddff4692b86f4f63330a30a7cf4fc0536ad038add66fa8ae376029
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// MessageMedia models the table "message_media".
type MessageMedia struct {
	momo.Model        `momo:"table:message_media"`
	ID                int64 `momo:"pk,serial"`
	MessageID         *int64
	ConversationID    *int64
	ChannelID         int64
	Direction         string
	Status            string                      `momo:"default:'pending'::text,dbdefault"`
	ObjectKey         string                      `momo:"default:''::text,dbdefault"`
	ContentType       string                      `momo:"default:''::text,dbdefault"`
	DeclaredType      string                      `momo:"default:''::text,dbdefault"`
	Size              int64                       `momo:"default:0,dbdefault"`
	Sha256            string                      `momo:"default:''::text,dbdefault"`
	Filename          string                      `momo:"default:''::text,dbdefault"`
	Reason            string                      `momo:"default:''::text,dbdefault"`
	SourceRef         types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	UploaderUserID    *int64
	UploaderVisitorID *int64
	Attempts          int32     `momo:"default:0,dbdefault"`
	NextAttemptAt     time.Time `momo:"default:now(),dbdefault"`
	AttachedAt        *time.Time
	CreatedAt         time.Time `momo:"default:now(),dbdefault"`

	Conversation    *Conversation        `momo:"belongs_to,fk:ConversationID,on_delete:set_null"`
	Message         *ConversationMessage `momo:"belongs_to,fk:MessageID,on_delete:set_null"`
	UploaderUser    *User                `momo:"belongs_to,fk:UploaderUserID,on_delete:set_null"`
	UploaderVisitor *ChatVisitor         `momo:"belongs_to,fk:UploaderVisitorID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MessageMedia) MomoConfig(c *momo.Config) {
	c.Check("message_media_content_type_check", "(length(content_type) <= 100)")
	c.Check("message_media_declared_type_check", "(length(declared_type) <= 100)")
	c.CheckIn(&m.Direction,
		"in",
		"out",
	)
	c.Check("message_media_filename_check", "(length(filename) <= 255)")
	c.Check("message_media_object_key_check", "((object_key = ''::text) OR (object_key ~~ 'msg/%'::text))")
	c.Check("message_media_reason_check", "(length(reason) <= 64)")
	c.Check("message_media_sha256_check", "((sha256 = ''::text) OR (sha256 ~ '^[0-9a-f]{64}$'::text))")
	c.Check("message_media_size_check", "(size >= 0)")
	c.CheckIn(&m.Status,
		"expired",
		"pending",
		"ready",
		"rejected",
	)
	c.Index("message_media_conversation", &m.ConversationID).Where("(conversation_id IS NOT NULL)")
	c.Index("message_media_fetch", &m.NextAttemptAt).Where("((status = 'pending'::text) AND (direction = 'in'::text) AND (attached_at IS NOT NULL))")
	c.Index("message_media_loose", &m.CreatedAt).Where("(message_id IS NULL)")
	c.Index("message_media_message", &m.MessageID).Where("(message_id IS NOT NULL)")
	c.Index("message_media_ready", &m.CreatedAt).Where("(status = 'ready'::text)")
	c.Index("message_media_visitor", &m.UploaderVisitorID).Where("(uploader_visitor_id IS NOT NULL)")
}
