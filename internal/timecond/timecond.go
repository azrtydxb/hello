// Package timecond decides whether a business is open at an instant and
// where a time condition sends a call. It is pure: no clock, no database.
// The store, the snapshot and the evaluate endpoint all call it, so the
// answer an operator sees in the UI is the answer a call gets.
//
// Semantics, in the order they apply:
//
//  1. A calendar entry in mode "closed" that covers the instant closes it
//     (a holiday).
//  2. Otherwise the hours of the local day come from the calendar entries in
//     mode "open" that cover the day (they replace the weekly hours), or, with
//     none, from the schedule's weekly ranges.
//  3. The instant is open when it lies in the day's hours, or in the part of
//     yesterday's hours that crosses midnight.
//
// Everything is compared as wall clock in the schedule's zone, so daylight
// saving needs no special case: a range over a skipped hour simply has fewer
// real minutes that day, and a repeated hour is inside the range both times.
// A manual override (open or closed) beats the verdict.
package timecond

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Branches of a time condition.
const (
	BranchOpen    = "open"
	BranchClosed  = "closed"
	BranchHoliday = "holiday"
)

// Manual override values.
const (
	OverrideAuto   = "auto"
	OverrideOpen   = "open"
	OverrideClosed = "closed"
)

// Calendar entry modes.
const (
	ModeClosed = "closed"
	ModeOpen   = "open"
)

// Destination kinds. They are the IVR destination kinds plus another time
// condition.
const (
	KindHangup        = "hangup"
	KindExtension     = "extension"
	KindRingGroup     = "ring_group"
	KindVoicemail     = "voicemail"
	KindAnnouncement  = "announcement"
	KindPaging        = "paging"
	KindIVR           = "ivr"
	KindExternal      = "external"
	KindQueue         = "queue"
	KindTimeCondition = "time_condition"
)

// MaxDepth bounds a chain of time conditions that name each other; the
// store refuses such chains that loop, this bound is the runtime guard.
const MaxDepth = 8

