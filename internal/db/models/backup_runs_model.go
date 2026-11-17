// momo:fromdb sha256=775c429a0e5e95bd7baaae9f648fbc4c57634dbbe80aad79f87b3dd3711d95e5
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// BackupRun models the table "backup_runs".
type BackupRun struct {
	momo.Model
	ID           int64 `momo:"pk,serial"`
	Kind         string
	Status       string
	Attempt      *int32    `momo:"default:1,dbdefault"`
	StartedAt    time.Time `momo:"default:now(),dbdefault,index:backup_runs_started,sort:desc"`
	FinishedAt   *time.Time
	DbBytes      int64  `momo:"default:0,dbdefault"`
	ObjectCount  int32  `momo:"default:0,dbdefault"`
	ObjectBytes  int64  `momo:"default:0,dbdefault"`
	ArchiveBytes int64  `momo:"default:0,dbdefault"`
	ObjectKey    string `momo:"default:''::text,dbdefault,index:backup_runs_key"`
	ErrorClass   string `momo:"default:''::text,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *BackupRun) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Kind,
		"manual",
		"scheduled",
	)
	c.CheckIn(&m.Status,
		"failed",
		"running",
		"succeeded",
	)
}
