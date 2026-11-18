// momo:fromdb sha256=d39b249a2b06333f442eb240cee58601c3f696cd91dfa24cd8496676f8d8c289
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// AuthMfaChallenge models the table "auth_mfa_challenges".
type AuthMfaChallenge struct {
	momo.Model
	TokenHash []byte `momo:"pk"`
	UserID    int64
	ExpiresAt time.Time `momo:"index:auth_mfa_challenges_expires"`
	Attempts  int32     `momo:"default:0,dbdefault"`

	User *User `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}
