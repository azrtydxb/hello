// momo:fromdb sha256=627d9655e488b680a86249730549f2e5cc3b218cbf3718e4f9a40d01393b79ef
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"time"

	"github.com/azrtydxb/momo"
)

// TimeCondition models the table "time_conditions".
type TimeCondition struct {
	momo.Model
	ID            int64 `momo:"pk,serial"`
	Name          string
	ScheduleID    int64
	OpenKind      string `momo:"default:'hangup'::text,dbdefault"`
	OpenTarget    string `momo:"default:''::text,dbdefault"`
	OpenThen      string `momo:"default:''::text,dbdefault"`
	ClosedKind    string `momo:"default:'hangup'::text,dbdefault"`
	ClosedTarget  string `momo:"default:''::text,dbdefault"`
	ClosedThen    string `momo:"default:''::text,dbdefault"`
	HolidayKind   string `momo:"default:''::text,dbdefault"`
	HolidayTarget string `momo:"default:''::text,dbdefault"`
	HolidayThen   string `momo:"default:''::text,dbdefault"`
	Override      string `momo:"default:'auto'::text,dbdefault"`
	OverrideAt    *time.Time
	OverrideBy    string    `momo:"default:''::text,dbdefault"`
	ToggleNumber  string    `momo:"default:''::text,dbdefault"`
	CreatedAt     time.Time `momo:"default:now(),dbdefault"`
	UpdatedAt     time.Time `momo:"default:now(),dbdefault"`

	Schedule *Schedule `momo:"belongs_to,fk:ScheduleID,on_delete:restrict"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *TimeCondition) MomoConfig(c *momo.Config) {
	c.Unique("time_conditions_name_key", &m.Name)
	c.CheckIn(&m.ClosedKind,
		"announcement",
		"directory",
		"extension",
		"external",
		"hangup",
		"ivr",
		"paging",
		"queue",
		"ring_group",
		"time_condition",
		"voicemail",
		"voicemail_group",
	)
	c.Check("time_conditions_closed_then_check", "(closed_then ~ '^([0-9]{2,10})?$'::text)")
	c.CheckIn(&m.HolidayKind,
		"",
		"announcement",
		"directory",
		"extension",
		"external",
		"hangup",
		"ivr",
		"paging",
		"queue",
		"ring_group",
		"time_condition",
		"voicemail",
		"voicemail_group",
	)
	c.Check("time_conditions_holiday_then_check", "(holiday_then ~ '^([0-9]{2,10})?$'::text)")
	c.Check("time_conditions_name_check", "(name ~ '^[A-Za-z0-9._-]{1,64}$'::text)")
	c.CheckIn(&m.OpenKind,
		"announcement",
		"directory",
		"extension",
		"external",
		"hangup",
		"ivr",
		"paging",
		"queue",
		"ring_group",
		"time_condition",
		"voicemail",
		"voicemail_group",
	)
	c.Check("time_conditions_open_then_check", "(open_then ~ '^([0-9]{2,10})?$'::text)")
	c.CheckIn(&m.Override,
		"auto",
		"closed",
		"open",
	)
	c.Check("time_conditions_toggle_number_check", "(toggle_number ~ '^([0-9]{1,4})?$'::text)")
	c.Unique("time_conditions_toggle_number", &m.ToggleNumber).Where("(toggle_number <> ''::text)")
}
