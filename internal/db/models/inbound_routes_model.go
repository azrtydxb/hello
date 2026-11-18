// momo:fromdb sha256=23a3197ea552e02c75e3152d9bfa0f4b7daa5dfd8645202144cbf0aa46c8e0ab
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// InboundRoute models the table "inbound_routes".
type InboundRoute struct {
	momo.Model
	ID                int64 `momo:"pk,serial"`
	Position          int32
	Name              string
	DidKind           string
	Did               string `momo:"default:''::text,dbdefault"`
	TrunkID           *int64
	SipDomain         string `momo:"default:''::text,dbdefault"`
	HeaderName        string `momo:"default:''::text,dbdefault"`
	HeaderRegex       string `momo:"default:''::text,dbdefault"`
	Schedule          *types.JSON[json.RawMessage]
	CalleridTransform types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	DestinationKind   string
	Destination       string
	Enabled           *bool `momo:"default:true,dbdefault"`
	MohClassID        *int64
	MohRingback       bool `momo:"default:false,dbdefault"`

	MohClass *MohClass `momo:"belongs_to,fk:MohClassID,on_delete:set_null"`
	Trunk    *Trunk    `momo:"belongs_to,fk:TrunkID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *InboundRoute) MomoConfig(c *momo.Config) {
	c.Unique("inbound_routes_position", &m.Position)
	c.CheckIn(&m.DestinationKind,
		"announcement",
		"conference",
		"directory",
		"extension",
		"external",
		"fax",
		"internal",
		"ivr",
		"paging",
		"queue",
		"ring_group",
		"sip_uri",
		"time_condition",
		"voicemail",
		"voicemail_group",
	)
	c.Check("inbound_routes_did_check", "(length(did) <= 500)")
	c.CheckIn(&m.DidKind,
		"any",
		"exact",
		"prefix",
		"regex",
	)
	c.Check("inbound_routes_header_regex_check", "(length(header_regex) <= 500)")
}
