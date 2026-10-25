package snapshot

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/azrtydxb/hello/internal/timecond"
)

// TimeConditions (migration 00020) are routable objects that pick a
// destination from a schedule and its calendars. The snapshot carries each
// with its manual override, so the override reaches every node with the
// revision that changed it and survives a restart or failover.

// TimeCondition is a time condition ready to decide, with the digits of its
// feature-code toggle (empty: none).
type TimeCondition struct {
	timecond.Condition
	ToggleNumber string
}

// WithTimeConditions installs the time conditions (tests and Load).
func (s *Snapshot) WithTimeConditions(tcs []TimeCondition) *Snapshot {
	if s.tcByName == nil {
		s.tcByName, s.tcByToggle = map[string]TimeCondition{}, map[string]TimeCondition{}
	}
	for _, t := range tcs {
		s.tcByName[t.Name] = t
		if t.ToggleNumber != "" {
			s.tcByToggle[t.ToggleNumber] = t
		}
	}
	return s
}

// TimeCondition returns the time condition with that name.
func (s *Snapshot) TimeCondition(name string) (TimeCondition, bool) {
	t, ok := s.tcByName[name]
	return t, ok
}

// TimeConditionByToggle returns the time condition whose feature-code
// toggle number is n.
func (s *Snapshot) TimeConditionByToggle(n string) (TimeCondition, bool) {
	if n == "" {
		return TimeCondition{}, false
	}
	t, ok := s.tcByToggle[n]
	return t, ok
}

// TimeConditions lists the time conditions.
func (s *Snapshot) TimeConditions() []TimeCondition {
	out := make([]TimeCondition, 0, len(s.tcByName))
	for _, t := range s.tcByName {
		out = append(out, t)
	}
	return out
}

// TimeConditionLookup is timecond.Resolve's lookup over this snapshot.
func (s *Snapshot) TimeConditionLookup(name string) (timecond.Condition, bool) {
	t, ok := s.tcByName[name]
	return t.Condition, ok
}

// loadTimeConditions reads the time conditions with their schedules and the
// entries of the calendars those consult.
func loadTimeConditions(ctx context.Context, db Querier) ([]TimeCondition, error) {
	rows, err := db.Query(ctx, `SELECT t.name, t.schedule_id, t.open_kind, t.open_target, t.open_then,
		t.closed_kind, t.closed_target, t.closed_then, t.holiday_kind, t.holiday_target, t.holiday_then,
		t.override, t.toggle_number FROM time_conditions t ORDER BY t.id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: time conditions: %w", err)
	}
	type row struct {
		tc    TimeCondition
		sched int64
	}
	var all []row
	for rows.Next() {
		var (
			r          row
			hk, ht, hn string
		)
		c := &r.tc
		if err := rows.Scan(&c.Name, &r.sched, &c.Open.Kind, &c.Open.Target, &c.Open.ThenVoicemail,
			&c.Closed.Kind, &c.Closed.Target, &c.Closed.ThenVoicemail, &hk, &ht, &hn, &c.Override, &c.ToggleNumber); err != nil {
			rows.Close()
			return nil, fmt.Errorf("snapshot: scan time condition: %w", err)
		}
		if hk != "" {
			c.Holiday = &timecond.Dest{Kind: hk, Target: ht, ThenVoicemail: hn}
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: time conditions: %w", err)
	}
	if len(all) == 0 {
		return nil, nil
	}
	specs := map[int64]*timecond.Spec{}
	srows, err := db.Query(ctx, `SELECT id, time_zone FROM schedules`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: schedules: %w", err)
	}
	for srows.Next() {
		var id int64
		sp := &timecond.Spec{}
		if err := srows.Scan(&id, &sp.TimeZone); err != nil {
			srows.Close()
			return nil, fmt.Errorf("snapshot: scan schedule: %w", err)
		}
		specs[id] = sp
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: schedules: %w", err)
	}
	rrows, err := db.Query(ctx, `SELECT schedule_id, weekday, start_time, end_time FROM schedule_ranges ORDER BY schedule_id, weekday, start_time`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: schedule ranges: %w", err)
	}
	for rrows.Next() {
		var id int64
		var r timecond.DayRange
		if err := rrows.Scan(&id, &r.Day, &r.Start, &r.End); err != nil {
			rrows.Close()
			return nil, fmt.Errorf("snapshot: scan schedule range: %w", err)
		}
		if sp := specs[id]; sp != nil {
			sp.Ranges = append(sp.Ranges, r)
		}
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: schedule ranges: %w", err)
	}
	erows, err := db.Query(ctx, `SELECT sc.schedule_id, c.name, e.label, to_char(e.start_date, 'YYYY-MM-DD'),
		to_char(e.end_date, 'YYYY-MM-DD'), e.yearly, e.all_day, e.start_time, e.end_time, e.mode, e.open_hours
		FROM schedule_calendars sc JOIN calendars c ON c.id = sc.calendar_id
		JOIN calendar_entries e ON e.calendar_id = c.id ORDER BY sc.schedule_id, c.name, e.id`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: calendar entries: %w", err)
	}
	for erows.Next() {
		var (
			id    int64
			ce    timecond.CalendarEntry
			hours []byte
		)
		if err := erows.Scan(&id, &ce.Calendar, &ce.Label, &ce.Start, &ce.End, &ce.Yearly, &ce.AllDay,
			&ce.StartTime, &ce.EndTime, &ce.Mode, &hours); err != nil {
			erows.Close()
			return nil, fmt.Errorf("snapshot: scan calendar entry: %w", err)
		}
		if len(hours) > 0 {
			if err := json.Unmarshal(hours, &ce.Hours); err != nil {
				erows.Close()
				return nil, fmt.Errorf("snapshot: calendar entry hours: %w", err)
			}
		}
		if ce.Yearly { // stored in the year 2000
			ce.Start, ce.End = ce.Start[5:], ce.End[5:]
		}
		if sp := specs[id]; sp != nil {
			sp.Entries = append(sp.Entries, ce)
		}
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: calendar entries: %w", err)
	}
	out := make([]TimeCondition, 0, len(all))
	for _, r := range all {
		if sp := specs[r.sched]; sp != nil {
			r.tc.Spec = *sp
		}
		out = append(out, r.tc)
	}
	return out, nil
}
