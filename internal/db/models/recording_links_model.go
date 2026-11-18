// momo:fromdb sha256=42e2f0678cfbc45224bfdedb6630710f4f7b41a50e9a3cba747f0600cf496d84
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// RecordingLink models the table "recording_links".
type RecordingLink struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	TokenHash   []byte
	RecordingID int64     `momo:"index:recording_links_recording"`
	ExpiresAt   time.Time `momo:"index:recording_links_expires"`
	MaxUses     int32     `momo:"default:0,dbdefault"`
	Uses        int32     `momo:"default:0,dbdefault"`
	LastUsedAt  *time.Time
	CreatedBy   string
	ConnectorID *int64
	CreatedAt   time.Time `momo:"default:now(),dbdefault"`
	RevokedAt   *time.Time

	Recording *Recording `momo:"belongs_to,fk:RecordingID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *RecordingLink) MomoConfig(c *momo.Config) {
	c.Unique("recording_links_token_hash_key", &m.TokenHash)
	c.Check("recording_links_max_uses_check", "(max_uses >= 0)")
}
