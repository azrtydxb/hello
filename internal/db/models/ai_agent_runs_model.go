// momo:fromdb sha256=0bb86c8b1511d6173235736948ad9f18cb83d9e51aedd86c2570d30daa3c770d
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// AiAgentRun models the table "ai_agent_runs".
type AiAgentRun struct {
	momo.Model
	ID         int64 `momo:"pk,serial"`
	Agent      string
	Replica    string
	StartedAt  time.Time `momo:"default:now(),dbdefault"`
	FinishedAt *time.Time
	Outcome    *string
	Tokens     int64 `momo:"default:0,dbdefault"`
	Detail     *types.JSON[json.RawMessage]
	Error      *string
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *AiAgentRun) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Outcome,
		"failed",
		"no_change",
		"ok",
		"skipped_budget",
		"skipped_locked",
	)
	c.Index("ai_agent_runs_agent", &m.Agent, &m.StartedAt).Desc(&m.StartedAt)
}
