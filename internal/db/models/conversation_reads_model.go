// momo:fromdb sha256=7541fcc59ba9b4018291e2d612e39241fb083e018e44421926367b04b1e3d3da
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ConversationRead models the table "conversation_reads".
type ConversationRead struct {
	momo.Model
	UserID            int64     `momo:"pk"`
	ConversationID    int64     `momo:"pk"`
	LastReadMessageID int64     `momo:"default:0,dbdefault"`
	At                time.Time `momo:"default:now(),dbdefault"`

	Conversation *Conversation `momo:"belongs_to,fk:ConversationID,on_delete:cascade"`
	User         *User         `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}
