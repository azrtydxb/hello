// momo:fromdb sha256=12fb16e4fd5b11d8d0e95a772dbbe4f21f7dd89f62a9baaf752068ffdb2c530a
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// AiMessageRun models the table "ai_message_runs".
type AiMessageRun struct {
	momo.Model
	ID             int64 `momo:"pk,serial"`
	MessageID      int64
	ConversationID int64
	Kind           string
	State          string `momo:"default:'queued'::text,dbdefault"`
	Result         *types.JSON[json.RawMessage]
	Error          string `momo:"default:''::text,dbdefault"`
	Attempts       int32  `momo:"default:0,dbdefault"`
	ClaimedAt      *time.Time
	CreatedAt      time.Time `momo:"default:now(),dbdefault,index:ai_message_runs_created"`
	FinishedAt     *time.Time

	Conversation *Conversation        `momo:"belongs_to,fk:ConversationID,on_delete:cascade"`
	Message      *ConversationMessage `momo:"belongs_to,fk:MessageID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *AiMessageRun) MomoConfig(c *momo.Config) {
	c.Unique("ai_message_runs_message_id_kind_key", &m.MessageID, &m.Kind)
	c.CheckIn(&m.Kind,
		"autoreply",
		"draft",
		"triage",
	)
	c.CheckIn(&m.State,
		"done",
		"failed",
		"queued",
		"running",
		"skipped",
		"stale",
	)
	c.Index("ai_message_runs_conv", &m.ConversationID, &m.Kind, &m.ID).Desc(&m.ID)
	c.Index("ai_message_runs_open", &m.ID).Where("(state = ANY (ARRAY['queued'::text, 'running'::text]))")
}
