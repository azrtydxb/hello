// momo:fromdb sha256=b4b3eff83d3b59e8dd44c809981e6ef4ee0ca309212df887a0be16a88d5a3ca2
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Recording models the table "recordings".
type Recording struct {
	momo.Model
	ID            int64 `momo:"pk,serial"`
	CorrelationID string
	MinioObject   string
	InitiatedBy   string
	DurationMs    int64     `momo:"default:0,dbdefault"`
	CreatedAt     time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Recording) MomoConfig(c *momo.Config) {
	c.Unique("recordings_correlation_id_key", &m.CorrelationID)
	c.CheckIn(&m.InitiatedBy,
		"api",
		"default",
		"dtmf",
	)
}
