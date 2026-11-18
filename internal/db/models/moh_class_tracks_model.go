// momo:fromdb sha256=937ae308be19b035b8058ce4d7805a457392d98ec413212af87f9a67b538ac81
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// MohClassTrack models the table "moh_class_tracks".
type MohClassTrack struct {
	momo.Model
	ClassID        int64 `momo:"pk"`
	Position       int32 `momo:"pk"`
	AnnouncementID int64

	Announcement *Announcement `momo:"belongs_to,fk:AnnouncementID,on_delete:cascade"`
	Class        *MohClass     `momo:"belongs_to,fk:ClassID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *MohClassTrack) MomoConfig(c *momo.Config) {
	c.Check("moh_class_tracks_position_check", "(\"position\" >= 0)")
}
