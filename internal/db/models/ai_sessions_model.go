// momo:fromdb sha256=8b6cbb64d4442282df75fbd733e0a632d518994979779821671846c7949dccc8
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
	"github.com/google/uuid"
)

// AiSession models the table "ai_sessions".
type AiSession struct {
	momo.Model
	ID           uuid.UUID `momo:"pk"`
	Owner        int64
	Title        string    `momo:"default:''::text,dbdefault"`
	CreatedAt    time.Time `momo:"default:now(),dbdefault"`
	LastActiveAt time.Time `momo:"default:now(),dbdefault"`

	User *User `momo:"belongs_to,fk:Owner,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *AiSession) MomoConfig(c *momo.Config) {
	c.Index("ai_sessions_owner", &m.Owner, &m.LastActiveAt).Desc(&m.LastActiveAt)
}
