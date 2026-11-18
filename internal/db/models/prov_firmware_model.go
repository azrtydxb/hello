// momo:fromdb sha256=90c1535036747968cca4c530f66d02a2e1c9671b55a10df4c9ffb8e14c56598a
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ProvFirmware models the table "prov_firmware".
type ProvFirmware struct {
	momo.Model `momo:"table:prov_firmware"`
	ID         int64 `momo:"pk,serial"`
	Vendor     string
	ModelGlob  string
	Version    string
	Filename   string
	ObjectKey  string
	Size       int64
	Sha256     string
	UploadedAt time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ProvFirmware) MomoConfig(c *momo.Config) {
	c.Unique("prov_firmware_object_key_key", &m.ObjectKey)
	c.Unique("prov_firmware_vendor_filename_key", &m.Vendor, &m.Filename)
	c.Check("prov_firmware_filename_check", "(filename ~ '^[A-Za-z0-9_+-][A-Za-z0-9._+-]{0,127}$'::text)")
	c.Check("prov_firmware_model_glob_check", "((length(model_glob) >= 1) AND (length(model_glob) <= 64))")
	c.Check("prov_firmware_sha256_check", "(sha256 ~ '^[0-9a-f]{64}$'::text)")
	c.Check("prov_firmware_size_check", "(size >= 0)")
	c.CheckIn(&m.Vendor,
		"fanvil",
		"generic",
		"grandstream",
		"poly",
		"snom",
		"yealink",
	)
	c.Check("prov_firmware_version_check", "((length(version) >= 1) AND (length(version) <= 64))")
}
