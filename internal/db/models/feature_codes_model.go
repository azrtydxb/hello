// momo:fromdb sha256=15d7d6e970d8f73b988b2af28d55793752a6c4c6a509ef6d86328328ae8c02e3
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// FeatureCode models the table "feature_codes".
type FeatureCode struct {
	momo.Model
	Code     string `momo:"pk"`
	Action   string
	Argument string `momo:"default:''::text,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *FeatureCode) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Action,
		"announcement",
		"attended_transfer",
		"blind_transfer",
		"boss_divert",
		"call_waiting_off",
		"call_waiting_on",
		"directory",
		"dnd_off",
		"dnd_on",
		"follow_me_off",
		"follow_me_on",
		"forward_always",
		"forward_busy",
		"forward_no_answer",
		"intercom",
		"ivr",
		"page",
		"park",
		"park_retrieve",
		"pickup_directed",
		"pickup_group",
		"queue_login",
		"queue_logout",
		"queue_pause",
		"queue_unpause",
		"tc_override",
		"time_condition",
		"voicemail",
		"voicemail_broadcast",
	)
	c.Check("feature_codes_code_check", "(code ~ '^\\*[0-9]{2,4}$|^##$'::text)")
}
