// momo:fromdb sha256=120d4ea796429f874dbcd52ff32a3f11c8ec0e303f29fc77ba353ee4152145cc
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// OauthRequest models the table "oauth_requests".
type OauthRequest struct {
	momo.Model
	ID            string `momo:"pk"`
	ClientID      string
	RedirectURI   string
	State         *string
	CodeChallenge string
	Scopes        []string
	Resources     []string
	CreatedAt     time.Time `momo:"default:now(),dbdefault"`
	ExpiresAt     time.Time `momo:"index:oauth_requests_expires"`
	UserID        *int64
	CodeHash      []byte `momo:"nullable"`
	CodeExpiresAt *time.Time
	CodeUsedAt    *time.Time

	Client *OauthClient `momo:"belongs_to,fk:ClientID,on_delete:cascade"`
	User   *User        `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *OauthRequest) MomoConfig(c *momo.Config) {
	c.Unique("oauth_requests_code_hash_key", &m.CodeHash)
}
