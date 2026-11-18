// momo:fromdb sha256=bb58a2c4562e115b70162916351662f076805784d6729344dd2c1578d39562cc
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// ProvFirmwareMinimum models the table "prov_firmware_minimums".
type ProvFirmwareMinimum struct {
	momo.Model
	Vendor      string `momo:"pk"`
	ModelGlob   string `momo:"pk"`
	MinVersion  string `momo:"default:''::text,dbdefault"`
	Recommended string `momo:"default:''::text,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ProvFirmwareMinimum) MomoConfig(c *momo.Config) {
	c.Check("prov_firmware_minimums_min_version_check", "(length(min_version) <= 64)")
	c.Check("prov_firmware_minimums_model_glob_check", "((length(model_glob) >= 1) AND (length(model_glob) <= 64))")
	c.Check("prov_firmware_minimums_recommended_check", "(length(recommended) <= 64)")
	c.CheckIn(&m.Vendor,
		"fanvil",
		"generic",
		"grandstream",
		"poly",
		"snom",
		"yealink",
	)
}
