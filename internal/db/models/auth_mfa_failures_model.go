// momo:fromdb sha256=6e31ffeb8f900637aa5fc1254cdcdba5f8faeb36f9a480e474b95c9568fbd1db
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// AuthMfaFailure models the table "auth_mfa_failures".
type AuthMfaFailure struct {
	momo.Model
	UserID int64
	At     time.Time `momo:"default:now(),dbdefault"`

	User *User `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *AuthMfaFailure) MomoConfig(c *momo.Config) {
	c.Index("auth_mfa_failures_user", &m.UserID, &m.At)
}
