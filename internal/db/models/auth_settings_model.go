// momo:fromdb sha256=c23f18aeb789cafa3e09932d33d21af15dea2b736cf4a27ad5d01c8db130e426
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// AuthSetting models the table "auth_settings".
type AuthSetting struct {
	momo.Model
	ID              *bool    `momo:"pk,default:true,dbdefault"`
	Require2faRoles []string `momo:"column:require_2fa_roles,default:'{}'::text[],dbdefault"`
	SsoRequired     bool     `momo:"default:false,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *AuthSetting) MomoConfig(c *momo.Config) {
	c.Check("auth_settings_id_check", "id")
}
