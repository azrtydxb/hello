// momo:fromdb sha256=7b9223d260cda12d1bd00eab6e80db081b15b5580383538fbaa2e86c68c6429e
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
	"github.com/google/uuid"
)

// AiProposal models the table "ai_proposals".
type AiProposal struct {
	momo.Model
	ID             uuid.UUID `momo:"pk"`
	Source         string
	SessionID      *uuid.UUID
	FindingID      *uuid.UUID
	Fingerprint    string
	Title          string
	Rationale      string `momo:"default:''::text,dbdefault"`
	Actions        types.JSON[json.RawMessage]
	ConfigRevision int64
	Status         string `momo:"default:'open'::text,dbdefault"`
	CreatedBy      *int64
	CreatedAt      time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt      time.Time `momo:"default:now(),dbdefault"`
	AppliedBy      *int64
	AppliedAt      *time.Time
	DismissedBy    *int64
	DismissedAt    *time.Time
	DismissReason  *string
	DismissText    *string
	Failure        *types.JSON[json.RawMessage]
	AppliedVia     *string
	BatchID        *uuid.UUID

	User    *User        `momo:"belongs_to,fk:AppliedBy,on_delete:set_null"`
	Batch   *ConfigBatch `momo:"belongs_to,fk:BatchID,on_delete:set_null"`
	UserRef *User        `momo:"belongs_to,fk:CreatedBy,on_delete:set_null"`
	User2   *User        `momo:"belongs_to,fk:DismissedBy,on_delete:set_null"`
	Finding *AiFinding   `momo:"belongs_to,fk:FindingID,on_delete:set_null"`
	Session *AiSession   `momo:"belongs_to,fk:SessionID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *AiProposal) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Status,
		"applied",
		"dismissed",
		"failed",
		"open",
		"stale",
		"superseded",
	)
	c.Index("ai_proposals_fingerprint", &m.Fingerprint, &m.Status)
	c.Index("ai_proposals_status", &m.Status, &m.CreatedAt).Desc(&m.CreatedAt)
}
