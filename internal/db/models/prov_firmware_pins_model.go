// momo:fromdb sha256=9fc3c17ce8e9c441a68f8ef690c3f6c402cb79261fc74f6b586f42a43b009f10
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// ProvFirmwarePin models the table "prov_firmware_pins".
type ProvFirmwarePin struct {
	momo.Model
	Vendor      string `momo:"pk"`
	ModelGlob   string `momo:"pk"`
	FirmwareID  int64
	UpgradeHour *int16

	Firmware *ProvFirmware `momo:"belongs_to,fk:FirmwareID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ProvFirmwarePin) MomoConfig(c *momo.Config) {
	c.Check("prov_firmware_pins_model_glob_check", "((length(model_glob) >= 1) AND (length(model_glob) <= 64))")
	c.Check("prov_firmware_pins_upgrade_hour_check", "((upgrade_hour >= 0) AND (upgrade_hour <= 23))")
	c.CheckIn(&m.Vendor,
		"fanvil",
		"generic",
		"grandstream",
		"poly",
		"snom",
		"yealink",
	)
}
