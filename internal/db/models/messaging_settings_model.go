// momo:fromdb sha256=922179e49f2d75fceea3f903d447bd274eec024b571413e364b15d1cdd4009f5
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// MessagingSetting models the table "messaging_settings".
type MessagingSetting struct {
	momo.Model
	ID                      *bool    `momo:"pk,default:true,dbdefault"`
	RetentionSmsDays        *int32   `momo:"default:365,dbdefault"`
	RetentionChatDays       *int32   `momo:"default:90,dbdefault"`
	ReopenDays              *int32   `momo:"default:7,dbdefault"`
	EmailNotify             *bool    `momo:"default:true,dbdefault"`
	FirstContact            string   `momo:"default:'off'::text,dbdefault"`
	FirstContactUserHour    *int32   `momo:"default:20,dbdefault"`
	FirstContactAddressDay  *int32   `momo:"default:200,dbdefault"`
	AiEnabled               *bool    `momo:"default:true,dbdefault"`
	AiAutoEnabled           *bool    `momo:"default:true,dbdefault"`
	AiBusinessName          string   `momo:"default:''::text,dbdefault"`
	AiIntents               []string `momo:"dbdefault"`
	AiSensitiveTopics       []string `momo:"dbdefault"`
	RetentionMediaDays      *int32   `momo:"default:90,dbdefault"`
	SLAFirstResponseSeconds *int32   `momo:"default:900,dbdefault"`
	SLAResolutionSeconds    *int32   `momo:"default:86400,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MessagingSetting) MomoConfig(c *momo.Config) {
	c.Default(&m.AiIntents, "'{question,sales,support,billing,appointment,complaint}'::text[]")
	c.Default(&m.AiSensitiveTopics, "'{medical,legal,emergency,self-harm,harassment,\"payment dispute\",\"personal data\"}'::text[]")
	c.Check("messaging_settings_ai_business_name_check", "(length(ai_business_name) <= 100)")
	c.Check("messaging_settings_first_contact_address_day_check", "((first_contact_address_day >= 1) AND (first_contact_address_day <= 10000))")
	c.CheckIn(&m.FirstContact,
		"attested",
		"off",
	)
	c.Check("messaging_settings_first_contact_user_hour_check", "((first_contact_user_hour >= 1) AND (first_contact_user_hour <= 1000))")
	c.Check("messaging_settings_id_check", "id")
	c.Check("messaging_settings_reopen_days_check", "((reopen_days >= 0) AND (reopen_days <= 365))")
	c.Check("messaging_settings_retention_chat_days_check", "((retention_chat_days >= 1) AND (retention_chat_days <= 3650))")
	c.Check("messaging_settings_retention_media_days_check", "((retention_media_days >= 1) AND (retention_media_days <= 3650))")
	c.Check("messaging_settings_retention_sms_days_check", "((retention_sms_days >= 1) AND (retention_sms_days <= 3650))")
	c.Check("messaging_settings_sla_first_response_seconds_check", "((sla_first_response_seconds >= 0) AND (sla_first_response_seconds <= 604800))")
	c.Check("messaging_settings_sla_resolution_seconds_check", "((sla_resolution_seconds >= 0) AND (sla_resolution_seconds <= 2592000))")
}
