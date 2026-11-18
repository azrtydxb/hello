// momo:fromdb sha256=c130095dec427c8b4d6bf31dfb943a1abc000884cdb73ad1f6f996c081afb6f7
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Announcement models the table "announcements".
type Announcement struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	Name        string
	MinioObject string
	CreatedAt   time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt   time.Time `momo:"default:now(),dbdefault"`
	SourceText  *string
	VoiceID     *string
	ModelID     *string
	GeneratedAt *time.Time
	TextHash    *string
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Announcement) MomoConfig(c *momo.Config) {
	c.Unique("announcements_name_key", &m.Name)
	c.Check("announcements_name_check", "(name ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
}
