// momo:fromdb sha256=66061778aa64f423eb2eae5d1a97c03a2f660e68c630143a1812ffdd68c7434b
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// OauthClient models the table "oauth_clients".
type OauthClient struct {
	momo.Model
	ClientID       string `momo:"pk"`
	Kind           string
	Name           string
	ClientURI      *string
	RedirectURIs   []string `momo:"default:'{}'::text[],dbdefault"`
	Metadata       *types.JSON[json.RawMessage]
	FetchedAt      *time.Time
	CacheUntil     *time.Time
	CreatedAt      time.Time `momo:"default:now(),dbdefault"`
	LastUsedAt     *time.Time
	Description    *string
	Role           *string
	Scopes         []string `momo:"nullable"`
	Enabled        *bool    `momo:"default:true,dbdefault"`
	CreatedBy      *int64
	ExtensionScope []string `momo:"nullable"`

	User *User `momo:"belongs_to,fk:CreatedBy,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *OauthClient) MomoConfig(c *momo.Config) {
	c.Check("oauth_clients_check", "((kind <> 'service'::text) OR ((role IS NOT NULL) AND (scopes IS NOT NULL)))")
	c.CheckIn(&m.Kind,
		"cimd",
		"dcr",
		"preregistered",
		"service",
	)
	c.CheckIn(&m.Role,
		"admin",
		"operator",
		"viewer",
	)
}
