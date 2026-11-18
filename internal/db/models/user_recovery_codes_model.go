// momo:fromdb sha256=9a0f1f7e8fa9e59b256fe040e3c5c1e24dacb2fcb10ca104c83c39d7d8a5227f
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// UserRecoveryCode models the table "user_recovery_codes".
type UserRecoveryCode struct {
	momo.Model
	UserID   int64  `momo:"pk"`
	CodeHash []byte `momo:"pk"`

	User *User `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}
