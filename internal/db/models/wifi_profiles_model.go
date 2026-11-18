// momo:fromdb sha256=48e8ffe835233cecef01ac968374d4c1eef6b43ddadb1af278cc72677069e8b2
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// WifiProfile models the table "wifi_profiles".
type WifiProfile struct {
	momo.Model
	ID                int64 `momo:"pk,serial"`
	Name              string
	SSID              string
	Security          string
	PskEnc            []byte    `momo:"nullable"`
	EapMethod         string    `momo:"default:''::text,dbdefault"`
	Identity          string    `momo:"default:''::text,dbdefault"`
	AnonymousIdentity string    `momo:"default:''::text,dbdefault"`
	PasswordEnc       []byte    `momo:"nullable"`
	CaCert            string    `momo:"default:'system'::text,dbdefault"`
	Priority          int32     `momo:"default:0,dbdefault"`
	Hidden            bool      `momo:"default:false,dbdefault"`
	PreferAccount     int32     `momo:"default:0,dbdefault"`
	Verified          bool      `momo:"default:false,dbdefault"`
	CreatedAt         time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt         time.Time `momo:"default:now(),dbdefault"`
	Dot1xID           *int64

	Dot1x *Dot1xProfile `momo:"belongs_to,fk:Dot1xID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *WifiProfile) MomoConfig(c *momo.Config) {
	c.Check("wifi_profiles_anonymous_identity_check", "(length(anonymous_identity) <= 128)")
	c.CheckIn(&m.CaCert,
		"skip",
		"system",
		"tofu",
	)
	c.CheckIn(&m.EapMethod,
		"",
		"peap",
	)
	c.Check("wifi_profiles_identity_check", "(length(identity) <= 128)")
	c.Check("wifi_profiles_name_check", "((length(name) >= 1) AND (length(name) <= 64))")
	c.Check("wifi_profiles_prefer_account_check", "((prefer_account >= 0) AND (prefer_account <= 3))")
	c.Check("wifi_profiles_priority_check", "((priority >= 0) AND (priority <= 10))")
	c.CheckIn(&m.Security,
		"open",
		"owe",
		"wpa2-enterprise",
		"wpa2-psk",
		"wpa3-enterprise",
		"wpa3-sae",
	)
	c.Check("wifi_profiles_ssid_check", "((octet_length(ssid) >= 1) AND (octet_length(ssid) <= 32))")
	c.Index("wifi_profiles_dot1x", &m.Dot1xID).Where("(dot1x_id IS NOT NULL)")
	c.IndexExpr("wifi_profiles_name", "lower(name)").Unique()
}
