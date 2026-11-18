// momo:fromdb sha256=7ad1fc7bf461e1b9d529feb50baa2d855fa583d39bac486191b62a3cc331ad64
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// MsgAgentStat models the table "msg_agent_stats".
type MsgAgentStat struct {
	momo.Model
	UserID       int64 `momo:"pk"`
	QueueID      int64 `momo:"pk"`
	TextAnswered int64 `momo:"default:0,dbdefault"`
	LastEnd      *time.Time

	Queue *Queue `momo:"belongs_to,fk:QueueID,on_delete:cascade"`
	User  *User  `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}
