// momo:fromdb sha256=87d26f707829c261d15571915c0e5dab4bfcea4d435b6029e30f13b9590383cb
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// IntegrationEvent models the table "integration_events".
type IntegrationEvent struct {
	momo.Model
	ID              string `momo:"pk"`
	CursorMs        int64
	CursorSeq       int64
	Type            string
	CreatedAt       time.Time `momo:"index:integration_events_created"`
	CorrelationID   string    `momo:"default:''::text,dbdefault,index:integration_events_correlation"`
	ExtensionNumber string    `momo:"default:''::text,dbdefault"`
	Payload         types.JSON[json.RawMessage]
	IngestedAt      time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *IntegrationEvent) MomoConfig(c *momo.Config) {
	c.Unique("integration_events_cursor", &m.CursorMs, &m.CursorSeq).Method("btree")
	c.Index("integration_events_extension", &m.ExtensionNumber, &m.CursorMs, &m.CursorSeq)
}
