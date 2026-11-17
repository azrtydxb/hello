// momo:fromdb sha256=324a944909b29330426d7545d9e08db79d9035f7648dcb1ff5019fe2b66b75be
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// AiAgentRequest models the table "ai_agent_requests".
type AiAgentRequest struct {
	momo.Model
	Agent       string `momo:"pk"`
	RequestedBy *int64
	RequestedAt time.Time `momo:"default:now(),dbdefault"`

	User *User `momo:"belongs_to,fk:RequestedBy,on_delete:set_null"`
}
