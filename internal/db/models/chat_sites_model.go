// momo:fromdb sha256=67e989e8c487af2c93f1ca33b60650a6060cbd203cfb1a1cf448607ce02a2331
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ChatSite models the table "chat_sites".
type ChatSite struct {
	momo.Model
	ID                 int64 `momo:"pk,serial"`
	ChannelID          int64
	AddressID          int64
	SiteKey            string
	AllowedOrigins     []string `momo:"default:'{}'::text[],dbdefault"`
	Greeting           string   `momo:"default:''::text,dbdefault"`
	Accent             string   `momo:"default:'#2563eb'::text,dbdefault"`
	Position           string   `momo:"default:'right'::text,dbdefault"`
	Collect            string   `momo:"default:'off'::text,dbdefault"`
	OfflineMode        string   `momo:"default:'collect'::text,dbdefault"`
	PrivacyURL         string   `momo:"default:''::text,dbdefault"`
	Challenge          string   `momo:"default:'pow'::text,dbdefault"`
	PowBits            *int32   `momo:"default:16,dbdefault"`
	TurnstileSiteKey   string   `momo:"default:''::text,dbdefault"`
	TurnstileSecretEnc []byte   `momo:"nullable"`
	StripLinks         bool     `momo:"default:false,dbdefault"`
	MaxOpenUnassigned  *int32   `momo:"default:200,dbdefault"`
	Transcripts        bool     `momo:"default:false,dbdefault"`
	RetentionDays      *int32
	CreatedAt          time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt          time.Time `momo:"default:now(),dbdefault"`
	Attachments        bool      `momo:"default:false,dbdefault"`
	LogoType           string    `momo:"default:''::text,dbdefault"`
	LogoUpdatedAt      *time.Time

	Address *MsgAddress `momo:"belongs_to,fk:AddressID,on_delete:cascade"`
	Channel *MsgChannel `momo:"belongs_to,fk:ChannelID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ChatSite) MomoConfig(c *momo.Config) {
	c.Unique("chat_sites_address_id_key", &m.AddressID)
	c.Unique("chat_sites_channel_id_key", &m.ChannelID)
	c.Unique("chat_sites_site_key_key", &m.SiteKey)
	c.Check("chat_sites_accent_check", "(accent ~ '^#[0-9a-fA-F]{6}$'::text)")
	c.CheckIn(&m.Challenge,
		"none",
		"pow",
		"turnstile",
	)
	c.CheckIn(&m.Collect,
		"off",
		"optional",
		"required",
	)
	c.Check("chat_sites_greeting_check", "(length(greeting) <= 500)")
	c.CheckIn(&m.LogoType,
		"",
		"image/gif",
		"image/jpeg",
		"image/png",
		"image/webp",
	)
	c.Check("chat_sites_max_open_unassigned_check", "((max_open_unassigned >= 1) AND (max_open_unassigned <= 100000))")
	c.CheckIn(&m.OfflineMode,
		"collect",
		"hidden",
	)
	c.CheckIn(&m.Position,
		"left",
		"right",
	)
	c.Check("chat_sites_pow_bits_check", "((pow_bits >= 14) AND (pow_bits <= 22))")
	c.Check("chat_sites_privacy_url_check", "(length(privacy_url) <= 500)")
	c.Check("chat_sites_retention_days_check", "((retention_days >= 1) AND (retention_days <= 3650))")
	c.Check("chat_sites_site_key_check", "(site_key ~ '^[0-9a-f]{24}$'::text)")
	c.Check("chat_sites_turnstile_site_key_check", "(length(turnstile_site_key) <= 100)")
}
