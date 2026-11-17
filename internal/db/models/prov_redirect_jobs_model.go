// momo:fromdb sha256=85577fdd833235a08f5e543c5d82f6b1dcbf2b5c075cb9b924822f0c48f8db14
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// ProvRedirectJob models the table "prov_redirect_jobs".
type ProvRedirectJob struct {
	momo.Model
	Seq           int64  `momo:"default:nextval('prov_redirect_jobs_seq'::regclass),dbdefault"`
	Vendor        string `momo:"pk"`
	Mac           string `momo:"pk"`
	Op            string
	Attempts      int32     `momo:"default:0,dbdefault"`
	NextAttemptAt time.Time `momo:"default:now(),dbdefault,index:prov_redirect_jobs_due"`
	FirstQueuedAt time.Time `momo:"default:now(),dbdefault"`
	LastError     string    `momo:"default:''::text,dbdefault"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ProvRedirectJob) MomoConfig(c *momo.Config) {
	// The standalone sequence behind Seq's default (no column owns it).
	c.Sequence("prov_redirect_jobs_seq")
	c.Check("prov_redirect_jobs_attempts_check", "(attempts >= 0)")
	c.Check("prov_redirect_jobs_mac_check", "(mac ~ '^[0-9a-f]{12}$'::text)")
	c.CheckIn(&m.Op,
		"register",
		"unregister",
	)
	c.CheckIn(&m.Vendor,
		"fanvil",
		"grandstream",
		"poly",
		"snom",
		"yealink",
	)
}
