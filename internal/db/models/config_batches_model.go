// momo:fromdb sha256=c1ecd960cf4e7a4bf245322bd67c79358e70888e5adab084c62d9d6e59c794bb
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
	"github.com/google/uuid"
)

// ConfigBatch models the table "config_batches".
type ConfigBatch struct {
	momo.Model
	ID           uuid.UUID `momo:"pk"`
	Actor        string
	Via          string `momo:"default:''::text,dbdefault"`
	BaseRevision int64
	Revision     int64
	Ops          types.JSON[json.RawMessage]
	Results      types.JSON[json.RawMessage]
	Reverts      *uuid.UUID
	CreatedAt    time.Time `momo:"default:now(),dbdefault,index:config_batches_created,sort:desc"`

	ConfigBatch *ConfigBatch `momo:"belongs_to,fk:Reverts,on_delete:set_null"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ConfigBatch) MomoConfig(c *momo.Config) {
	c.Index("config_batches_reverts", &m.Reverts).Where("(reverts IS NOT NULL)")
}
