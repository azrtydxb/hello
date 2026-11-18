// momo:fromdb sha256=ebb16010a6352cf2d675ea176ff021c54c104ea08f3ee547667d7cd9a01d3c1e
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// CdrQueueLeg models the table "cdr_queue_legs".
type CdrQueueLeg struct {
	momo.Model
	CdrID     int64 `momo:"pk"`
	Position  int32 `momo:"pk"`
	Event     string
	Queue     string
	Extension string `momo:"default:''::text,dbdefault"`
	WaitMs    int64  `momo:"default:0,dbdefault"`
	Result    string `momo:"default:''::text,dbdefault"`
	At        time.Time

	Cdr *Cdr `momo:"belongs_to,fk:CdrID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CdrQueueLeg) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Event,
		"abandon",
		"agent",
		"answered",
		"callback_connected",
		"callback_dialled",
		"callback_failed",
		"callback_requested",
		"enter",
		"exit",
		"wait",
	)
}
