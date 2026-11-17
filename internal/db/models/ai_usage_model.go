// momo:fromdb sha256=9bd3f878befb7ea60d90d41954ec583b2c426cd2f1c58cc89859e37e24de8174
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// AiUsage models the table "ai_usage".
type AiUsage struct {
	momo.Model      `momo:"table:ai_usage"`
	Day             types.Date `momo:"pk"`
	InputTokens     int64      `momo:"default:0,dbdefault"`
	OutputTokens    int64      `momo:"default:0,dbdefault"`
	ReasoningTokens int64      `momo:"default:0,dbdefault"`
	Calls           int64      `momo:"default:0,dbdefault"`
}
