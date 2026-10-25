package timecond

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func at(t *testing.T, zone, local string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := time.ParseInLocation("2006-01-02 15:04", local, loc)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// weekdays Monday to Friday (1-5) 09:00-12:00 and 13:00-17:00.
func officeHours() []DayRange {
	var rs []DayRange
	for d := 1; d <= 5; d++ {
		rs = append(rs, DayRange{d, "09:00", "12:00"}, DayRange{d, "13:00", "17:00"})
	}
	return rs
}

// TestWeeklyRangesWithSeveralPerDay: two ranges on a day, the gap between
// them and the edges (start inclusive, end exclusive). It fails if lunch is
// open or the end minute still counts as open.
func TestWeeklyRangesWithSeveralPerDay(t *testing.T) {
	s := Spec{TimeZone: "Europe/Brussels", Ranges: officeHours()}
	for _, tc := range []struct {
		local string
		open  bool
	}{
		{"2026-06-01 08:59", false}, // Monday
		{"2026-06-01 09:00", true},
		{"2026-06-01 11:59", true},
		{"2026-06-01 12:00", false}, // lunch
		{"2026-06-01 12:59", false},
		{"2026-06-01 13:00", true},
		{"2026-06-01 16:59", true},
		{"2026-06-01 17:00", false},
		{"2026-06-06 10:00", false}, // Saturday
		{"2026-06-07 10:00", false}, // Sunday
	} {
		if v := s.Evaluate(at(t, s.TimeZone, tc.local)); v.Open != tc.open || v.Holiday {
			t.Errorf("%s: open=%v holiday=%v (%s), want open=%v", tc.local, v.Open, v.Holiday, v.Reason, tc.open)
		}
	}
}

// TestOvernightRange: 22:00-06:00 started on Friday is open until 06:00
// Saturday and not again on Saturday morning's own account. It fails if the
// part after midnight is dropped or leaks to the wrong day.
func TestOvernightRange(t *testing.T) {
	s := Spec{TimeZone: "Asia/Dubai", Ranges: []DayRange{{5, "22:00", "06:00"}}} // Friday night
	for _, tc := range []struct {
		local string
		open  bool
	}{
		{"2026-06-05 21:59", false}, // Friday
		{"2026-06-05 22:00", true},
		{"2026-06-05 23:59", true},
		{"2026-06-06 00:00", true}, // Saturday
		{"2026-06-06 05:59", true},
		{"2026-06-06 06:00", false},
		{"2026-06-06 22:30", false}, // Saturday night is not covered
		{"2026-06-05 03:00", false}, // Friday early morning belongs to Thursday's range
	} {
		if v := s.Evaluate(at(t, s.TimeZone, tc.local)); v.Open != tc.open {
			t.Errorf("%s: open=%v (%s), want %v", tc.local, v.Open, v.Reason, tc.open)
		}
	}
	// 24:00 ends at midnight; a full day is 00:00-24:00.
	all := Spec{TimeZone: "UTC", Ranges: []DayRange{{3, "00:00", "24:00"}}}
	if !all.Evaluate(at(t, "UTC", "2026-06-03 23:59")).Open || all.Evaluate(at(t, "UTC", "2026-06-04 00:00")).Open {
		t.Error("00:00-24:00 on Wednesday must end at midnight")
	}
}

// TestTimeZoneDecides: the same instant is open in one zone and closed in
// another. It fails if the schedule used UTC or the host zone.
func TestTimeZoneDecides(t *testing.T) {
	instant := time.Date(2026, 6, 1, 6, 30, 0, 0, time.UTC) // Monday 10:30 Dubai, 08:30 Brussels
	dubai := Spec{TimeZone: "Asia/Dubai", Ranges: officeHours()}
	brussels := Spec{TimeZone: "Europe/Brussels", Ranges: officeHours()}
	if !dubai.Evaluate(instant).Open || brussels.Evaluate(instant).Open {
		t.Fatal("zone not applied")
	}
	if v := (Spec{TimeZone: "Nowhere/City", Ranges: officeHours()}).Evaluate(instant); v.Open || !strings.Contains(v.Reason, "unknown time zone") {
		t.Errorf("bad zone = %+v", v)
	}
	if v := (Spec{Ranges: officeHours()}).Evaluate(instant); v.Open {
		t.Errorf("no zone = %+v", v)
	}
}

// TestDSTSpringForward: Brussels skips 02:00-03:00 on 2026-03-29. A Sunday
// range 02:00-03:30 is wall clock, so the first real minute after the jump
// (03:00) is inside it and 03:30 is not. It fails if the schedule worked in
// UTC offsets that do not move with the clock.
func TestDSTSpringForward(t *testing.T) {
	s := Spec{TimeZone: "Europe/Brussels", Ranges: []DayRange{{0, "02:00", "03:30"}}}
	for _, tc := range []struct {
		utc  string
		open bool
	}{
		{"2026-03-29 00:59", false}, // 01:59 CET
		{"2026-03-29 01:00", true},  // 03:00 CEST, right after the jump
		{"2026-03-29 01:29", true},  // 03:29 CEST
		{"2026-03-29 01:30", false}, // 03:30 CEST
	} {
		if v := s.Evaluate(at(t, "UTC", tc.utc)); v.Open != tc.open {
			t.Errorf("%s UTC: open=%v (%s) local %s, want %v", tc.utc, v.Open, v.Reason, v.Local, tc.open)
		}
	}
	// An overnight range across the jump stays open until its wall-clock end.
	night := Spec{TimeZone: "Europe/Brussels", Ranges: []DayRange{{6, "22:00", "06:00"}}} // Saturday night into Sunday
	for _, tc := range []struct {
		local string
		open  bool
	}{
		{"2026-03-29 03:30", true}, // the hour of 02:00 never existed
		{"2026-03-29 05:59", true},
		{"2026-03-29 06:00", false},
	} {
		if v := night.Evaluate(at(t, "Europe/Brussels", tc.local)); v.Open != tc.open {
			t.Errorf("%s: open=%v (%s), want %v", tc.local, v.Open, v.Reason, tc.open)
		}
	}
}

// TestDSTFallBack: Brussels repeats 02:00-03:00 on 2026-10-25. A Sunday
// range 02:00-03:00 is open both times 02:30 occurs, and 03:00 CET (the end)
// is closed. It fails if the repeated hour is skipped the second time.
func TestDSTFallBack(t *testing.T) {
	s := Spec{TimeZone: "Europe/Brussels", Ranges: []DayRange{{0, "02:00", "03:00"}}}
	for _, tc := range []struct {
		utc  string
		open bool
	}{
		{"2026-10-25 00:30", true},  // 02:30 CEST
		{"2026-10-25 01:30", true},  // 02:30 CET, the repeat
		{"2026-10-25 01:59", true},  // 02:59 CET
		{"2026-10-25 02:00", false}, // 03:00 CET
		{"2026-10-24 23:59", false}, // 01:59 CEST
	} {
		if v := s.Evaluate(at(t, "UTC", tc.utc)); v.Open != tc.open {
			t.Errorf("%s UTC: open=%v (%s) local %s, want %v", tc.utc, v.Open, v.Reason, v.Local, tc.open)
		}
	}
}

// TestHolidayOverridesWeekly: a closed entry beats the weekly hours for a
// date, a range, a yearly date, a yearly range that wraps the new year and
// part of a day. It fails if the weekly hours still open the day.
func TestHolidayOverridesWeekly(t *testing.T) {
	s := Spec{TimeZone: "Europe/Brussels", Ranges: officeHours(), Entries: []CalendarEntry{
		{"Belgium", Entry{Label: "Christmas Day", Start: "2026-12-25", End: "2026-12-25", AllDay: true, Mode: ModeClosed}},
		{"Belgium", Entry{Label: "Bridge", Start: "2026-05-14", End: "2026-05-15", AllDay: true, Mode: ModeClosed}},
		{"Belgium", Entry{Label: "National day", Start: "07-21", End: "07-21", Yearly: true, AllDay: true, Mode: ModeClosed}},
		{"Belgium", Entry{Label: "Year end", Start: "12-31", End: "01-02", Yearly: true, AllDay: true, Mode: ModeClosed}},
		{"Belgium", Entry{Label: "Staff meeting", Start: "2026-06-02", End: "2026-06-02", StartTime: "14:00", EndTime: "15:00", Mode: ModeClosed}},
	}}
	for _, tc := range []struct {
		local   string
		open    bool
		holiday bool
		reason  string
	}{
		{"2026-12-24 10:00", true, false, ""},          // Thursday, normal
		{"2026-12-25 10:00", false, true, "Christmas"}, // Friday, holiday
		{"2026-05-14 10:00", false, true, "Bridge"},
		{"2026-05-15 10:00", false, true, "Bridge"},
		{"2026-05-18 10:00", true, false, ""},
		{"2026-07-21 10:00", false, true, "National day"}, // Tuesday
		{"2027-07-21 10:00", false, true, "National day"}, // yearly: next year too
		{"2027-07-22 10:00", true, false, ""},
		{"2026-12-31 10:00", false, true, "Year end"}, // wraps the new year
		{"2027-01-01 10:00", false, true, "Year end"},
		{"2027-01-02 10:00", false, true, "Year end"}, // Saturday anyway, still a holiday
		{"2027-01-04 10:00", true, false, ""},         // Monday
		{"2026-06-02 13:59", true, false, ""},         // meeting: only 14:00-15:00
		{"2026-06-02 14:00", false, true, "Staff meeting"},
		{"2026-06-02 14:59", false, true, "Staff meeting"},
		{"2026-06-02 15:00", true, false, ""},
	} {
		v := s.Evaluate(at(t, s.TimeZone, tc.local))
		if v.Open != tc.open || v.Holiday != tc.holiday || !strings.Contains(v.Reason, tc.reason) {
			t.Errorf("%s: open=%v holiday=%v reason=%q, want open=%v holiday=%v ~%q", tc.local, v.Open, v.Holiday, v.Reason, tc.open, tc.holiday, tc.reason)
		}
	}
}

// TestHolidayOvernightWindow: a timed closed entry that crosses midnight
// closes the early hours of the next day too.
func TestHolidayOvernightWindow(t *testing.T) {
	s := Spec{TimeZone: "UTC", Ranges: []DayRange{{0, "00:00", "24:00"}, {1, "00:00", "24:00"}}, Entries: []CalendarEntry{
		{"c", Entry{Label: "maintenance", Start: "2026-06-07", End: "2026-06-07", StartTime: "23:00", EndTime: "02:00", Mode: ModeClosed}}}}
	for _, tc := range []struct {
		local string
		open  bool
	}{
		{"2026-06-07 22:59", true}, {"2026-06-07 23:00", false}, {"2026-06-08 01:59", false}, {"2026-06-08 02:00", true},
	} {
		if v := s.Evaluate(at(t, "UTC", tc.local)); v.Open != tc.open {
			t.Errorf("%s: open=%v (%s), want %v", tc.local, v.Open, v.Reason, tc.open)
		}
	}
}

// TestOpenEntryReplacesWeekly: an "open" entry swaps a day's hours: a normal
// weekday closes early on Christmas Eve, and a Sunday opens. Outside the
// special hours the day counts as a holiday. It fails if the weekly hours
// still apply on that day or the holiday flag is lost.
func TestOpenEntryReplacesWeekly(t *testing.T) {
	s := Spec{TimeZone: "Europe/Brussels", Ranges: officeHours(), Entries: []CalendarEntry{
		{"Belgium", Entry{Label: "Christmas Eve", Start: "12-24", End: "12-24", Yearly: true, AllDay: true, Mode: ModeOpen, Hours: []Range{{"09:00", "13:00"}}}},
		{"Belgium", Entry{Label: "Shopping Sunday", Start: "2026-11-29", End: "2026-11-29", AllDay: true, Mode: ModeOpen, Hours: []Range{{"10:00", "18:00"}}}},
	}}
	for _, tc := range []struct {
		local   string
		open    bool
		holiday bool
	}{
		{"2026-12-24 12:59", true, false},
		{"2026-12-24 13:00", false, true}, // normally open until 17:00
		{"2026-12-24 15:00", false, true},
		{"2026-12-23 15:00", true, false},
		{"2026-11-29 11:00", true, false}, // a Sunday, normally closed
		{"2026-11-29 19:00", false, true},
	} {
		v := s.Evaluate(at(t, s.TimeZone, tc.local))
		if v.Open != tc.open || v.Holiday != tc.holiday {
			t.Errorf("%s: open=%v holiday=%v (%s), want open=%v holiday=%v", tc.local, v.Open, v.Holiday, v.Reason, tc.open, tc.holiday)
		}
	}
}

// TestClosedBeatsOpenEntry: when a closed and an open entry cover the same
// day, closed wins.
func TestClosedBeatsOpenEntry(t *testing.T) {
	s := Spec{TimeZone: "UTC", Entries: []CalendarEntry{
		{"a", Entry{Label: "open", Start: "2026-06-01", End: "2026-06-01", AllDay: true, Mode: ModeOpen, Hours: []Range{{"00:00", "24:00"}}}},
		{"b", Entry{Label: "shut", Start: "2026-06-01", End: "2026-06-01", AllDay: true, Mode: ModeClosed}},
	}}
	if v := s.Evaluate(at(t, "UTC", "2026-06-01 10:00")); v.Open || !v.Holiday {
		t.Fatalf("verdict = %+v", v)
	}
}

func cond() Condition {
	holiday := Dest{Kind: KindAnnouncement, Target: "holiday-msg"}
	return Condition{
		Name: "office",
		Spec: Spec{TimeZone: "Europe/Brussels", Ranges: officeHours(), Entries: []CalendarEntry{
			{"Belgium", Entry{Label: "Christmas Day", Start: "2026-12-25", End: "2026-12-25", AllDay: true, Mode: ModeClosed}}}},
		Open:    Dest{Kind: KindRingGroup, Target: "sales"},
		Closed:  Dest{Kind: KindAnnouncement, Target: "closed-msg", ThenVoicemail: "600"},
		Holiday: &holiday,
	}
}

// TestDecideBranchesAndOverride: automatic branches, the holiday fallback to
// closed, and the manual override beating a holiday and the schedule alike.
func TestDecideBranchesAndOverride(t *testing.T) {
	c := cond()
	open, closed, holiday := at(t, "Europe/Brussels", "2026-06-01 10:00"), at(t, "Europe/Brussels", "2026-06-01 20:00"), at(t, "Europe/Brussels", "2026-12-25 10:00")
	for _, tc := range []struct {
		name     string
		override string
		when     time.Time
		branch   string
		forced   bool
		dest     string
	}{
		{"open", "", open, BranchOpen, false, "ring_group sales"},
		{"closed", OverrideAuto, closed, BranchClosed, false, "announcement closed-msg then voicemail 600"},
		{"holiday", "", holiday, BranchHoliday, false, "announcement holiday-msg"},
		{"forced closed in hours", OverrideClosed, open, BranchClosed, true, "announcement closed-msg then voicemail 600"},
		{"forced open after hours", OverrideOpen, closed, BranchOpen, true, "ring_group sales"},
		{"forced open on a holiday", OverrideOpen, holiday, BranchOpen, true, "ring_group sales"},
		{"forced closed on a holiday is closed, not holiday", OverrideClosed, holiday, BranchClosed, true, "announcement closed-msg then voicemail 600"},
	} {
		c.Override = tc.override
		d := c.Decide(tc.when)
		if d.Branch != tc.branch || d.Forced != tc.forced || d.Dest.String() != tc.dest {
			t.Errorf("%s: %+v", tc.name, d)
		}
		if tc.forced && !strings.Contains(d.Why, "override") {
			t.Errorf("%s: why = %q", tc.name, d.Why)
		}
	}
	// Without a holiday destination a holiday goes to closed.
	c.Override, c.Holiday = "", nil
	if d := c.Decide(holiday); d.Branch != BranchHoliday || d.Dest.Target != "closed-msg" {
		t.Errorf("holiday without destination: %+v", d)
	}
}

func chain(m map[string]Condition) func(string) (Condition, bool) {
	return func(n string) (Condition, bool) { c, ok := m[n]; return c, ok }
}

// TestResolveChain: a condition naming another is followed to a real
// destination; each step is reported. A loop, a missing condition and a
// chain longer than MaxDepth fail with their own error.
func TestResolveChain(t *testing.T) {
	now := at(t, "UTC", "2026-06-01 10:00")
	always := Spec{TimeZone: "UTC", Ranges: []DayRange{{1, "00:00", "24:00"}}}
	never := Spec{TimeZone: "UTC", Ranges: []DayRange{{2, "00:00", "24:00"}}} // not on Monday
	m := map[string]Condition{
		"a": {Name: "a", Spec: always, Open: Dest{Kind: KindTimeCondition, Target: "b"}, Closed: Dest{Kind: KindHangup}},
		"b": {Name: "b", Spec: never, Open: Dest{Kind: KindHangup}, Closed: Dest{Kind: KindVoicemail, Target: "100"}},
	}
	res, err := Resolve(chain(m), "a", now)
	if err != nil || res.Dest.Kind != KindVoicemail || res.Dest.Target != "100" || len(res.Steps) != 2 ||
		res.Steps[0].Condition != "a" || res.Steps[1].Branch != BranchClosed {
		t.Fatalf("res = %+v, %v", res, err)
	}
	// Loop: b's closed branch leads back to a.
	m["b"] = Condition{Name: "b", Spec: never, Closed: Dest{Kind: KindTimeCondition, Target: "a"}}
	if res, err := Resolve(chain(m), "a", now); !errors.Is(err, ErrLoop) || len(res.Steps) != 2 {
		t.Fatalf("loop: %+v, %v", res, err)
	}
	// Visited from an earlier feature (a ring group failover) counts.
	if _, err := Resolve(chain(m), "a", now, "a"); !errors.Is(err, ErrLoop) {
		t.Fatalf("visited: %v", err)
	}
	// Missing.
	m["b"] = Condition{Name: "b", Spec: never, Closed: Dest{Kind: KindTimeCondition, Target: "ghost"}}
	if _, err := Resolve(chain(m), "a", now); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing: %v", err)
	}
	// Too deep without repeating: c0 -> c1 -> ... -> c9.
	deep := map[string]Condition{}
	for i := 0; i < 10; i++ {
		n := "c" + string(rune('0'+i))
		next := "c" + string(rune('1'+i))
		deep[n] = Condition{Name: n, Spec: always, Open: Dest{Kind: KindTimeCondition, Target: next}}
	}
	if _, err := Resolve(chain(deep), "c0", now); !errors.Is(err, ErrLoop) {
		t.Fatalf("deep: %v", err)
	}
}

