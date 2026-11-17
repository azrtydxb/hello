// momo:fromdb sha256=ddbfbe2d979ac77cd3d9d524d17a30d79f8ce53887cd599f82c2bf9670ffe5ab
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// RecordingSetting models the table "recording_settings".
type RecordingSetting struct {
	momo.Model
	ID                 *int16    `momo:"pk,default:1,dbdefault"`
	VideoRetentionDays *int32    `momo:"default:30,dbdefault"`
	UpdatedAt          time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *RecordingSetting) MomoConfig(c *momo.Config) {
	c.Check("recording_settings_id_check", "(id = 1)")
	c.Check("recording_settings_video_retention_days_check", "((video_retention_days >= 1) AND (video_retention_days <= 3650))")
}
