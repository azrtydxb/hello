// momo:fromdb sha256=b3dd700f35143b6b17be1fcd947fdd6fb20944cbfd27c7151616a3b582f4abf3
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ScimToken models the table "scim_tokens".
type ScimToken struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	ConnectorID int64
	TokenHash   []byte
	Hint        string    `momo:"default:''::text,dbdefault"`
	CreatedBy   string    `momo:"default:''::text,dbdefault"`
	CreatedAt   time.Time `momo:"default:now(),dbdefault"`
	LastUsedAt  *time.Time
	RevokedAt   *time.Time

	Connector *DirectoryConnector `momo:"belongs_to,fk:ConnectorID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ScimToken) MomoConfig(c *momo.Config) {
	c.Unique("scim_tokens_token_hash_key", &m.TokenHash)
	c.Index("scim_tokens_connector", &m.ConnectorID, &m.ID)
}
