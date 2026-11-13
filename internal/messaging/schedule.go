package messaging

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Route kinds: where a number's new conversations go (spec M-14).
const (
	// RouteInbox is the unassigned pool every supervisor sees.
	RouteInbox = "inbox"
	// RouteQueue assigns through a queue's strategy (target: queue id).
	RouteQueue = "queue"
	// RouteUser assigns to one user (target: user id).
	RouteUser = "user"
	// RouteExtension assigns to the user an extension is linked to (target:
	// extension id).
	RouteExtension = "extension"
	// RouteRingGroup is a shared pool of the users of a ring group's
	// extensions (target: ring group id).
	RouteRingGroup = "ring_group"
)

// RouteKinds lists the route kinds in the order a console shows them.
var RouteKinds = []string{RouteInbox, RouteQueue, RouteUser, RouteExtension, RouteRingGroup}

// ValidRouteKind reports whether k is a route kind.
func ValidRouteKind(k string) bool { return slices.Contains(RouteKinds, k) }

// After-hours actions: what a number does with a conversation that starts
// outside its schedule.
const (
	// AfterNone routes it as usual.
	AfterNone = "none"
	// AfterReply sends the configured text, then routes as usual.
	AfterReply = "reply"
	// AfterRoute sends it to the after-hours target instead.
	AfterRoute = "route"
)

// ValidAfterHours reports whether a is an after-hours action.
func ValidAfterHours(a string) bool { return a == AfterNone || a == AfterReply || a == AfterRoute }

// Schedule is a weekly opening schedule: the number is open in the listed
// windows. An empty schedule is always open. (The shape matches the chat
// widget's opening hours.)
type Schedule struct {
	Zone string        `json:"zone"`
	Days []ScheduleDay `json:"days"`
}

// ScheduleDay is one window: Day is 0 (Sunday) to 6, From and To are "HH:MM"
// with From before To.
type ScheduleDay struct {
	Day  int    `json:"day"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Validate reports why the schedule is unusable.
func (s Schedule) Validate() error {
	if len(s.Days) == 0 {
		return nil
	}
	if len(s.Days) > 28 {
		return errors.New("at most 28 windows")
	}
	if s.Zone == "" {
		return errors.New("a time zone is required")
	}
	if _, err := time.LoadLocation(s.Zone); err != nil {
		return fmt.Errorf("unknown time zone %q", s.Zone)
	}
	for _, d := range s.Days {
		if d.Day < 0 || d.Day > 6 {
			return errors.New("day must be 0 (Sunday) to 6")
		}
		from, ok1 := parseClock(d.From)
		to, ok2 := parseClock(d.To)
		if !ok1 || !ok2 || from >= to {
			return errors.New("each window needs from and to as HH:MM, from before to")
		}
	}
	return nil
}

// Open reports whether the number is open at now. An invalid schedule counts
// as open, so a bad setting never turns customers away.
func (s Schedule) Open(now time.Time) bool {
	if len(s.Days) == 0 || s.Validate() != nil {
		return true
	}
	loc, _ := time.LoadLocation(s.Zone)
	t := now.In(loc)
	m := t.Hour()*60 + t.Minute()
	for _, d := range s.Days {
		from, _ := parseClock(d.From)
		to, _ := parseClock(d.To)
		if d.Day == int(t.Weekday()) && m >= from && m < to {
			return true
		}
	}
	return false
}
