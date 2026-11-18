// momo:fromdb sha256=13e799b15af6dbbdc5e674064aecce5f3864d94999c9e0ac03a2903a42aeb386
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ScepChallenge models the table "scep_challenges".
type ScepChallenge struct {
	momo.Model
	PhoneID      int64 `momo:"pk"`
	ChallengeEnc []byte
	CreatedAt    time.Time `momo:"default:now(),dbdefault"`

	Phone *Phone `momo:"belongs_to,fk:PhoneID,on_delete:cascade"`
}
