// momo:fromdb sha256=1c8a9571de031c0bb048bdc40cea4d604d84cd6168b41f75f1b5dc49574f72a3
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ConversationAssignment models the table "conversation_assignments".
type ConversationAssignment struct {
	momo.Model
	ID             int64 `momo:"pk,serial"`
	ConversationID int64
	FromUserID     *int64
	ToUserID       *int64
	Reason         string    `momo:"default:''::text,dbdefault"`
	At             time.Time `momo:"default:now(),dbdefault"`

	Conversation *Conversation `momo:"belongs_to,fk:ConversationID,on_delete:cascade"`
	FromUser     *User         `momo:"belongs_to,fk:FromUserID,on_delete:set_null"`
	ToUser       *User         `momo:"belongs_to,fk:ToUserID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ConversationAssignment) MomoConfig(c *momo.Config) {
	c.Index("conversation_assignments_conv", &m.ConversationID, &m.ID)
}
