// momo:fromdb sha256=0e829895546b2dc1b5ff267f39656ff60cf81e8d8033e4246d3f01f874dd3a84
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// AuditEvent models the table "audit_events".
type AuditEvent struct {
	momo.Model
	ID         int64 `momo:"pk,serial"`
	Actor      string
	Action     string
	Resource   string
	ResourceID string
	At         time.Time `momo:"default:now(),dbdefault"`
	Via        *string
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *AuditEvent) MomoConfig(c *momo.Config) {
	c.Index("audit_events_supervisor_view", &m.At).Where("(action = 'supervisor_view'::text)")
}
