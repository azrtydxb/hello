// momo:fromdb sha256=223e8f134c8833a885148b18b139feda75e12c0d23ff3714e6eaf4328c00258f
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// Trunk models the table "trunks".
type Trunk struct {
	momo.Model
	ID                  int64 `momo:"pk,serial"`
	Name                string
	Mode                string
	Username            string                      `momo:"default:''::text,dbdefault"`
	PasswordEnc         []byte                      `momo:"nullable"`
	Realm               string                      `momo:"default:''::text,dbdefault"`
	FromDomain          string                      `momo:"default:''::text,dbdefault"`
	RegisterExpires     *int32                      `momo:"default:3600,dbdefault"`
	OptionsInterval     *int32                      `momo:"default:30,dbdefault"`
	SourceCidrs         []string                    `momo:"type:cidr[],default:'{}'::cidr[],dbdefault"`
	MaxCalls            int32                       `momo:"default:0,dbdefault"`
	DefaultCallerID     string                      `momo:"default:''::text,dbdefault"`
	Enabled             *bool                       `momo:"default:true,dbdefault"`
	CreatedAt           time.Time                   `momo:"default:now(),dbdefault"`
	UpdatedAt           time.Time                   `momo:"default:now(),dbdefault"`
	InternalDialing     types.JSON[json.RawMessage] `momo:"dbdefault"`
	Codecs              types.JSON[json.RawMessage] `momo:"dbdefault"`
	Messaging           bool                        `momo:"default:false,dbdefault"`
	SessionTimers       string                      `momo:"default:'default'::text,dbdefault"`
	RejectAnonymous     bool                        `momo:"default:false,dbdefault"`
	AllowedCountryCodes []string                    `momo:"default:'{}'::text[],dbdefault"`
	UnknownCallerAction string                      `momo:"default:'block'::text,dbdefault"`
	RejectCode          *int32                      `momo:"default:603,dbdefault"`
	AnonymousPolicy     string                      `momo:"default:'honour'::text,dbdefault"`
	AssertedIdentity    string                      `momo:"default:''::text,dbdefault"`
	IdentityHeader      string                      `momo:"default:'pai'::text,dbdefault"`
	FaxMode             string                      `momo:"default:'passthrough'::text,dbdefault"`
	Geolocation         bool                        `momo:"default:false,dbdefault"`
	Srtp                string                      `momo:"default:'off'::text,dbdefault"`
	Profile             string                      `momo:"default:'generic'::text,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Trunk) MomoConfig(c *momo.Config) {
	c.Default(&m.InternalDialing, "'{\"mode\": \"off\"}'::jsonb")
	c.Default(&m.Codecs, "'[\"PCMU\", \"PCMA\"]'::jsonb")
	c.Unique("trunks_name_key", &m.Name)
	c.CheckIn(&m.AnonymousPolicy,
		"always",
		"honour",
	)
	c.Check("trunks_codecs_check", "((jsonb_typeof(codecs) = 'array'::text) AND ((jsonb_array_length(codecs) >= 1) AND (jsonb_array_length(codecs) <= 4)))")
	c.CheckIn(&m.FaxMode,
		"off",
		"passthrough",
		"t38",
	)
	c.CheckIn(&m.IdentityHeader,
		"pai",
		"ppi",
	)
	c.Check("trunks_internal_dialing_check", "((internal_dialing ->> 'mode'::text) = ANY (ARRAY['off'::text, 'patterns'::text, 'all'::text]))")
	c.Check("trunks_max_calls_check", "(max_calls >= 0)")
	c.CheckIn(&m.Mode,
		"ip",
		"registration",
	)
	c.Check("trunks_name_check", "(name ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
	c.Check("trunks_options_interval_check", "((options_interval >= 5) AND (options_interval <= 3600))")
	c.CheckIn(&m.Profile,
		"generic",
		"sbc-peer",
	)
	c.Check("trunks_register_expires_check", "((register_expires >= 60) AND (register_expires <= 86400))")
	c.Check("trunks_reject_code_check", "(reject_code = ANY (ARRAY[603, 486]))")
	c.CheckIn(&m.SessionTimers,
		"default",
		"off",
		"on",
	)
	c.CheckIn(&m.Srtp,
		"off",
		"optional",
		"required",
	)
	c.CheckIn(&m.UnknownCallerAction,
		"allow",
		"block",
	)
}
