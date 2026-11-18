// momo:fromdb sha256=04e0467b7d49757cf5b7a9eb651e7abc92512533f56563d0a50c86ec6b6d9c24
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// TtsSetting models the table "tts_settings".
type TtsSetting struct {
	momo.Model
	ID        int16     `momo:"pk"`
	APIKeyEnc []byte    `momo:"nullable"`
	KeyHint   string    `momo:"default:''::text,dbdefault"`
	Model2    string    `momo:"column:model,default:'elevenlabs/eleven-multilingual-v2'::text,dbdefault"`
	Voice     string    `momo:"default:''::text,dbdefault"`
	UpdatedAt time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *TtsSetting) MomoConfig(c *momo.Config) {
	c.Check("tts_settings_id_check", "(id = 1)")
}
