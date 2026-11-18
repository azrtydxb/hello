// momo:fromdb sha256=15a7419147ef556510dcef39080f44b9e3798f4c1ff08a4f573986c068f4720e
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// SchemaInfo models the table "schema_info".
type SchemaInfo struct {
	momo.Model         `momo:"table:schema_info"`
	ID                 *bool     `momo:"pk,default:true,dbdefault"`
	ConfigRevision     int64     `momo:"default:0,dbdefault"`
	CreatedAt          time.Time `momo:"default:now(),dbdefault"`
	ProvDriftCheckedAt *time.Time
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *SchemaInfo) MomoConfig(c *momo.Config) {
	c.Check("schema_info_id_check", "id")
}
