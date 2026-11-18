// momo:fromdb sha256=0f246de9c130d316a38169c725b7683ec035a6ad890268c8204d6e5addc61f87
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
	"github.com/google/uuid"
)

// AiMessage models the table "ai_messages".
type AiMessage struct {
	momo.Model
	ID         int64 `momo:"pk,serial"`
	SessionID  uuid.UUID
	Role       string
	Content    string
	ToolCalls  *types.JSON[json.RawMessage]
	Citations  *types.JSON[json.RawMessage]
	ProposalID *uuid.UUID
	TaskID     *uuid.UUID
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`
	Context    *types.JSON[json.RawMessage]

	Proposal *AiProposal `momo:"belongs_to,fk:ProposalID,on_delete:set_null"`
	Session  *AiSession  `momo:"belongs_to,fk:SessionID,on_delete:cascade"`
	Task     *AiTask     `momo:"belongs_to,fk:TaskID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *AiMessage) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Role,
		"assistant",
		"user",
	)
	c.Index("ai_messages_session", &m.SessionID, &m.ID)
}
