// momo:fromdb sha256=910c164d6d736a62bb9356f9395e2615b3f9f2f04169bc0b8b9c104cd8af6826
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// VoicemailMessage models the table "voicemail_messages".
type VoicemailMessage struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	BoxID       int64
	MinioObject string
	Caller      string
	DurationMs  int64
	Heard       bool      `momo:"default:false,dbdefault"`
	EmailStatus string    `momo:"default:'pending'::text,dbdefault"`
	CreatedAt   time.Time `momo:"default:now(),dbdefault"`
	CallerName  string    `momo:"default:''::text,dbdefault"`

	Box *VoicemailBox `momo:"belongs_to,fk:BoxID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *VoicemailMessage) MomoConfig(c *momo.Config) {
	c.Index("voicemail_messages_box", &m.BoxID, &m.CreatedAt).Desc(&m.CreatedAt)
}
