// momo:fromdb sha256=9221331dc29edede51283d3b8a3af2b69ce18b71bae7df55078b73417cdc52f5
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
	"github.com/google/uuid"
)

// AiFinding models the table "ai_findings".
type AiFinding struct {
	momo.Model
	ID              uuid.UUID `momo:"pk"`
	CandidateID     string
	Type            string
	Subject         string
	Severity        string
	Status          string `momo:"default:'open'::text,dbdefault"`
	Title           string
	Evidence        types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	Explanation     *types.JSON[json.RawMessage]
	Explained       bool `momo:"default:false,dbdefault"`
	Rank            *int32
	FirstSeen       time.Time                   `momo:"default:now(),dbdefault"`
	LastSeen        time.Time                   `momo:"default:now(),dbdefault"`
	Occurrences     *int64                      `momo:"default:1,dbdefault"`
	SeverityHistory types.JSON[json.RawMessage] `momo:"default:'[]'::jsonb,dbdefault"`
	AcknowledgedBy  *int64
	AcknowledgedAt  *time.Time
	DismissedBy     *int64
	DismissedAt     *time.Time
	DismissReason   *string
	ResolvedAt      *time.Time

	User    *User `momo:"belongs_to,fk:AcknowledgedBy,on_delete:set_null"`
	UserRef *User `momo:"belongs_to,fk:DismissedBy,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *AiFinding) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Severity,
		"critical",
		"info",
		"warning",
	)
	c.CheckIn(&m.Status,
		"acknowledged",
		"dismissed",
		"open",
		"resolved",
	)
	c.Index("ai_findings_candidate", &m.CandidateID, &m.LastSeen).Desc(&m.LastSeen)
	c.Unique("ai_findings_live_candidate", &m.CandidateID).Where("(status = ANY (ARRAY['open'::text, 'acknowledged'::text]))")
	c.Index("ai_findings_status", &m.Status, &m.Severity)
}
