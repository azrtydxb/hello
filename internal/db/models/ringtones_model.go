// momo:fromdb sha256=2c34039f6449db089a510e1bbfd46c0eacf143b7231f9b40d6f7015f6877949c
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// Ringtone models the table "ringtones".
type Ringtone struct {
	momo.Model
	ID         int64 `momo:"pk,serial"`
	Name       string
	ObjectKey  string
	SizeBytes  int32
	Sha256     string
	SampleRate int32
	DurationMs int32
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *Ringtone) MomoConfig(c *momo.Config) {
	c.Unique("ringtones_name_key", &m.Name)
	c.Check("ringtones_duration_ms_check", "((duration_ms >= 1) AND (duration_ms <= 30000))")
	c.Check("ringtones_name_check", "(name ~ '^[a-z0-9][a-z0-9-]{0,31}$'::text)")
	c.Check("ringtones_sample_rate_check", "(sample_rate = ANY (ARRAY[8000, 16000]))")
	c.Check("ringtones_sha256_check", "(sha256 ~ '^[0-9a-f]{64}$'::text)")
	c.Check("ringtones_size_bytes_check", "((size_bytes >= 1) AND (size_bytes <= 524288))")
}
