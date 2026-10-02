package routing

import (
	"fmt"
	"time"
)

// Schedule semantics: each window opens at Start (inclusive) and closes at
// End (exclusive), compared to the minute in the schedule's time zone on
// the wall clock, so DST needs no special case: a window over a skipped
// hour simply has fewer minutes that day, and a repeated hour is inside
// the window both times. Days are the days a window starts on. A window
// with End <= Start crosses midnight and stays open until End on the next
// day (Start == End is therefore a 24-hour window). A schedule with an
// invalid zone or window is never open; Compile rejects such schedules.

// compiledSchedule is a validated Schedule with its location loaded once.
type compiledSchedule struct {
	zone    string
	loc     *time.Location
	windows []compiledWindow
}

type compiledWindow struct {
	days       uint8 // bit d set: the window starts on weekday d
	start, end int   // minutes since local midnight
}

// Open reports whether the schedule is open at the instant at. A nil
// schedule is always open.
func (s *Schedule) Open(at time.Time) bool {
	if s == nil {
		return true
	}
	var errs []FieldError
	cs, ok := compileSchedule(*s, "schedule", &errs)
	if !ok {
		return false
	}
	return cs.open(at)
}

func compileSchedule(s Schedule, path string, errs *[]FieldError) (*compiledSchedule, bool) {
	n := len(*errs)
	cs := &compiledSchedule{zone: s.TimeZone}
	switch s.TimeZone {
	case "":
		addErr(errs, path+".timeZone", "is required (an IANA name such as Asia/Dubai)")
	case "Local":
		addErr(errs, path+".timeZone", `"Local" depends on the host; use an IANA name`)
	default:
		loc, err := time.LoadLocation(s.TimeZone)
		if err != nil {
			addErr(errs, path+".timeZone", fmt.Sprintf("unknown time zone %s", quote(s.TimeZone)))
		}
		cs.loc = loc
	}
	if len(s.Windows) == 0 {
		addErr(errs, path+".windows", "at least one window is required (disable the route instead)")
	}
	for i, w := range s.Windows {
		wp := fmt.Sprintf("%s.windows[%d]", path, i)
		var cw compiledWindow
		if len(w.Days) == 0 {
			addErr(errs, wp+".days", "at least one day is required")
		}
		for j, d := range w.Days {
			if d < time.Sunday || d > time.Saturday {
				addErr(errs, fmt.Sprintf("%s.days[%d]", wp, j), "must be 0 (Sunday) to 6 (Saturday)")
				continue
			}
			cw.days |= 1 << uint(d)
		}
		var ok bool
		if cw.start, ok = parseHHMM(w.Start); !ok {
			addErr(errs, wp+".start", `must be "HH:MM" (00:00 to 23:59)`)
		}
		if cw.end, ok = parseHHMM(w.End); !ok {
			addErr(errs, wp+".end", `must be "HH:MM" (00:00 to 23:59)`)
		}
		cs.windows = append(cs.windows, cw)
	}
	return cs, len(*errs) == n
}

func parseHHMM(s string) (int, bool) {
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
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func (cs *compiledSchedule) open(at time.Time) bool {
	lt := at.In(cs.loc)
	wd := lt.Weekday()
	prev := (wd + 6) % 7
	m := lt.Hour()*60 + lt.Minute()
	for _, w := range cs.windows {
		today := w.days&(1<<uint(wd)) != 0
		if w.end > w.start {
			if today && m >= w.start && m < w.end {
				return true
			}
			continue
		}
		// Crosses midnight: open from start today, or until end if the
		// window started yesterday.
		if (today && m >= w.start) || (w.days&(1<<uint(prev)) != 0 && m < w.end) {
			return true
		}
	}
	return false
}

// local renders at in the schedule's zone for the trace, e.g.
// "Fri 18:30 Asia/Dubai".
func (cs *compiledSchedule) local(at time.Time) string {
	return at.In(cs.loc).Format("Mon 15:04") + " " + cs.zone
}