// TestValidation: the checks the store and API rely on.
func TestValidation(t *testing.T) {
	if len(ValidateRanges("ranges", officeHours())) != 0 {
		t.Fatal("office hours rejected")
	}
	for name, rs := range map[string][]DayRange{
		"bad day":           {{7, "09:00", "10:00"}},
		"bad start":         {{1, "9:00", "10:00"}},
		"bad end":           {{1, "09:00", "25:00"}},
		"24:00 start":       {{1, "24:00", "10:00"}},
		"empty":             {{1, "09:00", "09:00"}},
		"overlap":           {{1, "09:00", "12:00"}, {1, "11:00", "13:00"}},
		"overnight overlap": {{1, "22:00", "06:00"}, {1, "23:00", "23:30"}},
	} {
		if len(ValidateRanges("ranges", rs)) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
	if ps := ValidateRanges("r", []DayRange{{1, "09:00", "12:00"}, {1, "12:00", "17:00"}, {2, "22:00", "06:00"}, {3, "00:00", "24:00"}}); len(ps) != 0 {
		t.Errorf("touching and overnight ranges rejected: %v", ps)
	}
	if !ValidZone("Europe/Brussels") || ValidZone("Local") || ValidZone("") || ValidZone("Nowhere/City") {
		t.Error("ValidZone")
	}

	good := Entry{Label: "x", Start: "2026-12-25"}
	if ps := good.Normalize("e"); len(ps) != 0 || good.End != "2026-12-25" || good.Mode != ModeClosed || !good.AllDay {
		t.Errorf("defaults: %+v %v", good, ps)
	}
	yearly := Entry{Start: "2026-12-25", Yearly: true}
	if ps := yearly.Normalize("e"); len(ps) != 0 || yearly.Start != "12-25" || yearly.End != "12-25" {
		t.Errorf("yearly: %+v %v", yearly, ps)
	}
	leap := Entry{Start: "02-29", Yearly: true}
	if ps := leap.Normalize("e"); len(ps) != 0 {
		t.Errorf("02-29 yearly: %v", ps)
	}
	for name, e := range map[string]Entry{
		"no date":         {},
		"bad date":        {Start: "2026-13-01"},
		"end before":      {Start: "2026-12-25", End: "2026-12-24"},
		"not yearly form": {Start: "12-25"},
		"yearly form":     {Start: "2026-12-25", Yearly: true, End: "13-01"},
		"bad mode":        {Start: "2026-12-25", Mode: "x"},
		"half a window":   {Start: "2026-12-25", StartTime: "10:00"},
		"empty window":    {Start: "2026-12-25", StartTime: "10:00", EndTime: "10:00"},
		"open w/o hours":  {Start: "2026-12-25", Mode: ModeOpen},
		"open bad hours":  {Start: "2026-12-25", Mode: ModeOpen, Hours: []Range{{"9", "10"}}},
		"long label":      {Start: "2026-12-25", Label: strings.Repeat("x", 121)},
	} {
		if ps := e.Normalize("e"); len(ps) == 0 {
			t.Errorf("%s accepted: %+v", name, e)
		}
	}
	timed := Entry{Start: "2026-12-25", StartTime: "10:00", EndTime: "24:00"}
	if ps := timed.Normalize("e"); len(ps) != 0 || timed.AllDay {
		t.Errorf("timed: %+v %v", timed, ps)
	}
}
