// momo:fromdb sha256=18782a0044640e0d0f941ed3328846d43e87b5ca1a784f8241fce045e9679584
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// SmsOptOut models the table "sms_opt_outs".
type SmsOptOut struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	AddressID int64
	PeerKey   *string
	PeerHash  []byte
	State     string
	Source    string
	Note      string    `momo:"default:''::text,dbdefault"`
	At        time.Time `momo:"default:now(),dbdefault"`

	Address *MsgAddress `momo:"belongs_to,fk:AddressID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *SmsOptOut) MomoConfig(c *momo.Config) {
	c.Unique("sms_opt_outs_address_id_peer_hash_key", &m.AddressID, &m.PeerHash)
	c.CheckIn(&m.Source,
		"admin",
		"customer",
		"keyword",
		"provider",
	)
	c.CheckIn(&m.State,
		"in",
		"out",
	)
}
