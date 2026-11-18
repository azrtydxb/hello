// momo:fromdb sha256=95299c61cdd2dcb387008433368489f6e0696afca0d67b0efc29f37d4216e2eb
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// DirectorySyncChange models the table "directory_sync_changes".
type DirectorySyncChange struct {
	momo.Model
	ID         int64 `momo:"pk,serial"`
	RunID      int64
	Action     string
	UserID     *int64
	ExternalID string                      `momo:"default:''::text,dbdefault"`
	Username   string                      `momo:"default:''::text,dbdefault"`
	FieldDiffs types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	Reason     string                      `momo:"default:''::text,dbdefault"`
	CreatedAt  time.Time                   `momo:"default:now(),dbdefault,index:directory_sync_changes_age"`

	Run *DirectorySyncRun `momo:"belongs_to,fk:RunID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *DirectorySyncChange) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Action,
		"adopt",
		"conflict",
		"create",
		"disable",
		"enable",
		"error",
		"link",
		"skip",
		"update",
	)
	c.Index("directory_sync_changes_run", &m.RunID, &m.ID)
}
