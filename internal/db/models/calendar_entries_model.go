// momo:fromdb sha256=52b166894efed42e8890f71b09fb3ecfee284eae4730ac187896617ca1f34d1d
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"encoding/json"

	"github.com/azrtydxb/momo"
	"github.com/azrtydxb/momo/types"
)

// CalendarEntry models the table "calendar_entries".
type CalendarEntry struct {
	momo.Model
	ID         int64  `momo:"pk,serial"`
	CalendarID int64  `momo:"index:calendar_entries_calendar"`
	Label      string `momo:"default:''::text,dbdefault"`
	StartDate  types.Date
	EndDate    types.Date
	Yearly     bool                        `momo:"default:false,dbdefault"`
	AllDay     *bool                       `momo:"default:true,dbdefault"`
	StartTime  string                      `momo:"default:''::text,dbdefault"`
	EndTime    string                      `momo:"default:''::text,dbdefault"`
	Mode       string                      `momo:"default:'closed'::text,dbdefault"`
	OpenHours  types.JSON[json.RawMessage] `momo:"default:'[]'::jsonb,dbdefault"`

	Calendar *Calendar `momo:"belongs_to,fk:CalendarID,on_delete:cascade"`
}

// MomoConfig declares what a tag cannot: the other indexes, constraints, enums and defaults.
func (m *CalendarEntry) MomoConfig(c *momo.Config) {
	c.Check("calendar_entries_check", "(yearly OR (end_date >= start_date))")
	c.Check("calendar_entries_check1", "(all_day OR ((start_time <> ''::text) AND (end_time <> ''::text) AND (start_time <> end_time)))")
	c.Check("calendar_entries_end_time_check", "(end_time ~ '^((([01][0-9]|2[0-3]):[0-5][0-9])|24:00)?$'::text)")
	c.Check("calendar_entries_label_check", "(length(label) <= 120)")
	c.CheckIn(&m.Mode,
		"closed",
		"open",
	)
	c.Check("calendar_entries_start_time_check", "(start_time ~ '^(([01][0-9]|2[0-3]):[0-5][0-9])?$'::text)")
}
