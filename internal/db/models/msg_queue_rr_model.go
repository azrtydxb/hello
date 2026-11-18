// momo:fromdb sha256=9291064ad3e1e1b80b0f0a6450e08e0dff1b6e34cd1252ca9795f0bc81129017
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// MsgQueueRr models the table "msg_queue_rr".
type MsgQueueRr struct {
	momo.Model `momo:"table:msg_queue_rr"`
	QueueID    int64 `momo:"pk"`
	Rr         int64 `momo:"default:0,dbdefault"`

	Queue *Queue `momo:"belongs_to,fk:QueueID,on_delete:cascade"`
}
