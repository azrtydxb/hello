// momo:fromdb sha256=945f5302be14bf9252fc23af71968bbdcdfec26a62c0e7de6b63ff80aa7783bf
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// ProvTemplate models the table "prov_templates".
type ProvTemplate struct {
	momo.Model
	ID         int64 `momo:"pk,serial"`
	Vendor     string
	ModelGlob  string
	Priority   int32 `momo:"default:0,dbdefault"`
	Name       string
	Files      types.JSON[json.RawMessage]
	BuiltinRef *string
	Version    *int32    `momo:"default:1,dbdefault"`
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt  time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ProvTemplate) MomoConfig(c *momo.Config) {
	c.Check("prov_templates_files_check", "(jsonb_typeof(files) = 'array'::text)")
	c.Check("prov_templates_model_glob_check", "((length(model_glob) >= 1) AND (length(model_glob) <= 64))")
	c.Check("prov_templates_name_check", "((length(name) >= 1) AND (length(name) <= 128))")
	c.CheckIn(&m.Vendor,
		"fanvil",
		"generic",
		"grandstream",
		"poly",
		"snom",
		"yealink",
	)
	c.Check("prov_templates_version_check", "(version >= 1)")
}
