// momo:fromdb sha256=0e31c6d2f0a002e9dadf72b6bc0477a383acc61085cbf570150b081616d12046
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// MessagingEvent models the table "messaging_events".
type MessagingEvent struct {
	momo.Model
	ChannelID  int64     `momo:"pk"`
	EventKey   string    `momo:"pk"`
	ReceivedAt time.Time `momo:"default:now(),dbdefault,index:messaging_events_received"`

	Channel *MsgChannel `momo:"belongs_to,fk:ChannelID,on_delete:cascade"`
}
