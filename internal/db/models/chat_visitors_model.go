// momo:fromdb sha256=56fdfef600203cdef0342663cd456fc1fb5de8167702bfe0ca4eceecbadcef00
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ChatVisitor models the table "chat_visitors".
type ChatVisitor struct {
	momo.Model
	ID             int64 `momo:"pk,serial"`
	SiteID         int64
	TokenHash      []byte
	Name           string    `momo:"default:''::text,dbdefault"`
	Email          string    `momo:"default:''::text,dbdefault"`
	ConsentToStore bool      `momo:"default:false,dbdefault"`
	IPHash         []byte    `momo:"nullable"`
	UaFamily       string    `momo:"default:''::text,dbdefault"`
	Page           string    `momo:"default:''::text,dbdefault"`
	FirstSeen      time.Time `momo:"default:now(),dbdefault"`
	LastSeen       time.Time `momo:"default:now(),dbdefault"`

	Site *ChatSite `momo:"belongs_to,fk:SiteID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ChatVisitor) MomoConfig(c *momo.Config) {
	c.Unique("chat_visitors_token_hash_key", &m.TokenHash)
	c.Check("chat_visitors_email_check", "(length(email) <= 254)")
	c.Check("chat_visitors_name_check", "(length(name) <= 100)")
	c.Check("chat_visitors_page_check", "(length(page) <= 500)")
	c.Check("chat_visitors_ua_family_check", "(length(ua_family) <= 32)")
	c.Index("chat_visitors_site", &m.SiteID, &m.LastSeen)
}
