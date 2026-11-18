// momo:fromdb sha256=12ebe93f819e9f2426dd7cf413dcfb99d7fa78cb37de43e7e64034df013e2c81
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// ScheduleRange models the table "schedule_ranges".
type ScheduleRange struct {
	momo.Model
	ScheduleID int64  `momo:"pk"`
	Weekday    int16  `momo:"pk"`
	StartTime  string `momo:"pk"`
	EndTime    string

	Schedule *Schedule `momo:"belongs_to,fk:ScheduleID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *ScheduleRange) MomoConfig(c *momo.Config) {
	c.Check("schedule_ranges_check", "(start_time <> end_time)")
	c.Check("schedule_ranges_end_time_check", "(end_time ~ '^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$'::text)")
	c.Check("schedule_ranges_start_time_check", "(start_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'::text)")
	c.Check("schedule_ranges_weekday_check", "((weekday >= 0) AND (weekday <= 6))")
}
