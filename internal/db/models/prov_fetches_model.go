// momo:fromdb sha256=8bad6ea1cdc5158a31b0d31d241b1cdf2878b2b957f2f18b3205a3691fb06cd8
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ProvFetch models the table "prov_fetches".
type ProvFetch struct {
	momo.Model
	ID           int64     `momo:"pk,serial"`
	At           time.Time `momo:"default:now(),dbdefault,index:prov_fetches_at"`
	PhoneID      *int64
	MacClaimed   string  `momo:"default:''::text,dbdefault"`
	IP           *string `momo:"type:inet"`
	UserAgent    string  `momo:"default:''::text,dbdefault"`
	PathRedacted string
	Kind         string
	Result       string
	Status       int32
	Bytes        int64 `momo:"default:0,dbdefault"`
	UaMismatch   bool  `momo:"default:false,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ProvFetch) MomoConfig(c *momo.Config) {
	c.Check("prov_fetches_bytes_check", "(bytes >= 0)")
	c.CheckIn(&m.Kind,
		"boot",
		"ca",
		"common",
		"device",
		"firmware",
		"master",
		"other",
		"phonebook",
		"ringtone",
		"scep",
		"upload",
	)
	c.Check("prov_fetches_path_redacted_check", "(path_redacted !~ '^/p/[a-z2-7]{52}(/|$)'::text)")
	c.CheckIn(&m.Result,
		"boot_denied",
		"boot_handoff",
		"boot_reclaim",
		"boot_served",
		"mac_mismatch",
		"no_template",
		"not_allowlisted",
		"not_found",
		"not_modified",
		"plain_http",
		"rate_limited",
		"render_error",
		"scep_issued",
		"scep_refused",
		"served",
		"unavailable",
		"unknown_token",
		"upload_discarded",
	)
	c.Check("prov_fetches_user_agent_check", "(octet_length(user_agent) <= 256)")
	c.Index("prov_fetches_phone_at", &m.PhoneID, &m.At).Desc(&m.At)
}
