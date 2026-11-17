// momo:fromdb sha256=dec875762628b412eb3afa6ddb726c1bc460be326650dd37b25b1828f5e29205
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Device models the table "devices".
type Device struct {
	momo.Model
	ID                  int64 `momo:"pk,serial"`
	ExtensionID         int64 `momo:"index:devices_extension_id"`
	SipUsername         string
	Realm               string
	Ha1Md5              *string
	Ha1Sha256           *string
	Enabled             *bool     `momo:"default:true,dbdefault"`
	CreatedAt           time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt           time.Time `momo:"default:now(),dbdefault"`
	SecretEnc           []byte    `momo:"nullable"`
	Kind                string    `momo:"default:'desk'::text,dbdefault"`
	UserID              *int64
	Label               string `momo:"default:''::text,dbdefault"`
	Platform            *string
	AppVersion          *string
	LastSeenAt          *time.Time
	AuthMode            string `momo:"default:'digest'::text,dbdefault"`
	Srtp                string `momo:"default:'optional'::text,dbdefault"`
	SrtpKeyex           string `momo:"default:'sdes'::text,dbdefault"`
	MediaIce            bool   `momo:"default:false,dbdefault"`
	DisabledByDirectory bool   `momo:"default:false,dbdefault"`

	Extension *Extension `momo:"belongs_to,fk:ExtensionID,on_delete:cascade"`
	User      *User      `momo:"belongs_to,fk:UserID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Device) MomoConfig(c *momo.Config) {
	c.Unique("devices_sip_username_key", &m.SipUsername)
	c.CheckIn(&m.AuthMode,
		"derived",
		"digest",
	)
	c.Check("devices_client_derived", "((kind = 'desk'::text) OR (auth_mode = 'derived'::text))")
	c.Check("devices_client_owner", "((kind = 'desk'::text) OR (user_id IS NOT NULL))")
	c.Check("devices_ha1_present", "((auth_mode = 'derived'::text) OR ((ha1_md5 IS NOT NULL) AND (ha1_sha256 IS NOT NULL)))")
	c.CheckIn(&m.Kind,
		"desk",
		"mobile",
		"web",
	)
	c.Check("devices_sip_username_check", "(sip_username ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
	c.CheckIn(&m.Srtp,
		"off",
		"optional",
		"required",
	)
	c.CheckIn(&m.SrtpKeyex,
		"dtls",
		"sdes",
	)
	c.Index("devices_user_id", &m.UserID).Where("(user_id IS NOT NULL)")
}
