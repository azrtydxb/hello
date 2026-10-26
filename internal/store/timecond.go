package store

// Time conditions (migration 00035): schedules (weekly open hours in a time
// zone), calendars (holidays and exceptions) and the time conditions that
// route a call by them. Every mutation runs through configChange, so it is
// audited, bumps the configuration revision and notifies hello-sip. The
// manual override lives in the time_conditions row, so it survives a restart
// and an HA failover, and a change of it reaches every node as a snapshot.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/timecond"
)

// Limits.
const (
	MaxSchedules       = 200
	MaxCalendars       = 100
	MaxCalendarEntries = timecond.MaxImportEntries
	MaxTimeConditions  = 200
	MaxScheduleRanges  = 100
)

// --- schedules ---------------------------------------------------------------

// Schedule is a named weekly schedule as the API shows it.
type Schedule struct {
	ID          int64               `json:"id"`
	Name        string              `json:"name"`
	TimeZone    string              `json:"timeZone"`
	Ranges      []timecond.DayRange `json:"ranges"`
	CalendarIDs []int64             `json:"calendarIds"`
	Calendars   []string            `json:"calendars"`
	CreatedAt   time.Time           `json:"createdAt"`
	UpdatedAt   time.Time           `json:"updatedAt"`
}

// ScheduleInput is the writable shape of a schedule; an update replaces the
// ranges and calendars as a whole.
type ScheduleInput struct {
	Name        string
	TimeZone    string
	Ranges      []timecond.DayRange
	CalendarIDs []int64
}

func listSchedules(ctx context.Context, q querier, where string, args ...any) ([]Schedule, error) {
	rows, err := q.QueryContext(ctx, `SELECT s.id, s.name, s.time_zone, s.created_at, s.updated_at FROM schedules s `+where+` ORDER BY s.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Schedule{}
	byID := map[int64]*Schedule{}
	var ids []int64
	for rows.Next() {
		var s Schedule
		if err := rows.Scan(&s.ID, &s.Name, &s.TimeZone, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		s.Ranges, s.CalendarIDs, s.Calendars = []timecond.DayRange{}, []int64{}, []string{}
		out = append(out, s)
		ids = append(ids, s.ID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	rrows, err := q.QueryContext(ctx, `SELECT schedule_id, weekday, start_time, end_time FROM schedule_ranges
		WHERE schedule_id = ANY($1) ORDER BY schedule_id, weekday, start_time`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rrows.Close() }()
	for rrows.Next() {
		var id int64
		var r timecond.DayRange
		if err := rrows.Scan(&id, &r.Day, &r.Start, &r.End); err != nil {
			return nil, err
		}
		byID[id].Ranges = append(byID[id].Ranges, r)
	}
	_ = rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, err
	}
	crows, err := q.QueryContext(ctx, `SELECT sc.schedule_id, c.id, c.name FROM schedule_calendars sc
		JOIN calendars c ON c.id = sc.calendar_id WHERE sc.schedule_id = ANY($1) ORDER BY sc.schedule_id, c.name`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = crows.Close() }()
	for crows.Next() {
		var id, cid int64
		var name string
		if err := crows.Scan(&id, &cid, &name); err != nil {
			return nil, err
		}
		byID[id].CalendarIDs = append(byID[id].CalendarIDs, cid)
		byID[id].Calendars = append(byID[id].Calendars, name)
	}
	return out, crows.Err()
}

// ListSchedules returns every schedule.
func (s *Store) ListSchedules(ctx context.Context) ([]Schedule, error) {
	return listSchedules(ctx, s.db, "")
}

// GetSchedule returns one schedule.
func (s *Store) GetSchedule(ctx context.Context, id int64) (Schedule, error) {
	ss, err := listSchedules(ctx, s.db, ` WHERE s.id = $1`, id)
	if err != nil {
		return Schedule{}, err
	}
	if len(ss) == 0 {
		return Schedule{}, ErrNotFound
	}
	return ss[0], nil
}

func writeScheduleParts(ctx context.Context, tx *sql.Tx, id int64, in ScheduleInput) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_ranges WHERE schedule_id = $1`, id); err != nil {
		return err
	}
	for _, r := range in.Ranges {
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_ranges (schedule_id, weekday, start_time, end_time)
			VALUES ($1, $2, $3, $4)`, id, r.Day, r.Start, r.End); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_calendars WHERE schedule_id = $1`, id); err != nil {
		return err
	}
	for _, c := range in.CalendarIDs {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM calendars WHERE id = $1`, c).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fieldError("calendarIds", fmt.Sprintf("calendar %d does not exist", c))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_calendars (schedule_id, calendar_id) VALUES ($1, $2)`, id, c); err != nil {
			return err
		}
	}
	return nil
}

