// momo:fromdb sha256=e50f9a1dd3ade4e075ca8c88bea960a182fe2fdc925878c911034928627ef8ae
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ConversationNotify models the table "conversation_notify".
type ConversationNotify struct {
	momo.Model     `momo:"table:conversation_notify"`
	UserID         int64 `momo:"pk"`
	ConversationID int64 `momo:"pk"`
	LastEmailAt    time.Time

	Conversation *Conversation `momo:"belongs_to,fk:ConversationID,on_delete:cascade"`
	User         *User         `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}
