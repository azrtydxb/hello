// momo:fromdb sha256=37eff619e2fb6921448d8a0c2bef415ab1617d8efe63346b30f0f59342b3a8e4
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// WifiRadio models the table "wifi_radio".
type WifiRadio struct {
	momo.Model `momo:"table:wifi_radio"`
	GroupID    int64 `momo:"pk"`
	Band       *string
	RoamMode   *string
	Threshold  *int32
	PoorScan   *int32
	GoodScan   *int32
	Gain       *int32
	Dot11r     *bool
	Dot11v     *bool
	Prompt     *bool
	UpdatedAt  time.Time `momo:"default:now(),dbdefault"`

	Group *PhoneGroup `momo:"belongs_to,fk:GroupID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *WifiRadio) MomoConfig(c *momo.Config) {
	c.CheckIn(&m.Band,
		"2.4",
		"5",
		"both",
	)
	c.Check("wifi_radio_gain_check", "((gain >= 4) AND (gain <= 20))")
	c.Check("wifi_radio_good_scan_check", "((good_scan >= 5) AND (good_scan <= 600))")
	c.Check("wifi_radio_poor_scan_check", "((poor_scan >= 5) AND (poor_scan <= 300))")
	c.CheckIn(&m.RoamMode,
		"boost",
		"custom",
		"default",
		"fast",
	)
	c.Check("wifi_radio_threshold_check", "((threshold >= '-100'::integer) AND (threshold <= '-30'::integer))")
}
