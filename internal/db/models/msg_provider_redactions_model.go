// momo:fromdb sha256=7b14592fa5e7fa5c86c0fe1f4b7cdffcb37ba0682bbbc983d0653f119e4001c1
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// MsgProviderRedaction models the table "msg_provider_redactions".
type MsgProviderRedaction struct {
	momo.Model
	ID                int64 `momo:"pk,serial"`
	ChannelID         int64
	Provider          string
	ProviderMessageID string
	Batch             string    `momo:"index:msg_provider_redactions_batch"`
	State             string    `momo:"default:'pending'::text,dbdefault"`
	Attempts          int32     `momo:"default:0,dbdefault"`
	NextAttemptAt     time.Time `momo:"default:now(),dbdefault"`
	ClaimedUntil      *time.Time
	LastError         string    `momo:"default:''::text,dbdefault"`
	CreatedAt         time.Time `momo:"default:now(),dbdefault"`
	FinishedAt        *time.Time

	Channel *MsgChannel `momo:"belongs_to,fk:ChannelID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MsgProviderRedaction) MomoConfig(c *momo.Config) {
	c.Unique("msg_provider_redactions_channel_id_provider_message_id_key", &m.ChannelID, &m.ProviderMessageID)
	c.Check("msg_provider_redactions_last_error_check", "(length(last_error) <= 200)")
	c.Check("msg_provider_redactions_provider_check", "((length(provider) >= 1) AND (length(provider) <= 32))")
	c.Check("msg_provider_redactions_provider_message_id_check", "((length(provider_message_id) >= 1) AND (length(provider_message_id) <= 128))")
	c.CheckIn(&m.State,
		"done",
		"failed",
		"pending",
		"unsupported",
	)
	c.Index("msg_provider_redactions_due", &m.NextAttemptAt).Where("(state = 'pending'::text)")
	c.Index("msg_provider_redactions_finished", &m.FinishedAt).Where("(finished_at IS NOT NULL)")
}
