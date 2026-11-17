// momo:fromdb sha256=9b7dd603ae51ce82433c9e98ff6d41e396b5a3fa461ffb21c6b10569df4a053c
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// MsgChannel models the table "msg_channels".
type MsgChannel struct {
	momo.Model
	ID             int64 `momo:"pk,serial"`
	Kind           string
	Provider       string
	Name           string
	Enabled        *bool `momo:"default:true,dbdefault"`
	HookID         string
	TrunkID        *int64
	Config         types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	CredentialsEnc []byte                      `momo:"nullable"`
	CredentialFp   string                      `momo:"default:''::text,dbdefault"`
	Status         string                      `momo:"default:'ok'::text,dbdefault"`
	StatusDetail   string                      `momo:"default:''::text,dbdefault"`
	LastEventAt    *time.Time
	CreatedAt      time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt      time.Time `momo:"default:now(),dbdefault"`
	FailStreak     int32     `momo:"default:0,dbdefault"`
	AiEnabled      *bool     `momo:"default:true,dbdefault"`

	Trunk *Trunk `momo:"belongs_to,fk:TrunkID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MsgChannel) MomoConfig(c *momo.Config) {
	c.Unique("msg_channels_hook_id_key", &m.HookID)
	c.CheckIn(&m.Kind,
		"sms",
		"webchat",
		"whatsapp",
	)
	c.Check("msg_channels_name_check", "((length(name) >= 1) AND (length(name) <= 100))")
	c.Check("msg_channels_provider_check", "((length(provider) >= 1) AND (length(provider) <= 32))")
	c.CheckIn(&m.Status,
		"credentials_invalid",
		"degraded",
		"ok",
	)
	c.IndexExpr("msg_channels_name", "lower(name)").Unique()
	c.Index("msg_channels_trunk", &m.TrunkID).Where("(trunk_id IS NOT NULL)")
}
