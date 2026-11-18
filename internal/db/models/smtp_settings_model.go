// momo:fromdb sha256=24f16182d7688d1058e3a38306c54825b6f508d866c3be69713602c2c85a1457
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// SMTPSetting models the table "smtp_settings".
type SMTPSetting struct {
	momo.Model
	ID             *bool     `momo:"pk,default:true,dbdefault"`
	Host           string    `momo:"default:''::text,dbdefault"`
	Port           *int32    `momo:"default:587,dbdefault"`
	Security       string    `momo:"default:'starttls'::text,dbdefault"`
	Username       string    `momo:"default:''::text,dbdefault"`
	PasswordSealed []byte    `momo:"nullable"`
	FromAddress    string    `momo:"default:''::text,dbdefault"`
	UpdatedAt      time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *SMTPSetting) MomoConfig(c *momo.Config) {
	c.Check("smtp_settings_id_check", "id")
	c.Check("smtp_settings_port_check", "((port >= 1) AND (port <= 65535))")
	c.CheckIn(&m.Security,
		"none",
		"starttls",
		"tls",
	)
}
