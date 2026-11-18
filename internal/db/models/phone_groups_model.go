// momo:fromdb sha256=2937b5bfa925c1c96079abd71f4532b3961760bcd26aef67cbdab10d60e078e8
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// PhoneGroup models the table "phone_groups".
type PhoneGroup struct {
	momo.Model
	ID                      int64 `momo:"pk,serial"`
	Name                    string
	Description             string    `momo:"default:''::text,dbdefault"`
	IncludeExtensions       *bool     `momo:"default:true,dbdefault"`
	IncludeCompany          *bool     `momo:"default:true,dbdefault"`
	CreatedAt               time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt               time.Time `momo:"default:now(),dbdefault"`
	PttEnabled              *bool     `momo:"default:true,dbdefault"`
	PttAddress              string    `momo:"default:'224.0.1.116:5001'::text,dbdefault"`
	PttCodec                string    `momo:"default:'PCMU'::text,dbdefault"`
	PttDefaultChannel       *int32    `momo:"default:1,dbdefault"`
	PttAcceptBusy           bool      `momo:"default:false,dbdefault"`
	PttChannelID            *int64
	PttPriorityChannel      *int32 `momo:"default:24,dbdefault"`
	PttEmergencyChannel     *int32 `momo:"default:25,dbdefault"`
	GroupPagingEnabled      bool   `momo:"default:false,dbdefault"`
	GroupPagingDefault      *int32 `momo:"default:26,dbdefault"`
	GroupPagingPriority     *int32 `momo:"default:49,dbdefault"`
	GroupPagingEmergency    *int32 `momo:"default:50,dbdefault"`
	PagingAllowInDnd        bool   `momo:"default:false,dbdefault"`
	PagingBarge             int32  `momo:"default:0,dbdefault"`
	PagingPriorityActive    *bool  `momo:"default:true,dbdefault"`
	PagingTimeout           int32  `momo:"default:0,dbdefault"`
	IntercomBarge           bool   `momo:"default:false,dbdefault"`
	LdapEnabled             bool   `momo:"default:false,dbdefault"`
	SyslogEnabled           bool   `momo:"default:false,dbdefault"`
	SyslogLevel             string `momo:"default:'info'::text,dbdefault"`
	AccountSharing          bool   `momo:"default:false,dbdefault"`
	AccountSharingSecretEnc []byte `momo:"nullable"`
	ScepEnabled             bool   `momo:"default:false,dbdefault"`
	ScepValidityDays        *int32 `momo:"default:365,dbdefault"`
	WiredDot1xID            *int64
	LocationID              *int64
	E911Enabled             bool   `momo:"default:false,dbdefault"`
	AlarmTarget             string `momo:"default:''::text,dbdefault"`
	SideKey                 string `momo:"default:'ptt'::text,dbdefault"`

	Location   *Location         `momo:"belongs_to,fk:LocationID,on_delete:restrict"`
	PttChannel *MulticastChannel `momo:"belongs_to,fk:PttChannelID,on_delete:set_null"`
	WiredDot1x *Dot1xProfile     `momo:"belongs_to,fk:WiredDot1xID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *PhoneGroup) MomoConfig(c *momo.Config) {
	c.Check("phone_groups_alarm_target_check", "(alarm_target ~ '^[0-9A-Za-z._-]{0,64}$'::text)")
	c.Check("phone_groups_description_check", "(char_length(description) <= 255)")
	c.Check("phone_groups_group_paging_default_check", "(group_paging_default = ANY (ARRAY[26, 27, 49, 50]))")
	c.Check("phone_groups_group_paging_emergency_check", "((group_paging_emergency >= 26) AND (group_paging_emergency <= 50))")
	c.Check("phone_groups_group_paging_priority_check", "((group_paging_priority >= 26) AND (group_paging_priority <= 50))")
	c.Check("phone_groups_name_check", "((char_length(name) >= 1) AND (char_length(name) <= 100))")
	c.Check("phone_groups_paging_barge_check", "((paging_barge >= 0) AND (paging_barge <= 10))")
	c.Check("phone_groups_paging_timeout_check", "((paging_timeout >= 0) AND (paging_timeout <= 3600))")
	c.CheckIn(&m.PttCodec,
		"G722",
		"PCMU",
	)
	c.Check("phone_groups_ptt_default_channel_check", "((ptt_default_channel >= 1) AND (ptt_default_channel <= 25))")
	c.Check("phone_groups_ptt_emergency_channel_check", "((ptt_emergency_channel >= 1) AND (ptt_emergency_channel <= 25))")
	c.Check("phone_groups_ptt_priority_channel_check", "((ptt_priority_channel >= 1) AND (ptt_priority_channel <= 25))")
	c.Check("phone_groups_scep_validity_days_check", "((scep_validity_days >= 30) AND (scep_validity_days <= 1825))")
	c.CheckIn(&m.SideKey,
		"alarm",
		"none",
		"ptt",
	)
	c.CheckIn(&m.SyslogLevel,
		"debug",
		"error",
		"info",
		"warning",
	)
	c.Index("phone_groups_location", &m.LocationID).Where("(location_id IS NOT NULL)")
	c.IndexExpr("phone_groups_name", "lower(name)").Unique()
}
