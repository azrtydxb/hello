// momo:fromdb sha256=832e591fe757ef691d9cee9f517bfa6d7255858f40e1cf394bd51d40cac1e248
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// MsgAddress models the table "msg_addresses".
type MsgAddress struct {
	momo.Model
	ID                    int64 `momo:"pk,serial"`
	ChannelID             int64
	Address               string
	AddressKey            string    `momo:"index:msg_addresses_key"`
	AddressSuffix         string    `momo:"default:''::text,dbdefault"`
	Label                 string    `momo:"default:''::text,dbdefault"`
	RouteKind             string    `momo:"default:'inbox'::text,dbdefault"`
	RouteTarget           string    `momo:"default:''::text,dbdefault"`
	OptoutKeywords        []string  `momo:"dbdefault"`
	HelpText              string    `momo:"default:''::text,dbdefault"`
	OptoutText            string    `momo:"default:''::text,dbdefault"`
	Enabled               *bool     `momo:"default:true,dbdefault"`
	CreatedAt             time.Time `momo:"default:now(),dbdefault"`
	SendsPerMinute        *int32    `momo:"default:60,dbdefault"`
	DeskPhoneDelivery     bool      `momo:"default:false,dbdefault"`
	OwnerExtensionID      *int64
	Schedule              types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	AfterHoursAction      string                      `momo:"default:'none'::text,dbdefault"`
	AfterHoursText        string                      `momo:"default:''::text,dbdefault"`
	AfterHoursRouteKind   string                      `momo:"default:'inbox'::text,dbdefault"`
	AfterHoursRouteTarget string                      `momo:"default:''::text,dbdefault"`
	FirstContactText      string                      `momo:"default:''::text,dbdefault"`
	AiMode                string                      `momo:"default:'off'::text,dbdefault"`
	AiRedact              *bool                       `momo:"default:true,dbdefault"`
	AiDisclosure          *bool                       `momo:"default:true,dbdefault"`
	AiDisclosureText      string                      `momo:"default:'This is an automated reply.'::text,dbdefault"`
	AiHoldingText         string                      `momo:"default:''::text,dbdefault"`
	AiMaxReplies          *int32                      `momo:"default:3,dbdefault"`
	AiHourlyCap           *int32                      `momo:"default:60,dbdefault"`
	AiMinConfidence       *float32                    `momo:"default:0.8,dbdefault"`
	AiAutoIntents         []string                    `momo:"default:'{}'::text[],dbdefault"`
	AiHandoffWords        []string                    `momo:"dbdefault"`

	Channel        *MsgChannel `momo:"belongs_to,fk:ChannelID,on_delete:cascade"`
	OwnerExtension *Extension  `momo:"belongs_to,fk:OwnerExtensionID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MsgAddress) MomoConfig(c *momo.Config) {
	c.Default(&m.OptoutKeywords, "'{stop,stopall,unsubscribe,cancel,end,quit}'::text[]")
	c.Default(&m.AiHandoffWords, "'{agent,human,person,representative}'::text[]")
	c.Unique("msg_addresses_channel_id_address_key_key", &m.ChannelID, &m.AddressKey)
	c.Check("msg_addresses_address_check", "((length(address) >= 2) AND (length(address) <= 64))")
	c.CheckIn(&m.AfterHoursAction,
		"none",
		"reply",
		"route",
	)
	c.CheckIn(&m.AfterHoursRouteKind,
		"extension",
		"inbox",
		"queue",
		"ring_group",
		"user",
	)
	c.Check("msg_addresses_after_hours_text_check", "(length(after_hours_text) <= 480)")
	c.Check("msg_addresses_ai_disclosure_text_check", "(length(ai_disclosure_text) <= 160)")
	c.Check("msg_addresses_ai_holding_text_check", "(length(ai_holding_text) <= 480)")
	c.Check("msg_addresses_ai_hourly_cap_check", "((ai_hourly_cap >= 1) AND (ai_hourly_cap <= 1000))")
	c.Check("msg_addresses_ai_max_replies_check", "((ai_max_replies >= 1) AND (ai_max_replies <= 10))")
	c.Check("msg_addresses_ai_min_confidence_check", "((ai_min_confidence >= (0)::double precision) AND (ai_min_confidence <= (1)::double precision))")
	c.CheckIn(&m.AiMode,
		"auto",
		"draft",
		"off",
		"triage",
	)
	c.Check("msg_addresses_first_contact_text_check", "(length(first_contact_text) <= 480)")
	c.Check("msg_addresses_help_text_check", "(length(help_text) <= 480)")
	c.Check("msg_addresses_label_check", "(length(label) <= 100)")
	c.Check("msg_addresses_optout_text_check", "(length(optout_text) <= 480)")
	c.CheckIn(&m.RouteKind,
		"ai_triage",
		"extension",
		"inbox",
		"queue",
		"ring_group",
		"user",
	)
	c.Check("msg_addresses_sends_per_minute_check", "((sends_per_minute >= 1) AND (sends_per_minute <= 600))")
	c.Index("msg_addresses_owner", &m.OwnerExtensionID).Where("(owner_extension_id IS NOT NULL)")
	c.Index("msg_addresses_suffix", &m.AddressSuffix).Where("(address_suffix <> ''::text)")
}
