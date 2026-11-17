// momo:fromdb sha256=d7f42e2b06de080a6cb828b09fdee8a671ca166a89a2bec8562181b2b63826c7
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
	"github.com/google/uuid"
)

// AiTask models the table "ai_tasks".
type AiTask struct {
	momo.Model
	ID           uuid.UUID `momo:"pk"`
	Kind         string
	RequestedBy  int64
	SessionID    *uuid.UUID
	Replica      string
	Status       string `momo:"default:'queued'::text,dbdefault"`
	ErrorCode    *string
	ErrorMessage *string
	Result       *types.JSON[json.RawMessage]
	CreatedAt    time.Time `momo:"default:now(),dbdefault,index:ai_tasks_created"`
	StartedAt    *time.Time
	FinishedAt   *time.Time

	User    *User      `momo:"belongs_to,fk:RequestedBy,on_delete:cascade"`
	Session *AiSession `momo:"belongs_to,fk:SessionID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *AiTask) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Status,
		"failed",
		"queued",
		"running",
		"succeeded",
	)
	c.Index("ai_tasks_status_replica", &m.Status, &m.Replica)
}
