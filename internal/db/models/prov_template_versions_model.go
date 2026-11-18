// momo:fromdb sha256=6d9c0596e8beaa5324fb44981dc87968a099305aa9d2dbbc25e19a57c0d6cadb
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// ProvTemplateVersion models the table "prov_template_versions".
type ProvTemplateVersion struct {
	momo.Model
	TemplateID int64 `momo:"pk"`
	Version    int32 `momo:"pk"`
	Files      types.JSON[json.RawMessage]
	Actor      string
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`

	Template *ProvTemplate `momo:"belongs_to,fk:TemplateID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ProvTemplateVersion) MomoConfig(c *momo.Config) {
	c.Check("prov_template_versions_files_check", "(jsonb_typeof(files) = 'array'::text)")
}
