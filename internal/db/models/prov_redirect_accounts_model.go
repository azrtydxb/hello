// momo:fromdb sha256=4778ea0304306be0280e54881a58872533f634c4cf591d1ccbeda2324ac532f5
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// ProvRedirectAccount models the table "prov_redirect_accounts".
type ProvRedirectAccount struct {
	momo.Model
	Vendor          string                      `momo:"pk"`
	CredentialsEnc  []byte                      `momo:"nullable"`
	Settings        types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	Enabled         bool                        `momo:"default:false,dbdefault"`
	LastCheckAt     *time.Time
	LastCheckResult *string
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ProvRedirectAccount) MomoConfig(c *momo.Config) {
	c.Check("prov_redirect_accounts_settings_check", "(jsonb_typeof(settings) = 'object'::text)")
	c.CheckIn(&m.Vendor,
		"fanvil",
		"grandstream",
		"poly",
		"snom",
		"yealink",
	)
}
