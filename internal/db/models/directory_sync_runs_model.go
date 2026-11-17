// momo:fromdb sha256=fdfff5ae130cf011fe02a87142dc032d3cd21420247961111f790b27e5c14173
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// DirectorySyncRun models the table "directory_sync_runs".
type DirectorySyncRun struct {
	momo.Model
	ID          int64 `momo:"pk,serial"`
	ConnectorID int64
	Kind        string
	Status      string
	RequestedBy string    `momo:"default:''::text,dbdefault"`
	CreatedAt   time.Time `momo:"default:now(),dbdefault"`
	StartedAt   *time.Time
	FinishedAt  *time.Time
	Replica     string                      `momo:"default:''::text,dbdefault"`
	Counts      types.JSON[json.RawMessage] `momo:"default:'{}'::jsonb,dbdefault"`
	DiffHash    string                      `momo:"default:''::text,dbdefault"`
	DeltaToken  string                      `momo:"default:''::text,dbdefault"`
	Incremental bool                        `momo:"default:false,dbdefault"`
	FullReason  string                      `momo:"default:''::text,dbdefault"`
	ErrorCode   string                      `momo:"default:''::text,dbdefault"`
	ErrorDetail string                      `momo:"default:''::text,dbdefault"`

	Connector *DirectoryConnector `momo:"belongs_to,fk:ConnectorID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *DirectorySyncRun) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Kind,
		"apply",
		"preview",
		"scheduled",
		"scim",
	)
	c.CheckIn(&m.Status,
		"failed",
		"needs_confirmation",
		"preview",
		"queued",
		"running",
		"stale",
		"succeeded",
	)
	c.Index("directory_sync_runs_connector", &m.ConnectorID, &m.ID).Desc(&m.ID)
	c.Index("directory_sync_runs_open", &m.ConnectorID, &m.Status).Where("(status = ANY (ARRAY['queued'::text, 'running'::text]))")
}
