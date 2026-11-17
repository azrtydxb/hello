// momo:fromdb sha256=8d0ce5254a9620fdc7e3659cb6a21d4792f03be81afffea01370ce9cd0662754
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// SsoProvider models the table "sso_providers".
type SsoProvider struct {
	momo.Model
	Provider        string    `momo:"pk"`
	Enabled         bool      `momo:"default:false,dbdefault"`
	ClientID        string    `momo:"default:''::text,dbdefault"`
	ClientSecretEnc []byte    `momo:"nullable"`
	Tenant          string    `momo:"default:''::text,dbdefault"`
	Domains         []string  `momo:"default:'{}'::text[],dbdefault"`
	Jit             bool      `momo:"default:false,dbdefault"`
	UpdatedAt       time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *SsoProvider) MomoConfig(c *momo.Config) {
	c.Check("sso_providers_client_id_check", "(char_length(client_id) <= 256)")
	c.CheckIn(&m.Provider,
		"entra",
		"google",
	)
	c.Check("sso_providers_tenant_check", "(char_length(tenant) <= 64)")
}
