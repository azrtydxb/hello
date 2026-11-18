// momo:fromdb sha256=28ed82a3f7255e499de2bc1100d2bc91ba51e8ebeab919321479f73325639b9b
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/schema"
	"github.com/azrtydxb/momo/types"
)

// Phone models the table "phones".
type Phone struct {
	momo.Model
	ID                  int64 `momo:"pk,serial"`
	Mac                 string
	Serial              *string
	Vendor              string
	Model2              string `momo:"column:model"`
	Label               string `momo:"default:''::text,dbdefault"`
	DeviceID            *int64
	TemplateID          *int64
	Blf                 types.JSON[json.RawMessage] `momo:"default:'[]'::jsonb,dbdefault"`
	Enabled             *bool                       `momo:"default:true,dbdefault"`
	TokenHash           []byte
	TokenEnc            []byte
	PrevTokenHash       []byte `momo:"nullable"`
	PrevTokenExpires    *time.Time
	TokenExposed        bool  `momo:"default:false,dbdefault"`
	UaMismatch          bool  `momo:"default:false,dbdefault"`
	BootArmed           *bool `momo:"default:true,dbdefault"`
	BootReclaimed       bool  `momo:"default:false,dbdefault"`
	AdminPasswordEnc    []byte
	RedirectStatus      types.JSON[json.RawMessage] `momo:"dbdefault"`
	FirstFetchAt        *time.Time
	LastFetchAt         *time.Time
	LastFetchIP         *string `momo:"type:inet"`
	LastFetchUa         *string
	LastFetchFile       *string
	FirmwareSeen        *string
	CreatedAt           time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt           time.Time `momo:"default:now(),dbdefault"`
	BootHandoffAt       *time.Time
	BootHandoffIP       *string `momo:"type:inet"`
	GroupID             *int64
	SyncHash            *string
	PttChannelID        *int64
	LdapDn              *string
	LdapPasswordEnc     []byte `momo:"nullable"`
	LdapPrevPasswordEnc []byte `momo:"nullable"`
	LdapRotatedAt       *time.Time
	LdapLastBindAt      *time.Time
	SharingRole         string `momo:"default:'host'::text,dbdefault"`
	LocationID          *int64
	SideKey             *string

	Device     *Device           `momo:"belongs_to,fk:DeviceID,on_delete:restrict"`
	Group      *PhoneGroup       `momo:"belongs_to,fk:GroupID,on_delete:set_null"`
	Location   *Location         `momo:"belongs_to,fk:LocationID,on_delete:restrict"`
	PttChannel *MulticastChannel `momo:"belongs_to,fk:PttChannelID,on_delete:set_null"`
	Template   *ProvTemplate     `momo:"belongs_to,fk:TemplateID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Phone) MomoConfig(c *momo.Config) {
	c.Default(&m.RedirectStatus, "'{\"state\": \"not_configured\"}'::jsonb")
	c.Unique("phones_device_id_key", &m.DeviceID)
	c.Unique("phones_mac_key", &m.Mac)
	c.Unique("phones_prev_token_hash_key", &m.PrevTokenHash)
	c.Unique("phones_token_hash_key", &m.TokenHash)
	c.Check("phones_blf_check", "(jsonb_typeof(blf) = 'array'::text)")
	c.Check("phones_check", "((device_id IS NOT NULL) OR (NOT enabled))")
	c.Check("phones_check1", "((prev_token_hash IS NULL) = (prev_token_expires IS NULL))")
	c.Check("phones_mac_check", "(mac ~ '^[0-9a-f]{12}$'::text)")
	c.Check("phones_model_check", "((length(model) >= 1) AND (length(model) <= 64))")
	c.Check("phones_prev_token_hash_check", "(length(prev_token_hash) = 32)")
	c.Check("phones_redirect_status_check", "(jsonb_typeof(redirect_status) = 'object'::text)")
	c.Check("phones_serial_check", "((length(serial) >= 1) AND (length(serial) <= 64))")
	c.CheckIn(&m.SharingRole,
		"guest",
		"host",
	)
	c.CheckIn(&m.SideKey,
		"alarm",
		"none",
		"ptt",
	)
	c.Check("phones_token_hash_check", "(length(token_hash) = 32)")
	c.CheckIn(&m.Vendor,
		"fanvil",
		"generic",
		"grandstream",
		"poly",
		"snom",
		"yealink",
	)
	c.Index("phones_location", &m.LocationID).Where("(location_id IS NOT NULL)")
	c.Function(schema.Function{Name: "scep_revoke_removed_phone", Returns: "trigger", Language: "plpgsql", Volatility: "VOLATILE", Body: "\nBEGIN\n    UPDATE scep_certificates SET revoked_at = now(), revoke_reason = 'phone_removed'\n    WHERE phone_id = OLD.id AND revoked_at IS NULL;\n    RETURN OLD;\nEND;\n"})
	c.Trigger(schema.Trigger{Table: "phones", Name: "phones_scep_revoke", Timing: "BEFORE", Events: []string{"DELETE"}, ForEachRow: true, Function: "scep_revoke_removed_phone()"})
}
