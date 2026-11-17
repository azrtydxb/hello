// momo:fromdb sha256=48c8701e0a2b50712ede00528630a32a02e14af58cc29b308e6b929b61f62878
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// VoicemailNotification models the table "voicemail_notifications".
type VoicemailNotification struct {
	momo.Model
	ID            int64 `momo:"pk,serial"`
	MessageID     int64
	UserID        *int64
	ToEmail       string
	Attach        *bool     `momo:"default:true,dbdefault"`
	AfterAction   string    `momo:"default:'none'::text,dbdefault"`
	Status        string    `momo:"default:'pending'::text,dbdefault"`
	Attempts      int32     `momo:"default:0,dbdefault"`
	NextAttemptAt time.Time `momo:"default:now(),dbdefault"`
	ClaimedAt     *time.Time
	LastError     string    `momo:"default:''::text,dbdefault"`
	CreatedAt     time.Time `momo:"default:now(),dbdefault"`

	Message *VoicemailMessage `momo:"belongs_to,fk:MessageID,on_delete:cascade"`
	User    *User             `momo:"belongs_to,fk:UserID,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *VoicemailNotification) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.AfterAction,
		"delete",
		"mark_read",
		"none",
	)
	c.CheckIn(&m.Status,
		"failed",
		"pending",
		"sending",
		"sent",
	)
	c.Index("voicemail_notifications_due", &m.Status, &m.NextAttemptAt).Where("(status = ANY (ARRAY['pending'::text, 'sending'::text]))")
	c.IndexExpr("voicemail_notifications_recipient", "message_id", "lower(to_email)").Unique()
}