// Range is a span of one day in "HH:MM". End may be "24:00" (the end of the
// day); an End before Start crosses midnight.
type Range struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// DayRange is a weekly open range: Day 0 is Sunday. A range with End before
// Start runs on into the next day.
type DayRange struct {
	Day   int    `json:"day"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// Entry is one exception of a calendar. Start and End are "YYYY-MM-DD" (End
// is the last day, inclusive), or "MM-DD" when Yearly is set (the entry then
// repeats every year and a range may wrap the new year). AllDay covers whole
// days; otherwise StartTime and EndTime bound the part of each day covered.
// Mode "closed" closes the covered time; mode "open" replaces the weekly
// hours of the covered days with Hours.
type Entry struct {
	ID        int64   `json:"id,omitempty"`
	Label     string  `json:"label"`
	Start     string  `json:"start"`
	End       string  `json:"end"`
	Yearly    bool    `json:"yearly"`
	AllDay    bool    `json:"allDay"`
	StartTime string  `json:"startTime"`
	EndTime   string  `json:"endTime"`
	Mode      string  `json:"mode"`
	Hours     []Range `json:"hours"`
}

// CalendarEntry is an entry with the name of the calendar it belongs to.
type CalendarEntry struct {
	Calendar string
	Entry
}

// Spec is what a schedule evaluates: its zone, weekly ranges and the
// entries of every calendar it consults.
type Spec struct {
	TimeZone string
	Ranges   []DayRange
	Entries  []CalendarEntry
}

// Verdict is the schedule's answer for an instant.
type Verdict struct {
	// Open reports whether the schedule is open.
	Open bool `json:"open"`
	// Holiday is set when a calendar entry made it closed (or, with
	// "open" entries, closed outside their hours).
	Holiday bool `json:"holiday"`
	// Reason says why, in words for the trace and the UI.
	Reason string `json:"reason"`
	// Local is the instant in the schedule's zone.
	Local string `json:"local"`
}

// Dest is where a branch sends the call. ThenVoicemail, for an announcement,
// names the extension whose voicemail box takes the call after the
// announcement.
type Dest struct {
	Kind          string `json:"kind"`
	Target        string `json:"target"`
	ThenVoicemail string `json:"thenVoicemail,omitempty"`
}

// String renders the destination for traces.
func (d Dest) String() string {
	s := d.Kind
	if d.Target != "" {
		s += " " + d.Target
	}
	if d.ThenVoicemail != "" {
		s += " then voicemail " + d.ThenVoicemail
	}
	return s
}

// Condition is a time condition ready to decide.
type Condition struct {
	Name     string
	Spec     Spec
	Override string // "", auto, open or closed
	Open     Dest
	Closed   Dest
	// Holiday is the destination of a holiday; nil uses Closed.
	Holiday *Dest
}

// Decision is the outcome of one time condition at one instant.
type Decision struct {
	Branch string `json:"branch"`
	Why    string `json:"why"`
	Forced bool   `json:"forced"`
	Dest   Dest   `json:"destination"`
	// Verdict is the schedule's own answer, also when an override
	// replaced it, so the UI can show both.
	Verdict Verdict `json:"verdict"`
}

// Decide picks the branch and destination at the instant at.
func (c Condition) Decide(at time.Time) Decision {
	v := c.Spec.Evaluate(at)
	d := Decision{Verdict: v}
	switch c.Override {
	case OverrideOpen:
		d.Branch, d.Forced, d.Why = BranchOpen, true, "manual override: forced open"
	case OverrideClosed:
		d.Branch, d.Forced, d.Why = BranchClosed, true, "manual override: forced closed"
	default:
		d.Why = v.Reason
		switch {
		case v.Open:
			d.Branch = BranchOpen
		case v.Holiday:
			d.Branch = BranchHoliday
		default:
			d.Branch = BranchClosed
		}
	}
	switch d.Branch {
	case BranchOpen:
		d.Dest = c.Open
	case BranchHoliday:
		d.Dest = c.Closed
		if c.Holiday != nil {
			d.Dest = *c.Holiday
		}
	default:
		d.Dest = c.Closed
	}
	return d
}

// Step is one time condition passed on the way to a destination.
type Step struct {
	Condition string `json:"condition"`
	Branch    string `json:"branch"`
	Why       string `json:"why"`
	Forced    bool   `json:"forced"`
	Dest      Dest   `json:"destination"`
}

// Resolution is a chain of time conditions followed to a destination that is
// not itself a time condition.
type Resolution struct {
	Steps []Step `json:"steps"`
	Dest  Dest   `json:"destination"`
}

// Resolve errors.
var (
	ErrMissing = errors.New("time condition does not exist")
	ErrLoop    = errors.New("time conditions lead back to each other")
)

// Resolve follows name and the time conditions it names until a real
// destination is reached. visited lists conditions already passed in this
// call (so a loop through another feature is caught too); the chain's own
// steps are added to it by the caller if it resumes. The returned steps are
// filled up to the failure when err is not nil.
func Resolve(lookup func(name string) (Condition, bool), name string, at time.Time, visited ...string) (Resolution, error) {
	var res Resolution
	seen := map[string]bool{}
	for _, v := range visited {
		seen[v] = true
	}
	cur := name
	for depth := 0; ; depth++ {
		if depth >= MaxDepth {
			return res, fmt.Errorf("%w (more than %d in a row)", ErrLoop, MaxDepth)
		}
		if seen[cur] {
			return res, fmt.Errorf("%w (%s twice)", ErrLoop, cur)
		}
		seen[cur] = true
		c, ok := lookup(cur)
		if !ok {
			return res, fmt.Errorf("%w: %s", ErrMissing, cur)
		}
		d := c.Decide(at)
		res.Steps = append(res.Steps, Step{Condition: cur, Branch: d.Branch, Why: d.Why, Forced: d.Forced, Dest: d.Dest})
		if d.Dest.Kind != KindTimeCondition {
			res.Dest = d.Dest
			return res, nil
		}
		cur = d.Dest.Target
	}
}

// --- evaluation -------------------------------------------------------------

type day struct {
	y int
	m time.Month
	d int
}

func dayOf(t time.Time) day { return day{t.Year(), t.Month(), t.Day()} }

func (d day) at() time.Time { return time.Date(d.y, d.m, d.d, 12, 0, 0, 0, time.UTC) }

func (d day) prev() day { return dayOf(d.at().AddDate(0, 0, -1)) }

func (d day) weekday() int { return int(d.at().Weekday()) }

func (d day) key() string { return fmt.Sprintf("%04d-%02d-%02d", d.y, int(d.m), d.d) }

func (d day) md() string { return fmt.Sprintf("%02d-%02d", int(d.m), d.d) }

// minutes parses "HH:MM" (and "24:00" when allow24) to minutes since midnight.
func minutes(s string, allow24 bool) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if m > 59 || h > 24 || (h == 24 && (m != 0 || !allow24)) {
		return 0, false
	}
	return h*60 + m, true
}

type span struct{ start, end int }

func (s span) overnight() bool { return s.end < s.start }

func (s span) String() string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", s.start/60, s.start%60, s.end/60, s.end%60)
}

// startsIn reports whether m lies in the part of the span that falls on the
// day it starts on.
func (s span) startsIn(m int) bool {
	if s.overnight() {
		return m >= s.start
	}
	return m >= s.start && m < s.end
}

// spillsInto reports whether m lies in the part that runs into the next day.
func (s span) spillsInto(m int) bool { return s.overnight() && m < s.end }

func toSpan(start, end string) (span, bool) {
	s, ok1 := minutes(start, false)
	e, ok2 := minutes(end, true)
	if !ok1 || !ok2 || s == e {
		return span{}, false
	}
	return span{s, e}, true
}

// coversDay reports whether the entry's dates include the day.
func (e Entry) coversDay(d day) bool {
	if e.Yearly {
		md := d.md()
		if e.Start <= e.End {
			return md >= e.Start && md <= e.End
		}
		return md >= e.Start || md <= e.End // wraps the new year
	}
	k := d.key()
	return k >= e.Start && k <= e.End
}

// coversInstant reports whether a closed entry covers minute m of today.
func (e Entry) coversInstant(today, yest day, m int) bool {
	if e.AllDay {
		return e.coversDay(today)
	}
	sp, ok := toSpan(e.StartTime, e.EndTime)
	if !ok {
		return false
	}
	return (e.coversDay(today) && sp.startsIn(m)) || (e.coversDay(yest) && sp.spillsInto(m))
}

// describe names an entry for a reason.
func (e CalendarEntry) describe() string {
	name := e.Label
	if name == "" {
		name = e.Start
		if e.End != e.Start {
			name += ".." + e.End
		}
	}
	return fmt.Sprintf("%s (calendar %s)", name, e.Calendar)
}

// hoursOf returns the hours of a day: those of the open entries covering it,
// else the weekly ranges. via names the entries when they replaced the week.
func (s Spec) hoursOf(d day) (spans []span, via []CalendarEntry) {
	for _, e := range s.Entries {
		if e.Mode != ModeOpen || !e.coversDay(d) {
			continue
		}
		via = append(via, e)
		for _, h := range e.Hours {
			if sp, ok := toSpan(h.Start, h.End); ok {
				spans = append(spans, sp)
			}
		}
	}
	if len(via) > 0 {
		return spans, via
	}
	wd := d.weekday()
	for _, r := range s.Ranges {
		if r.Day != wd {
			continue
		}
		if sp, ok := toSpan(r.Start, r.End); ok {
			spans = append(spans, sp)
		}
	}
	return spans, nil
}

// Evaluate answers whether the schedule is open at the instant at. A spec
// with an unusable zone is never open.
func (s Spec) Evaluate(at time.Time) Verdict {
	loc, err := loadZone(s.TimeZone)
	if err != nil {
		return Verdict{Reason: err.Error() + "; treated as closed"}
	}
	lt := at.In(loc)
	v := Verdict{Local: lt.Format("Mon 2006-01-02 15:04") + " " + s.TimeZone}
	today := dayOf(lt)
	yest := today.prev()
	m := lt.Hour()*60 + lt.Minute()

	for _, e := range s.Entries {
		if e.Mode == ModeOpen {
			continue
		}
		if e.coversInstant(today, yest, m) {
			v.Holiday = true
			v.Reason = "closed for " + e.describe()
			return v
		}
	}

	spans, via := s.hoursOf(today)
	for _, sp := range spans {
		if sp.startsIn(m) {
			v.Open = true
			v.Reason = rangeReason(sp, via, false)
			return v
		}
	}
	yspans, yvia := s.hoursOf(yest)
	for _, sp := range yspans {
		if sp.spillsInto(m) {
			v.Open = true
			v.Reason = rangeReason(sp, yvia, true)
			return v
		}
	}
	if len(via) > 0 {
		v.Holiday = true
		v.Reason = "outside the special hours of " + via[0].describe()
		return v
	}
	if len(spans) == 0 && len(yspans) == 0 {
		v.Reason = "no open hours on " + lt.Weekday().String()
	} else {
		v.Reason = "outside the weekly hours on " + lt.Weekday().String()
	}
	return v
}

func rangeReason(sp span, via []CalendarEntry, spill bool) string {
	what := "weekly hours"
	if len(via) > 0 {
		what = "special hours of " + via[0].describe()
	}
	if spill {
		return fmt.Sprintf("open: %s %s, which run past midnight", what, sp)
	}
	return fmt.Sprintf("open: within %s %s", what, sp)
}

func loadZone(name string) (*time.Location, error) {
	switch name {
	case "":
		return nil, errors.New("no time zone")
	case "Local":
		return nil, errors.New(`time zone "Local" depends on the host`)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	return loc, nil
}

// --- validation -------------------------------------------------------------

// Problem is one invalid field: Path is relative to the value validated.
type Problem struct {
	Path string
	Msg  string
}

// ValidZone reports whether name is an IANA zone usable for a schedule.
func ValidZone(name string) bool {
	_, err := loadZone(name)
	return err == nil
}

// ValidateRange checks "HH:MM" and "24:00" ends, and that the range is not
// empty (a start equal to the end is ambiguous; use 00:00-24:00 for a day).
func ValidateRange(path, start, end string) []Problem {
	var ps []Problem
	s, ok := minutes(start, false)
	if !ok {
		ps = append(ps, Problem{path + ".start", `must be "HH:MM" (00:00 to 23:59)`})
	}
	e, ok2 := minutes(end, true)
	if !ok2 {
		ps = append(ps, Problem{path + ".end", `must be "HH:MM" (00:00 to 24:00)`})
	}
	if ok && ok2 && s == e {
		ps = append(ps, Problem{path + ".end", `must differ from the start (use 00:00 to 24:00 for the whole day)`})
	}
	return ps
}

// ValidateRanges checks the weekly ranges and that ranges of one day do not
// overlap each other (an overnight range counts for both its days).
func ValidateRanges(path string, rs []DayRange) []Problem {
	var ps []Problem
	for i, r := range rs {
		p := fmt.Sprintf("%s[%d]", path, i)
		if r.Day < 0 || r.Day > 6 {
			ps = append(ps, Problem{p + ".day", "must be 0 (Sunday) to 6 (Saturday)"})
		}
		ps = append(ps, ValidateRange(p, r.Start, r.End)...)
	}
	if len(ps) > 0 {
		return ps
	}
	// Overlap check per weekday on the day each range starts on.
	for i, a := range rs {
		sa, _ := toSpan(a.Start, a.End)
		for j := i + 1; j < len(rs); j++ {
			b := rs[j]
			if a.Day != b.Day {
				continue
			}
			sb, _ := toSpan(b.Start, b.End)
			if overlaps(sa, sb) {
				ps = append(ps, Problem{fmt.Sprintf("%s[%d]", path, j), fmt.Sprintf("overlaps %s on the same day", sa)})
			}
		}
	}
	return ps
}

func overlaps(a, b span) bool {
	ae, be := a.end, b.end
	if a.overnight() {
		ae = 1440
	}
	if b.overnight() {
		be = 1440
	}
	return a.start < be && b.start < ae
}

// Normalize fills the defaults of an entry (mode closed, all-day unless
// times are given) and canonicalises it; it reports whether e is a valid
// entry through the problems. Yearly dates keep "MM-DD" form; a yearly entry
// given as "YYYY-MM-DD" is cut down to it.
func (e *Entry) Normalize(path string) []Problem {
	var ps []Problem
	if e.Mode == "" {
		e.Mode = ModeClosed
	}
	if e.Mode != ModeClosed && e.Mode != ModeOpen {
		ps = append(ps, Problem{path + ".mode", `must be "closed" or "open"`})
	}
	if len(e.Label) > 120 {
		ps = append(ps, Problem{path + ".label", "must be at most 120 characters"})
	}
	if e.Yearly {
		e.Start, e.End = yearlyForm(e.Start), yearlyForm(e.End)
		if e.End == "" {
			e.End = e.Start
		}
	} else if e.End == "" {
		e.End = e.Start
	}
	check := func(field, v string) bool {
		in := v
		if e.Yearly {
			in = "2000-" + v // 2000 is a leap year: 02-29 is valid
		}
		_, err := time.Parse("2006-01-02", in)
		if err != nil || (e.Yearly && len(v) != 5) || (!e.Yearly && len(v) != 10) {
			want := "YYYY-MM-DD"
			if e.Yearly {
				want = "MM-DD"
			}
			ps = append(ps, Problem{path + "." + field, "must be a date as " + want})
			return false
		}
		return true
	}
	okS, okE := check("start", e.Start), check("end", e.End)
	if okS && okE && !e.Yearly && e.End < e.Start {
		ps = append(ps, Problem{path + ".end", "must not be before the start"})
	}
	switch e.Mode {
	case ModeClosed:
		e.Hours = []Range{}
		if e.StartTime == "" && e.EndTime == "" {
			e.AllDay = true
		}
		if e.AllDay {
			e.StartTime, e.EndTime = "", ""
		} else {
			// The range problems belong to the entry's time fields.
			for _, p := range ValidateRange(path, e.StartTime, e.EndTime) {
				p.Path = strings.Replace(p.Path, path+".start", path+".startTime", 1)
				p.Path = strings.Replace(p.Path, path+".end", path+".endTime", 1)
				ps = append(ps, p)
			}
		}
	case ModeOpen:
		e.AllDay, e.StartTime, e.EndTime = true, "", ""
		if len(e.Hours) == 0 {
			ps = append(ps, Problem{path + ".hours", `an "open" entry needs at least one range of hours`})
		}
		if len(e.Hours) > 8 {
			ps = append(ps, Problem{path + ".hours", "at most 8 ranges"})
		}
		for i, h := range e.Hours {
			ps = append(ps, ValidateRange(fmt.Sprintf("%s.hours[%d]", path, i), h.Start, h.End)...)
		}
	}
	if e.Hours == nil {
		e.Hours = []Range{}
	}
	return ps
}

// yearlyForm cuts "YYYY-MM-DD" down to "MM-DD" and leaves other input as is.
func yearlyForm(s string) string {
	if len(s) == 10 && s[4] == '-' && s[7] == '-' {
		return s[5:]
	}
	return s
}
