// momo:fromdb sha256=decfbd1cab3f0887ca4b1689cd3a73de92a065e7546b6fe27ba5e34b3fcc7c19
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// OnboardingIgnored models the table "onboarding_ignored".
type OnboardingIgnored struct {
	momo.Model `momo:"table:onboarding_ignored"`
	Mac        string    `momo:"pk"`
	Vendor     string    `momo:"default:''::text,dbdefault"`
	Model2     string    `momo:"column:model,default:''::text,dbdefault"`
	CreatedBy  string    `momo:"default:''::text,dbdefault"`
	CreatedAt  time.Time `momo:"default:now(),dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *OnboardingIgnored) MomoConfig(c *momo.Config) {
	c.Check("onboarding_ignored_mac_check", "(mac ~ '^[0-9a-f]{12}$'::text)")
}
