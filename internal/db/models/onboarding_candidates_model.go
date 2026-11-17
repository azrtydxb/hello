// momo:fromdb sha256=d3d8a259d5d20cd02876c7e9d8801d7e1f573c75fa636dd78f7041bd39eec968
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// OnboardingCandidate models the table "onboarding_candidates".
type OnboardingCandidate struct {
	momo.Model
	Mac           string    `momo:"pk"`
	Vendor        string    `momo:"default:'generic'::text,dbdefault"`
	Model2        string    `momo:"column:model,default:''::text,dbdefault"`
	Firmware      string    `momo:"default:''::text,dbdefault"`
	SourceIP      *string   `momo:"type:inet"`
	RequestedPath string    `momo:"default:''::text,dbdefault"`
	FirstSeen     time.Time `momo:"default:now(),dbdefault"`
	LastSeen      time.Time `momo:"default:now(),dbdefault,index:onboarding_candidates_last_seen"`
	RequestCount  *int64    `momo:"default:1,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *OnboardingCandidate) MomoConfig(c *momo.Config) {
	c.Check("onboarding_candidates_firmware_check", "(char_length(firmware) <= 64)")
	c.Check("onboarding_candidates_mac_check", "(mac ~ '^[0-9a-f]{12}$'::text)")
	c.Check("onboarding_candidates_model_check", "(char_length(model) <= 64)")
	c.Check("onboarding_candidates_request_count_check", "(request_count >= 1)")
	c.Check("onboarding_candidates_requested_path_check", "(char_length(requested_path) <= 256)")
	c.CheckIn(&m.Vendor,
		"fanvil",
		"generic",
		"grandstream",
		"poly",
		"snom",
		"yealink",
	)
}
