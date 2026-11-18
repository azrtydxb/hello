// momo:fromdb sha256=a8df084ed83639ea02a6f38f912708f58042cdc72f70668a94b95dc4737533cc
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// APIToken models the table "api_tokens".
type APIToken struct {
	momo.Model
	ID         int64 `momo:"pk,serial"`
	UserID     int64
	Name       string
	TokenHash  []byte
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`
	LastUsedAt *time.Time
	Scopes     []string `momo:"nullable"`
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	Kind       string `momo:"default:'legacy'::text,dbdefault"`

	User *User `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *APIToken) MomoConfig(c *momo.Config) {
	c.Unique("api_tokens_token_hash_key", &m.TokenHash)
	c.CheckIn(&m.Kind,
		"legacy",
		"personal",
	)
}
