// momo:fromdb sha256=066e4894ba42cba6f0e7c41eb708bd8f3085bc3242340cc321b38e2fc3b7a85a
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// PhonePush models the table "phone_pushes".
type PhonePush struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	PhoneID     int64
	Kind        string
	Source      string    `momo:"default:'manual'::text,dbdefault"`
	Actor       string    `momo:"default:''::text,dbdefault"`
	Status      string    `momo:"default:'pending'::text,dbdefault"`
	Detail      string    `momo:"default:''::text,dbdefault"`
	Contacts    int32     `momo:"default:0,dbdefault"`
	RequestedAt time.Time `momo:"default:now(),dbdefault"`
	ClaimedAt   *time.Time
	ClaimedBy   string `momo:"default:''::text,dbdefault"`
	FinishedAt  *time.Time

	Phone *Phone `momo:"belongs_to,fk:PhoneID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *PhonePush) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Kind,
		"reboot",
		"resync",
	)
	c.CheckIn(&m.Source,
		"auto",
		"manual",
	)
	c.CheckIn(&m.Status,
		"auth_failed",
		"failed",
		"not_registered",
		"ok",
		"pending",
		"sending",
		"timeout",
	)
	c.Index("phone_pushes_claimed", &m.ClaimedAt).Where("(claimed_at IS NOT NULL)")
	c.Index("phone_pushes_open", &m.Status, &m.ID).Where("(status = ANY (ARRAY['pending'::text, 'sending'::text]))")
	c.Index("phone_pushes_phone", &m.PhoneID, &m.ID).Desc(&m.ID)
}
