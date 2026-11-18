// momo:fromdb sha256=e3ed691e2e65bcae3a6663a02c2ee806de4dbede8728a8de024396d7f9888a96
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ConversationEvent models the table "conversation_events".
type ConversationEvent struct {
	momo.Model
	ID             int64 `momo:"pk,serial"`
	UserID         int64
	Type           string
	ConversationID int64
	MessageID      *int64
	At             time.Time `momo:"default:now(),dbdefault,index:conversation_events_at"`
	Notified       bool      `momo:"default:false,dbdefault"`

	Conversation *Conversation `momo:"belongs_to,fk:ConversationID,on_delete:cascade"`
	User         *User         `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ConversationEvent) MomoConfig(c *momo.Config) {
	c.Index("conversation_events_unnotified", &m.At).Where("(NOT notified)")
	c.Index("conversation_events_user", &m.UserID, &m.ID)
}
