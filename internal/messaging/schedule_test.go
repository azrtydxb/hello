package messaging

import (
	"testing"
	"time"
)

func TestScheduleOpen(t *testing.T) {
	s := Schedule{Zone: "Europe/Brussels", Days: []ScheduleDay{{Day: 1, From: "09:00", To: "17:00"}, {Day: 3, From: "09:00", To: "12:00"}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	at := func(v string) time.Time {
		tm, err := time.Parse(time.RFC3339, v)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	// 2026-11-09 is a Monday; Brussels is UTC+1 in November.
	for v, want := range map[string]bool{
		"2026-11-09T08:30:00Z": true,  // 09:30 local
		"2026-11-09T07:59:00Z": false, // 08:59 local
		"2026-11-09T16:00:00Z": false, // 17:00 local: closed at the end
		"2026-11-10T10:00:00Z": false, // Tuesday
		"2026-11-11T10:00:00Z": true,  // Wednesday 11:00 local
		"2026-11-11T11:00:00Z": false, // Wednesday 12:00 local
	} {
		if got := s.Open(at(v)); got != want {
			t.Errorf("Open(%s) = %v, want %v", v, got, want)
		}
	}
	if !(Schedule{}).Open(at("2026-11-10T03:00:00Z")) {
		t.Error("an empty schedule is always open")
	}
}

func TestScheduleValidate(t *testing.T) {
	good := ScheduleDay{Day: 1, From: "09:00", To: "17:00"}
	for name, s := range map[string]Schedule{
		"no zone":      {Days: []ScheduleDay{good}},
		"unknown zone": {Zone: "Nowhere/Land", Days: []ScheduleDay{good}},
		"bad day":      {Zone: "UTC", Days: []ScheduleDay{{Day: 7, From: "09:00", To: "17:00"}}},
		"bad clock":    {Zone: "UTC", Days: []ScheduleDay{{Day: 1, From: "9am", To: "17:00"}}},
		"backwards":    {Zone: "UTC", Days: []ScheduleDay{{Day: 1, From: "17:00", To: "09:00"}}},
		"too many":     {Zone: "UTC", Days: make([]ScheduleDay, 29)},
	} {
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// An invalid stored schedule never closes the number.
	if !(Schedule{Zone: "Nowhere/Land", Days: []ScheduleDay{good}}).Open(time.Now()) {
		t.Error("an invalid schedule must read as open")
	}
}

func TestValidAfterHours(t *testing.T) {
	for _, a := range []string{AfterNone, AfterReply, AfterRoute} {
		if !ValidAfterHours(a) {
			t.Errorf("%q is an action", a)
		}
	}
	if ValidAfterHours("ignore") || ValidAfterHours("") {
		t.Error("accepted an unknown action")
	}
	for _, k := range []string{RouteInbox, RouteQueue, RouteUser, RouteExtension, RouteRingGroup} {
		if !ValidRouteKind(k) {
			t.Errorf("%q is a route kind", k)
		}
	}
	if ValidRouteKind("ai_triage") || ValidRouteKind("") {
		t.Error("accepted an unknown route kind")
	}
}
