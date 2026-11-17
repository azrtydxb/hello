// momo:fromdb sha256=4e44eefe1e16852e3b86afa283f7111aec51571965f5d1dc5dd0a9151e675487
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// InboxPresence models the table "inbox_presence".
type InboxPresence struct {
	momo.Model `momo:"table:inbox_presence"`
	UserID     int64     `momo:"pk"`
	SeenAt     time.Time `momo:"default:now(),dbdefault"`

	User *User `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}
