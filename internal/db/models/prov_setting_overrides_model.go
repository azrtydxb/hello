// momo:fromdb sha256=00e37fc82f2238476d7d4ac1162c5fb653eb622fc8615633ecaf1d7324e0fb1d
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ProvSettingOverride models the table "prov_setting_overrides".
type ProvSettingOverride struct {
	momo.Model
	ID        int64 `momo:"pk,serial"`
	GroupID   *int64
	PhoneID   *int64
	Key       string
	Value     string    `momo:"default:''::text,dbdefault"`
	ValueEnc  []byte    `momo:"nullable"`
	Secret    bool      `momo:"default:false,dbdefault"`
	CreatedAt time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt time.Time `momo:"default:now(),dbdefault"`

	Group *PhoneGroup `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
	Phone *Phone      `momo:"belongs_to,fk:PhoneID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ProvSettingOverride) MomoConfig(c *momo.Config) {
	c.Check("prov_setting_overrides_check", "((group_id IS NULL) <> (phone_id IS NULL))")
	c.Check("prov_setting_overrides_check1", "(secret = (value_enc IS NOT NULL))")
	c.Check("prov_setting_overrides_check2", "((NOT secret) OR (value = ''::text))")
	c.Check("prov_setting_overrides_key_check", "(key ~ '^[A-Za-z0-9_.]{1,128}$'::text)")
	c.Check("prov_setting_overrides_value_check", "(length(value) <= 1024)")
	c.Unique("prov_setting_overrides_group_key", &m.GroupID, &m.Key).Where("(group_id IS NOT NULL)")
	c.Unique("prov_setting_overrides_phone_key", &m.PhoneID, &m.Key).Where("(phone_id IS NOT NULL)")
}
