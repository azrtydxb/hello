// momo:fromdb sha256=d71e1b3bc075d9b41fe311fd18ab8e8fe707dbf2bcdfdaf9b34b84c21e2b4c1f
// Written by momo generate --from-db. A re-run keeps this file if you edit it, and reports a conflict when the database changed the model.

package models

import (
	"github.com/azrtydxb/momo"
)

// ScheduleCalendar models the table "schedule_calendars".
type ScheduleCalendar struct {
	momo.Model
	ScheduleID int64 `momo:"pk"`
	CalendarID int64 `momo:"pk"`

	Calendar *Calendar `momo:"belongs_to,fk:CalendarID,on_delete:restrict"`
	Schedule *Schedule `momo:"belongs_to,fk:ScheduleID,on_delete:cascade"`
}
