// momo:fromdb sha256=c718cf9f456c659b3ed6e3b3875a0cc6ee1f2c0ed1aa3e3631c7eb4cc06f7558
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// UserIdentity models the table "user_identities".
type UserIdentity struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	UserID      int64
	Provider    string
	Subject     string
	Tenant      string    `momo:"default:''::text,dbdefault"`
	Email       string    `momo:"default:''::text,dbdefault"`
	CreatedAt   time.Time `momo:"default:now(),dbdefault"`
	LastLoginAt *time.Time

	User *User `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *UserIdentity) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Provider,
		"entra",
		"google",
	)
	c.Check("user_identities_subject_check", "((char_length(subject) >= 1) AND (char_length(subject) <= 255))")
	c.Unique("user_identities_subject", &m.Provider, &m.Subject).Method("btree")
	c.Unique("user_identities_user", &m.UserID, &m.Provider).Method("btree")
}
