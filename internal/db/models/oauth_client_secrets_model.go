// momo:fromdb sha256=ecf3f7c2ecdc1a0c8f54e4bebd57e4d6a525e540c4b193500e2ccf3248a983e9
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// OauthClientSecret models the table "oauth_client_secrets".
type OauthClientSecret struct {
	momo.Model
	ID         int64  `momo:"pk,serial"`
	ClientID   string `momo:"index:oauth_client_secrets_client"`
	SecretHash []byte
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time

	Client *OauthClient `momo:"belongs_to,fk:ClientID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *OauthClientSecret) MomoConfig(c *momo.Config) {
	c.Unique("oauth_client_secrets_secret_hash_key", &m.SecretHash)
}
