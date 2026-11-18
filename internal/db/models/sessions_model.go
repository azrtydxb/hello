// momo:fromdb sha256=67697b6ca09ebf4a29cb0ae3cf80c671b0d9b4cafddf0084115f004932251ad8
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Session models the table "sessions".
type Session struct {
	momo.Model
	TokenHash []byte `momo:"pk"`
	UserID    int64
	ExpiresAt time.Time `momo:"index:sessions_expires_at"`

	User *User `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}