// CreateSchedule inserts a schedule with its ranges and calendars.
func (s *Store) CreateSchedule(ctx context.Context, actor string, in ScheduleInput, check Check) (Schedule, error) {
	var out Schedule
	err := s.configChange(ctx, actor, "create", "schedule", check, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schedules`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxSchedules {
			return 0, &InUseError{Msg: fmt.Sprintf("at most %d schedules", MaxSchedules)}
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO schedules (name, time_zone) VALUES ($1, $2) RETURNING id`,
			in.Name, in.TimeZone).Scan(&id); err != nil {
			return 0, err
		}
		if err := writeScheduleParts(ctx, tx, id, in); err != nil {
			return id, err
		}
		ss, err := listSchedules(ctx, tx, ` WHERE s.id = $1`, id)
		if err == nil && len(ss) == 1 {
			out = ss[0]
		}
		return id, err
	})
	return out, err
}

// UpdateSchedule replaces a schedule.
func (s *Store) UpdateSchedule(ctx context.Context, actor string, id int64, in ScheduleInput, check Check) (Schedule, error) {
	var out Schedule
	err := s.configChange(ctx, actor, "update", "schedule", check, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `UPDATE schedules SET name = $2, time_zone = $3, updated_at = now() WHERE id = $1`,
			id, in.Name, in.TimeZone)
		if err != nil {
			return id, err
		}
		if err := requireRow(res); err != nil {
			return id, err
		}
		if err := writeScheduleParts(ctx, tx, id, in); err != nil {
			return id, err
		}
		ss, err := listSchedules(ctx, tx, ` WHERE s.id = $1`, id)
		if err == nil && len(ss) == 1 {
			out = ss[0]
		}
		return id, err
	})
	return out, err
}

