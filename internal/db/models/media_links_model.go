// momo:fromdb sha256=45e10651be8a938ca25a9c8656b2aa01479018e23e1503f26bfa6be5fbaf4d1b
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// MediaLink models the table "media_links".
type MediaLink struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	TokenHash []byte
	MediaID   int64
	ExpiresAt time.Time `momo:"index:media_links_expires"`
	MaxUses   *int32    `momo:"default:5,dbdefault"`
	Uses      int32     `momo:"default:0,dbdefault"`
	CreatedAt time.Time `momo:"default:now(),dbdefault"`

	Media *MessageMedia `momo:"belongs_to,fk:MediaID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MediaLink) MomoConfig(c *momo.Config) {
	c.Unique("media_links_token_hash_key", &m.TokenHash)
	c.Check("media_links_max_uses_check", "((max_uses >= 1) AND (max_uses <= 100))")
	c.Check("media_links_uses_check", "(uses >= 0)")
}
