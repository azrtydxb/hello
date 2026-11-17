// momo:fromdb sha256=988d16ca98dce96cabf692575a32795fe45e7946ec95d5ee8c09ee175604bb43
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// OauthToken models the table "oauth_tokens".
type OauthToken struct {
	momo.Model
	Hash      []byte `momo:"pk"`
	Kind      string
	GrantID   *int64 `momo:"index:oauth_tokens_grant"`
	ClientID  string
	UserID    *int64
	Scopes    []string
	Resources []string
	CreatedAt time.Time `momo:"default:now(),dbdefault"`
	ExpiresAt time.Time `momo:"index:oauth_tokens_expires"`
	SpentAt   *time.Time
	RevokedAt *time.Time

	Client *OauthClient `momo:"belongs_to,fk:ClientID,on_delete:cascade"`
	Grant  *OauthGrant  `momo:"belongs_to,fk:GrantID,on_delete:cascade"`
	User   *User        `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *OauthToken) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Kind,
		"access",
		"refresh",
	)
}
