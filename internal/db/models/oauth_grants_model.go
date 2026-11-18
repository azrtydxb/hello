// momo:fromdb sha256=f4c57e8323e7718599fd81a143150f0f55f06463c2c5ce1f5b80c64005f65d98
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// OauthGrant models the table "oauth_grants".
type OauthGrant struct {
	momo.Model
	ID         int64 `momo:"pk,serial"`
	UserID     int64 `momo:"index:oauth_grants_user"`
	ClientID   string
	Scopes     []string
	Resources  []string
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`
	LastUsedAt *time.Time
	RevokedAt  *time.Time

	Client *OauthClient `momo:"belongs_to,fk:ClientID,on_delete:cascade"`
	User   *User        `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}
