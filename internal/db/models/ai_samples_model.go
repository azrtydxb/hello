// momo:fromdb sha256=4a67067e46757d78628ec2da63b48a487a3ba59f66b514c852c80596ae952d45
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// AiSample models the table "ai_samples".
type AiSample struct {
	momo.Model
	Kind    string    `momo:"pk"`
	Subject string    `momo:"pk"`
	At      time.Time `momo:"pk,default:now(),dbdefault,index:ai_samples_at"`
	Value   types.JSON[json.RawMessage]
}
