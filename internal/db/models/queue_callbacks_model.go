// momo:fromdb sha256=ef14f3537e89e88aac0be5b332cdce7fd291ceba01c5ab24f2962028f12e1c7b
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// QueueCallback models the table "queue_callbacks".
type QueueCallback struct {
	momo.Model
	ID            int64 `momo:"pk,serial"`
	QueueID       int64
	Number        string
	CallerName    string `momo:"default:''::text,dbdefault"`
	Status        string `momo:"default:'waiting'::text,dbdefault"`
	Attempts      int32  `momo:"default:0,dbdefault"`
	PositionKey   int64
	NextAttemptAt time.Time `momo:"default:now(),dbdefault"`
	ClaimedBy     string    `momo:"default:''::text,dbdefault"`
	LeaseUntil    *time.Time
	Agent         string    `momo:"default:''::text,dbdefault"`
	Result        string    `momo:"default:''::text,dbdefault"`
	CreatedAt     time.Time `momo:"default:now(),dbdefault"`
	ExpiresAt     time.Time
	FinishedAt    *time.Time

	Queue *Queue `momo:"belongs_to,fk:QueueID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *QueueCallback) MomoConfig(c *momo.Config) {
	c.Check("queue_callbacks_attempts_check", "((attempts >= 0) AND (attempts <= 3))")
	c.Check("queue_callbacks_number_check", "(number ~ '^\\+?[0-9]{2,20}$'::text)")
	c.CheckIn(&m.Status,
		"cancelled",
		"connected",
		"dialling",
		"expired",
		"failed",
		"waiting",
	)
	c.Index("queue_callbacks_due", &m.QueueID, &m.PositionKey).Where("(status = ANY (ARRAY['waiting'::text, 'dialling'::text]))")
	c.Index("queue_callbacks_finished", &m.FinishedAt).Where("(finished_at IS NOT NULL)")
	c.Unique("queue_callbacks_open_number", &m.QueueID, &m.Number).Where("(status = ANY (ARRAY['waiting'::text, 'dialling'::text]))")
}
