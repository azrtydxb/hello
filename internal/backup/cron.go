package backup

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a five-field cron expression (minute hour day-of-month month
// day-of-week) with numbers, "*", lists, ranges and "/step". As in cron, a
// day matches when both day fields are "*", or when either restricted field
// matches.
type Schedule struct {
	min, hour, dom, mon, dow uint64
	domStar, dowStar         bool
}

// ParseSchedule parses a cron expression.
func ParseSchedule(expr string) (Schedule, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Schedule{}, errors.New("cron must have five fields: minute hour day-of-month month day-of-week")
	}
	var s Schedule
	var err error
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	var sets [5]uint64
	for i, field := range f {
		sets[i], err = parseField(field, bounds[i][0], bounds[i][1])
		if err != nil {
			return Schedule{}, fmt.Errorf("cron field %d (%q): %w", i+1, field, err)
		}
	}
	// 7 is Sunday too.
	if sets[4]&(1<<7) != 0 {
		sets[4] |= 1
	}
	s.min, s.hour, s.dom, s.mon, s.dow = sets[0], sets[1], sets[2], sets[3], sets[4]&0x7f
	s.domStar, s.dowStar = strings.HasPrefix(f[2], "*"), strings.HasPrefix(f[4], "*")
	return s, nil
}

func parseField(field string, lo, hi int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(field, ",") {
		rng, step := part, 1
		if r, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n < 1 {
				return 0, errors.New("bad step")
			}
			rng, step = r, n
		}
		a, b := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			x, y, _ := strings.Cut(rng, "-")
			var e1, e2 error
			a, e1 = strconv.Atoi(x)
			b, e2 = strconv.Atoi(y)
			if e1 != nil || e2 != nil {
				return 0, errors.New("bad range")
			}
		default:
			n, err := strconv.Atoi(rng)
			if err != nil {
				return 0, errors.New("not a number")
			}
			a, b = n, n
			if strings.Contains(part, "/") {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("out of range %d-%d", lo, hi)
		}
		for v := a; v <= b; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

func (s Schedule) dayMatches(t time.Time) bool {
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	}
	return dom || dow
}

func (s Schedule) matches(t time.Time) bool {
	return s.min&(1<<uint(t.Minute())) != 0 && s.hour&(1<<uint(t.Hour())) != 0 &&
		s.mon&(1<<uint(t.Month())) != 0 && s.dayMatches(t)
}

// Prev returns the latest firing time at or before t (minute resolution, in
// t's location), or the zero time when none falls in the last year.
func (s Schedule) Prev(t time.Time) time.Time {
	t = t.Truncate(time.Minute)
	for i := 0; i < 366*24*60; i++ {
		if s.matches(t) {
			return t
		}
		t = t.Add(-time.Minute)
	}
	return time.Time{}
}

// Next returns the first firing time after t, or the zero time when none
// falls in the next year.
func (s Schedule) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 366*24*60; i++ {
		if s.matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}