// DeleteSchedule removes a schedule no time condition uses.
func (s *Store) DeleteSchedule(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "schedule", check, func(tx *sql.Tx) (int64, error) {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM schedules WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
			return id, err
		}
		refs, err := stringsOf(ctx, tx, `SELECT 'time condition ' || name FROM time_conditions WHERE schedule_id = $1 ORDER BY name`, id)
		if err != nil {
			return id, err
		}
		if len(refs) > 0 {
			return id, inUseBy("schedule "+name, refs)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM schedules WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// stringsOf runs a query that returns one text column.
func stringsOf(ctx context.Context, q querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// --- calendars ---------------------------------------------------------------

// Calendar is a named holiday / exception calendar.
type Calendar struct {
	ID          int64            `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Entries     []timecond.Entry `json:"entries"`
	// Schedules names the schedules that consult it (read-only).
	Schedules []string  `json:"schedules"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CalendarInput is the writable shape of a calendar; an update replaces the
// entries as a whole.
type CalendarInput struct {
	Name        string
	Description string
	Entries     []timecond.Entry
}

// calEntryCols reads an entry with dates in the API's form.
const calEntryCols = `e.id, e.label, to_char(e.start_date, 'YYYY-MM-DD'), to_char(e.end_date, 'YYYY-MM-DD'), e.yearly,
	e.all_day, e.start_time, e.end_time, e.mode, e.open_hours`

func scanCalEntry(rows interface{ Scan(...any) error }, calendarID *int64) (timecond.Entry, error) {
	var (
		e     timecond.Entry
		hours []byte
	)
	dst := []any{&e.ID, &e.Label, &e.Start, &e.End, &e.Yearly, &e.AllDay, &e.StartTime, &e.EndTime, &e.Mode, &hours}
	if calendarID != nil {
		dst = append([]any{calendarID}, dst...)
	}
	if err := rows.Scan(dst...); err != nil {
		return e, err
	}
	e.Hours = []timecond.Range{}
	if len(hours) > 0 {
		if err := json.Unmarshal(hours, &e.Hours); err != nil {
			return e, fmt.Errorf("calendar entry %d: open hours: %w", e.ID, err)
		}
	}
	if e.Yearly { // stored in the year 2000
		e.Start, e.End = e.Start[5:], e.End[5:]
	}
	return e, nil
}

func listCalendars(ctx context.Context, q querier, where string, args ...any) ([]Calendar, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id, c.name, c.description, c.created_at, c.updated_at FROM calendars c `+where+` ORDER BY c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Calendar{}
	var ids []int64
	for rows.Next() {
		var c Calendar
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Entries, c.Schedules = []timecond.Entry{}, []string{}
		out = append(out, c)
		ids = append(ids, c.ID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	byID := map[int64]*Calendar{}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	erows, err := q.QueryContext(ctx, `SELECT e.calendar_id, `+calEntryCols+` FROM calendar_entries e
		WHERE e.calendar_id = ANY($1) ORDER BY e.calendar_id, e.yearly, to_char(e.start_date, 'MM-DD'), e.start_date, e.id`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = erows.Close() }()
	for erows.Next() {
		var cid int64
		e, err := scanCalEntry(erows, &cid)
		if err != nil {
			return nil, err
		}
		byID[cid].Entries = append(byID[cid].Entries, e)
	}
	_ = erows.Close()
	if err := erows.Err(); err != nil {
		return nil, err
	}
	srows, err := q.QueryContext(ctx, `SELECT sc.calendar_id, s.name FROM schedule_calendars sc
		JOIN schedules s ON s.id = sc.schedule_id WHERE sc.calendar_id = ANY($1) ORDER BY sc.calendar_id, s.name`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = srows.Close() }()
	for srows.Next() {
		var cid int64
		var name string
		if err := srows.Scan(&cid, &name); err != nil {
			return nil, err
		}
		byID[cid].Schedules = append(byID[cid].Schedules, name)
	}
	return out, srows.Err()
}

// ListCalendars returns every calendar.
func (s *Store) ListCalendars(ctx context.Context) ([]Calendar, error) {
	return listCalendars(ctx, s.db, "")
}

// GetCalendar returns one calendar.
func (s *Store) GetCalendar(ctx context.Context, id int64) (Calendar, error) {
	cs, err := listCalendars(ctx, s.db, ` WHERE c.id = $1`, id)
	if err != nil {
		return Calendar{}, err
	}
	if len(cs) == 0 {
		return Calendar{}, ErrNotFound
	}
	return cs[0], nil
}

// insertEntries writes entries (already normalised) to a calendar.
func insertEntries(ctx context.Context, tx *sql.Tx, calendarID int64, entries []timecond.Entry) error {
	for _, e := range entries {
		start, end := e.Start, e.End
		if e.Yearly {
			start, end = "2000-"+start, "2000-"+end
		}
		hours, err := json.Marshal(e.Hours)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO calendar_entries
			(calendar_id, label, start_date, end_date, yearly, all_day, start_time, end_time, mode, open_hours)
			VALUES ($1, $2, $3::date, $4::date, $5, $6, $7, $8, $9, $10::jsonb)`,
			calendarID, e.Label, start, end, e.Yearly, e.AllDay, e.StartTime, e.EndTime, e.Mode, string(hours)); err != nil {
			return err
		}
	}
	return nil
}

// CreateCalendar inserts a calendar with its entries.
func (s *Store) CreateCalendar(ctx context.Context, actor string, in CalendarInput, check Check) (Calendar, error) {
	var out Calendar
	err := s.configChange(ctx, actor, "create", "calendar", check, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM calendars`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxCalendars {
			return 0, &InUseError{Msg: fmt.Sprintf("at most %d calendars", MaxCalendars)}
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO calendars (name, description) VALUES ($1, $2) RETURNING id`,
			in.Name, in.Description).Scan(&id); err != nil {
			return 0, err
		}
		if err := insertEntries(ctx, tx, id, in.Entries); err != nil {
			return id, err
		}
		cs, err := listCalendars(ctx, tx, ` WHERE c.id = $1`, id)
		if err == nil && len(cs) == 1 {
			out = cs[0]
		}
		return id, err
	})
	return out, err
}

// UpdateCalendar replaces a calendar and its entries.
func (s *Store) UpdateCalendar(ctx context.Context, actor string, id int64, in CalendarInput, check Check) (Calendar, error) {
	var out Calendar
	err := s.configChange(ctx, actor, "update", "calendar", check, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `UPDATE calendars SET name = $2, description = $3, updated_at = now() WHERE id = $1`,
			id, in.Name, in.Description)
		if err != nil {
			return id, err
		}
		if err := requireRow(res); err != nil {
			return id, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_entries WHERE calendar_id = $1`, id); err != nil {
			return id, err
		}
		if err := insertEntries(ctx, tx, id, in.Entries); err != nil {
			return id, err
		}
		cs, err := listCalendars(ctx, tx, ` WHERE c.id = $1`, id)
		if err == nil && len(cs) == 1 {
			out = cs[0]
		}
		return id, err
	})
	return out, err
}

// ImportCalendarEntries adds entries to a calendar, or replaces its entries
// with them when replace is set. The calendar ends up with at most
// MaxCalendarEntries entries.
func (s *Store) ImportCalendarEntries(ctx context.Context, actor string, id int64, entries []timecond.Entry, replace bool, check Check) (Calendar, error) {
	var out Calendar
	err := s.configChange(ctx, actor, "import", "calendar", check, func(tx *sql.Tx) (int64, error) {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM calendars WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
			return id, err
		}
		if replace {
			if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_entries WHERE calendar_id = $1`, id); err != nil {
				return id, err
			}
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM calendar_entries WHERE calendar_id = $1`, id).Scan(&n); err != nil {
			return id, err
		}
		if n+len(entries) > MaxCalendarEntries {
			return id, &InUseError{Msg: fmt.Sprintf("a calendar holds at most %d entries", MaxCalendarEntries)}
		}
		if err := insertEntries(ctx, tx, id, entries); err != nil {
			return id, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE calendars SET updated_at = now() WHERE id = $1`, id); err != nil {
			return id, err
		}
		cs, err := listCalendars(ctx, tx, ` WHERE c.id = $1`, id)
		if err == nil && len(cs) == 1 {
			out = cs[0]
		}
		return id, err
	})
	return out, err
}

// DeleteCalendar removes a calendar no schedule consults.
func (s *Store) DeleteCalendar(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "calendar", check, func(tx *sql.Tx) (int64, error) {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM calendars WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
			return id, err
		}
		refs, err := stringsOf(ctx, tx, `SELECT 'schedule ' || s.name FROM schedule_calendars sc
			JOIN schedules s ON s.id = sc.schedule_id WHERE sc.calendar_id = $1 ORDER BY s.name`, id)
		if err != nil {
			return id, err
		}
		if len(refs) > 0 {
			return id, inUseBy("calendar "+name, refs)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM calendars WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// --- time conditions ---------------------------------------------------------

// TimeCondition is a time condition as the API shows it.
type TimeCondition struct {
	ID           int64          `json:"id"`
	Name         string         `json:"name"`
	ScheduleID   int64          `json:"scheduleId"`
	ScheduleName string         `json:"scheduleName"`
	Open         timecond.Dest  `json:"open"`
	Closed       timecond.Dest  `json:"closed"`
	Holiday      *timecond.Dest `json:"holiday"`
	Override     string         `json:"override"`
	OverrideAt   *time.Time     `json:"overrideAt"`
	OverrideBy   string         `json:"overrideBy"`
	ToggleNumber string         `json:"toggleNumber"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

// TimeConditionInput is the writable shape of a time condition. The override
// is set through SetTimeConditionOverride, never by an update.
type TimeConditionInput struct {
	Name         string
	ScheduleID   int64
	Open         timecond.Dest
	Closed       timecond.Dest
	Holiday      *timecond.Dest
	ToggleNumber string
}

const tcCols = `t.id, t.name, t.schedule_id, s.name, t.open_kind, t.open_target, t.open_then, t.closed_kind, t.closed_target,
	t.closed_then, t.holiday_kind, t.holiday_target, t.holiday_then, t.override, t.override_at, t.override_by,
	t.toggle_number, t.created_at, t.updated_at`

const tcFrom = ` FROM time_conditions t JOIN schedules s ON s.id = t.schedule_id `

func listTimeConditions(ctx context.Context, q querier, where string, args ...any) ([]TimeCondition, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+tcCols+tcFrom+where+` ORDER BY t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []TimeCondition{}
	for rows.Next() {
		var (
			t          TimeCondition
			hk, ht, hn string
			at         sql.NullTime
		)
		if err := rows.Scan(&t.ID, &t.Name, &t.ScheduleID, &t.ScheduleName,
			&t.Open.Kind, &t.Open.Target, &t.Open.ThenVoicemail,
			&t.Closed.Kind, &t.Closed.Target, &t.Closed.ThenVoicemail,
			&hk, &ht, &hn, &t.Override, &at, &t.OverrideBy, &t.ToggleNumber, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if hk != "" {
			t.Holiday = &timecond.Dest{Kind: hk, Target: ht, ThenVoicemail: hn}
		}
		t.OverrideAt = nullTime(at)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTimeConditions returns every time condition.
func (s *Store) ListTimeConditions(ctx context.Context) ([]TimeCondition, error) {
	return listTimeConditions(ctx, s.db, "")
}

// GetTimeCondition returns one time condition.
func (s *Store) GetTimeCondition(ctx context.Context, id int64) (TimeCondition, error) {
	ts, err := listTimeConditions(ctx, s.db, ` WHERE t.id = $1`, id)
	if err != nil {
		return TimeCondition{}, err
	}
	if len(ts) == 0 {
		return TimeCondition{}, ErrNotFound
	}
	return ts[0], nil
}

// requireTimeConditionTx refuses a reference to a time condition that does
// not exist, for the settings that name one (ring group failover, feature
// codes). It runs in the configuration change's transaction, under the
// configuration lock, so a time condition cannot be deleted between the
// check and the write that names it.
func requireTimeConditionTx(ctx context.Context, tx *sql.Tx, path, name string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM time_conditions WHERE name = $1`, name).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fieldError(path, fmt.Sprintf("time condition %q does not exist", name))
	}
	return nil
}

type tcDest struct {
	path string
	d    timecond.Dest
}

func (in TimeConditionInput) destinations() []tcDest {
	out := []tcDest{{"open", in.Open}, {"closed", in.Closed}}
	if in.Holiday != nil {
		out = append(out, tcDest{"holiday", *in.Holiday})
	}
	return out
}

// checkTimeCondition verifies the references of a time condition before it
// is written: the schedule, the destinations (and the voicemail box that
// follows an announcement), the toggle number, and that no chain of time
// conditions leads back to it. id is 0 for a new one.
func checkTimeCondition(ctx context.Context, tx *sql.Tx, id int64, in TimeConditionInput) error {
	var n int
	// A taken name is a conflict (409) whatever else the request gets wrong,
	// as the unique index would report it; checked first so a duplicate is
	// not reported as, say, its toggle number being in use.
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM time_conditions WHERE name = $1 AND id <> $2`, in.Name, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: time_conditions_name_key", ErrConflict)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schedules WHERE id = $1`, in.ScheduleID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fieldError("scheduleId", "names a schedule that does not exist")
	}
	for _, d := range in.destinations() {
		if d.d.Kind == IVRTimeCondition && d.d.Target == in.Name {
			return fieldError(d.path+".target", "a time condition cannot lead to itself")
		}
		if err := checkIVRDestination(ctx, tx, ivrDest{path: d.path, IVRDestination: IVRDestination{Kind: d.d.Kind, Target: d.d.Target}}); err != nil {
			return err
		}
		if d.d.ThenVoicemail != "" {
			if d.d.Kind != IVRAnnouncement {
				return fieldError(d.path+".thenVoicemail", `is only for an "announcement" destination`)
			}
			if err := checkIVRDestination(ctx, tx, ivrDest{path: d.path + ".thenVoicemail", IVRDestination: IVRDestination{Kind: IVRVoicemail, Target: d.d.ThenVoicemail}}); err != nil {
				var ve *ValidationError
				if errors.As(err, &ve) {
					return fieldError(d.path+".thenVoicemail", "extension "+d.d.ThenVoicemail+" has no voicemail box")
				}
				return err
			}
		}
	}
	if in.ToggleNumber != "" {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM time_conditions WHERE toggle_number = $1 AND id <> $2`,
			in.ToggleNumber, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fieldError("toggleNumber", "is used by another time condition")
		}
	}
	return checkTimeConditionLoops(ctx, tx, id, in)
}

// checkTimeConditionLoops refuses a time condition from which a chain of
// time conditions leads back to it, whichever branch each takes.
func checkTimeConditionLoops(ctx context.Context, tx *sql.Tx, id int64, in TimeConditionInput) error {
	rows, err := tx.QueryContext(ctx, `SELECT name, open_kind, open_target, closed_kind, closed_target, holiday_kind, holiday_target
		FROM time_conditions WHERE id <> $1`, id)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	next := map[string][]string{}
	for rows.Next() {
		var name, ok, ot, ck, ct, hk, ht string
		if err := rows.Scan(&name, &ok, &ot, &ck, &ct, &hk, &ht); err != nil {
			return err
		}
		for _, d := range [][2]string{{ok, ot}, {ck, ct}, {hk, ht}} {
			if d[0] == IVRTimeCondition {
				next[name] = append(next[name], d[1])
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range in.destinations() {
		if d.d.Kind != IVRTimeCondition {
			continue
		}
		if path, ok := tcPath(next, d.d.Target, in.Name, nil, map[string]bool{}); ok {
			return fieldError(d.path+".target", fmt.Sprintf("would lead back to this time condition (%s)",
				strings.Join(append([]string{in.Name}, path...), " -> ")))
		}
	}
	return nil
}

// tcPath finds a chain from `from` to `want` through the time conditions'
// own destinations; the chain is returned with both ends.
func tcPath(next map[string][]string, from, want string, via []string, seen map[string]bool) ([]string, bool) {
	via = append(via[:len(via):len(via)], from)
	if from == want {
		return via, true
	}
	if seen[from] {
		return nil, false
	}
	seen[from] = true
	for _, n := range next[from] {
		if p, ok := tcPath(next, n, want, via, seen); ok {
			return p, true
		}
	}
	return nil, false
}

func destCols(d *timecond.Dest) (kind, target, then string) {
	if d == nil {
		return "", "", ""
	}
	return d.Kind, d.Target, d.ThenVoicemail
}

// CreateTimeCondition inserts a time condition; it starts in automatic mode.
func (s *Store) CreateTimeCondition(ctx context.Context, actor string, in TimeConditionInput, check Check) (TimeCondition, error) {
	var out TimeCondition
	err := s.configChange(ctx, actor, "create", "time_condition", check, func(tx *sql.Tx) (int64, error) {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM time_conditions`).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxTimeConditions {
			return 0, &InUseError{Msg: fmt.Sprintf("at most %d time conditions", MaxTimeConditions)}
		}
		if err := checkTimeCondition(ctx, tx, 0, in); err != nil {
			return 0, err
		}
		hk, ht, hn := destCols(in.Holiday)
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO time_conditions (name, schedule_id, open_kind, open_target, open_then,
				closed_kind, closed_target, closed_then, holiday_kind, holiday_target, holiday_then, toggle_number)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
			in.Name, in.ScheduleID, in.Open.Kind, in.Open.Target, in.Open.ThenVoicemail,
			in.Closed.Kind, in.Closed.Target, in.Closed.ThenVoicemail, hk, ht, hn, in.ToggleNumber).Scan(&id); err != nil {
			return 0, err
		}
		ts, err := listTimeConditions(ctx, tx, ` WHERE t.id = $1`, id)
		if err == nil && len(ts) == 1 {
			out = ts[0]
		}
		return id, err
	})
	return out, err
}

// tcReferences names what points at the time condition: inbound routes, ring
// groups, feature codes, IVR menus and other time conditions.
func tcReferences(ctx context.Context, tx *sql.Tx, id int64, name string) ([]string, error) {
	var refs []string
	for _, q := range []struct {
		query, label string
		args         []any
	}{
		{`SELECT name FROM inbound_routes WHERE destination_kind = 'time_condition' AND destination = $1`, "inbound route", []any{name}},
		{`SELECT name FROM ring_groups WHERE failure_kind = 'time_condition' AND failure_target = $1`, "ring group", []any{name}},
		{`SELECT code FROM feature_codes WHERE action = 'time_condition' AND argument = $1`, "feature code", []any{name}},
		{`SELECT m.name FROM ivr_menus m WHERE (m.timeout_kind = 'time_condition' AND m.timeout_target = $1)
			OR (m.invalid_kind = 'time_condition' AND m.invalid_target = $1)
			OR EXISTS (SELECT 1 FROM ivr_menu_options o WHERE o.menu_id = m.id AND o.dest_kind = 'time_condition' AND o.dest_target = $1)`,
			"IVR menu", []any{name}},
		{`SELECT name FROM time_conditions WHERE id <> $2 AND (
			(open_kind = 'time_condition' AND open_target = $1) OR (closed_kind = 'time_condition' AND closed_target = $1)
			OR (holiday_kind = 'time_condition' AND holiday_target = $1))`, "time condition", []any{name, id}},
		{`SELECT name FROM queues WHERE (timeout_kind = 'time_condition' AND timeout_target = $1)
			OR (full_kind = 'time_condition' AND full_target = $1) OR (no_agents_kind = 'time_condition' AND no_agents_target = $1)`, "queue", []any{name}},
	} {
		names, err := stringsOf(ctx, tx, q.query, q.args...)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			refs = append(refs, q.label+" "+n)
		}
	}
	return refs, nil
}

// UpdateTimeCondition replaces a time condition, keeping its override.
// Renaming one that something points at is refused: the references name it.
func (s *Store) UpdateTimeCondition(ctx context.Context, actor string, id int64, in TimeConditionInput, check Check) (TimeCondition, error) {
	var out TimeCondition
	err := s.configChange(ctx, actor, "update", "time_condition", check, func(tx *sql.Tx) (int64, error) {
		var cur string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM time_conditions WHERE id = $1 FOR UPDATE`, id).Scan(&cur); err != nil {
			return id, err
		}
		if cur != in.Name {
			refs, err := tcReferences(ctx, tx, id, cur)
			if err != nil {
				return id, err
			}
			if len(refs) > 0 {
				return id, inUseBy("time condition "+cur+", which cannot be renamed while it", refs)
			}
		}
		if err := checkTimeCondition(ctx, tx, id, in); err != nil {
			return id, err
		}
		hk, ht, hn := destCols(in.Holiday)
		if _, err := tx.ExecContext(ctx, `UPDATE time_conditions SET name = $2, schedule_id = $3, open_kind = $4, open_target = $5,
				open_then = $6, closed_kind = $7, closed_target = $8, closed_then = $9, holiday_kind = $10, holiday_target = $11,
				holiday_then = $12, toggle_number = $13, updated_at = now()
			WHERE id = $1`, id, in.Name, in.ScheduleID, in.Open.Kind, in.Open.Target, in.Open.ThenVoicemail,
			in.Closed.Kind, in.Closed.Target, in.Closed.ThenVoicemail, hk, ht, hn, in.ToggleNumber); err != nil {
			return id, err
		}
		ts, err := listTimeConditions(ctx, tx, ` WHERE t.id = $1`, id)
		if err == nil && len(ts) == 1 {
			out = ts[0]
		}
		return id, err
	})
	return out, err
}

// DeleteTimeCondition removes a time condition nothing points at.
func (s *Store) DeleteTimeCondition(ctx context.Context, actor string, id int64, check Check) error {
	return s.configChange(ctx, actor, "delete", "time_condition", check, func(tx *sql.Tx) (int64, error) {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM time_conditions WHERE id = $1 FOR UPDATE`, id).Scan(&name); err != nil {
			return id, err
		}
		refs, err := tcReferences(ctx, tx, id, name)
		if err != nil {
			return id, err
		}
		if len(refs) > 0 {
			return id, inUseBy("time condition "+name, refs)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM time_conditions WHERE id = $1`, id)
		if err != nil {
			return id, err
		}
		return id, requireRow(res)
	})
}

// ValidOverride reports whether v is an override mode.
func ValidOverride(v string) bool {
	return v == timecond.OverrideAuto || v == timecond.OverrideOpen || v == timecond.OverrideClosed
}

// SetTimeConditionOverride forces a time condition open or closed, or
// returns it to the schedule (auto). The change is a configuration change:
// it is audited and reaches every SIP node as a new snapshot revision.
func (s *Store) SetTimeConditionOverride(ctx context.Context, actor string, id int64, mode string) (TimeCondition, error) {
	if !ValidOverride(mode) {
		return TimeCondition{}, fieldError("override", `must be "auto", "open" or "closed"`)
	}
	var out TimeCondition
	err := s.configChange(ctx, actor, "override", "time_condition", nil, func(tx *sql.Tx) (int64, error) {
		if err := setOverride(ctx, tx, id, actor, mode); err != nil {
			return id, err
		}
		ts, err := listTimeConditions(ctx, tx, ` WHERE t.id = $1`, id)
		if err == nil && len(ts) == 1 {
			out = ts[0]
		}
		return id, err
	})
	return out, err
}

func setOverride(ctx context.Context, tx *sql.Tx, id int64, actor, mode string) error {
	var at any
	if mode != timecond.OverrideAuto {
		at = time.Now().UTC()
	}
	res, err := tx.ExecContext(ctx, `UPDATE time_conditions SET override = $2, override_at = $3, override_by = $4, updated_at = now()
		WHERE id = $1`, id, mode, at, actor)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// Toggle modes of the time-condition feature code.
const (
	ToggleFlip = "toggle"
)

// ApplyTimeConditionToggle is the feature code *28<n>: it changes the
// override of the time condition whose toggle number is number. mode
// "toggle" returns a forced condition to automatic and forces an automatic
// one to the opposite of what the schedule says now (open becomes closed,
// closed or holiday becomes open); "open", "closed" and "auto" set that.
// It returns the condition and the override now in force.
func (s *Store) ApplyTimeConditionToggle(ctx context.Context, actor, number, mode string, now time.Time) (TimeCondition, error) {
	if mode != ToggleFlip && !ValidOverride(mode) {
		return TimeCondition{}, fieldError("mode", `must be "toggle", "auto", "open" or "closed"`)
	}
	var out TimeCondition
	err := s.configChange(ctx, actor, "override", "time_condition", nil, func(tx *sql.Tx) (int64, error) {
		ts, err := listTimeConditions(ctx, tx, ` WHERE t.toggle_number = $1`, number)
		if err != nil {
			return 0, err
		}
		if len(ts) == 0 {
			return 0, ErrNotFound
		}
		t := ts[0]
		if mode == ToggleFlip {
			switch {
			case t.Override != timecond.OverrideAuto:
				mode = timecond.OverrideAuto
			default:
				set, err := loadTimeConditionSet(ctx, tx)
				if err != nil {
					return t.ID, err
				}
				c, ok := set[t.Name]
				if !ok {
					return t.ID, ErrNotFound
				}
				if c.Decide(now).Branch == timecond.BranchOpen {
					mode = timecond.OverrideClosed
				} else {
					mode = timecond.OverrideOpen
				}
			}
		}
		if err := setOverride(ctx, tx, t.ID, actor, mode); err != nil {
			return t.ID, err
		}
		ts, err = listTimeConditions(ctx, tx, ` WHERE t.id = $1`, t.ID)
		if err == nil && len(ts) == 1 {
			out = ts[0]
		}
		return t.ID, err
	})
	return out, err
}

// loadTimeConditionSet reads every time condition as the evaluator needs it,
// by name: the schedule with its calendars' entries and the destinations.
func loadTimeConditionSet(ctx context.Context, q querier) (map[string]timecond.Condition, error) {
	scheds, err := listSchedules(ctx, q, "")
	if err != nil {
		return nil, err
	}
	cals, err := listCalendars(ctx, q, "")
	if err != nil {
		return nil, err
	}
	calByID := map[int64]Calendar{}
	for _, c := range cals {
		calByID[c.ID] = c
	}
	specs := map[int64]timecond.Spec{}
	for _, s := range scheds {
		spec := timecond.Spec{TimeZone: s.TimeZone, Ranges: s.Ranges}
		for _, cid := range s.CalendarIDs {
			c := calByID[cid]
			for _, e := range c.Entries {
				spec.Entries = append(spec.Entries, timecond.CalendarEntry{Calendar: c.Name, Entry: e})
			}
		}
		specs[s.ID] = spec
	}
	tcs, err := listTimeConditions(ctx, q, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]timecond.Condition, len(tcs))
	for _, t := range tcs {
		out[t.Name] = timecond.Condition{Name: t.Name, Spec: specs[t.ScheduleID], Override: t.Override,
			Open: t.Open, Closed: t.Closed, Holiday: t.Holiday}
	}
	return out, nil
}

// TimeConditionEvaluation is what a time condition would do at an instant.
type TimeConditionEvaluation struct {
	Name     string `json:"name"`
	At       string `json:"at"`
	Override string `json:"override"`
	// Decision is the condition's own decision; Resolution follows any time
	// conditions it names to the destination the call would reach.
	Decision   timecond.Decision   `json:"decision"`
	Resolution timecond.Resolution `json:"resolution"`
	// Error is set when the chain cannot be followed (a loop or a missing
	// condition); the call would be hung up.
	Error string `json:"error,omitempty"`
}

// EvaluateTimeCondition says what the time condition would do at at, with
// its stored override. ignoreOverride evaluates the schedule alone.
func (s *Store) EvaluateTimeCondition(ctx context.Context, id int64, at time.Time, ignoreOverride bool) (TimeConditionEvaluation, error) {
	t, err := s.GetTimeCondition(ctx, id)
	if err != nil {
		return TimeConditionEvaluation{}, err
	}
	set, err := loadTimeConditionSet(ctx, s.db)
	if err != nil {
		return TimeConditionEvaluation{}, err
	}
	if ignoreOverride {
		for n, c := range set {
			c.Override = timecond.OverrideAuto
			set[n] = c
		}
	}
	lookup := func(n string) (timecond.Condition, bool) { c, ok := set[n]; return c, ok }
	ev := TimeConditionEvaluation{Name: t.Name, At: at.UTC().Format(time.RFC3339), Override: set[t.Name].Override}
	ev.Decision = set[t.Name].Decide(at)
	res, err := timecond.Resolve(lookup, t.Name, at)
	ev.Resolution = res
	if res.Steps == nil {
		ev.Resolution.Steps = []timecond.Step{}
	}
	if err != nil {
		ev.Error = err.Error()
	}
	return ev, nil
}
