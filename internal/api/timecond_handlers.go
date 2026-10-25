package api

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/timecond"
)

// Time conditions (migration 00020): schedules, calendars and the time
// conditions that route by them. Reads are open to a viewer; changes need
// the write scope, like ring groups and IVR menus.

var toggleNumberRe = regexp.MustCompile(`^([0-9]{1,4})?$`)

// timeSettings is what GET /api/v1/time-settings returns: the zone a new
// schedule gets when the request names none.
type timeSettings struct {
	TimeZone string `json:"timeZone"`
}

func (s *server) getTimeSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, timeSettings{TimeZone: s.defaultZone()})
}

// defaultZone is the phone time zone setting (HELLO_PROV_TIMEZONE), which
// the schedules default to; UTC when it is unusable.
func (s *server) defaultZone() string {
	if z := s.DefaultTimeZone; timecond.ValidZone(z) {
		return z
	}
	return "UTC"
}

func addProblems(f *fieldErrs, ps []timecond.Problem) {
	for _, p := range ps {
		f.add(p.Path, "%s", p.Msg)
	}
}

// --- schedules ---------------------------------------------------------------

type scheduleBody struct {
	Name        *string              `json:"name"`
	TimeZone    *string              `json:"timeZone"`
	Ranges      *[]timecond.DayRange `json:"ranges"`
	CalendarIDs *[]int64             `json:"calendarIds"`
}

func (s *server) scheduleInput(b scheduleBody) (store.ScheduleInput, fieldErrs) {
	in := store.ScheduleInput{TimeZone: s.defaultZone(), Ranges: []timecond.DayRange{}, CalendarIDs: []int64{}}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.TimeZone != nil && *b.TimeZone != "" {
		in.TimeZone = *b.TimeZone
	}
	if b.Ranges != nil {
		in.Ranges = *b.Ranges
	}
	if b.CalendarIDs != nil {
		in.CalendarIDs = *b.CalendarIDs
	}
	var f fieldErrs
	if !usernameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	if !timeZoneRe.MatchString(in.TimeZone) || !timecond.ValidZone(in.TimeZone) {
		f.add("timeZone", "must be an IANA time zone name such as Asia/Dubai")
	}
	if len(in.Ranges) > store.MaxScheduleRanges {
		f.add("ranges", "must have at most %d ranges", store.MaxScheduleRanges)
	}
	addProblems(&f, timecond.ValidateRanges("ranges", in.Ranges))
	seen := map[int64]bool{}
	for i, id := range in.CalendarIDs {
		if id <= 0 || seen[id] {
			f.add("calendarIds", "item %d must be a distinct calendar id", i)
		}
		seen[id] = true
	}
	return in, f
}

func (s *server) listSchedules(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ListSchedules(r.Context())
	if err != nil {
		s.internal(w, "list schedules", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

func (s *server) getSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.Store.GetSchedule(r.Context(), id)
	if err != nil {
		s.configError(w, "schedule", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) createSchedule(w http.ResponseWriter, r *http.Request) {
	var b scheduleBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil {
		badRequest(w, "name is required")
		return
	}
	in, f := s.scheduleInput(b)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.CreateSchedule(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "schedule", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) putSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b scheduleBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil {
		badRequest(w, "name is required")
		return
	}
	in, f := s.scheduleInput(b)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.UpdateSchedule(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "schedule", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteSchedule(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "schedule", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- calendars ---------------------------------------------------------------

type calendarBody struct {
	Name        *string           `json:"name"`
	Description *string           `json:"description"`
	Entries     *[]timecond.Entry `json:"entries"`
}

func calendarInput(b calendarBody) (store.CalendarInput, fieldErrs) {
	in := store.CalendarInput{Entries: []timecond.Entry{}}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.Description != nil {
		in.Description = *b.Description
	}
	if b.Entries != nil {
		in.Entries = *b.Entries
	}
	var f fieldErrs
	if !usernameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	if len(in.Description) > 500 {
		f.add("description", "must be at most 500 characters")
	}
	if len(in.Entries) > store.MaxCalendarEntries {
		f.add("entries", "must have at most %d entries", store.MaxCalendarEntries)
		return in, f
	}
	for i := range in.Entries {
		addProblems(&f, in.Entries[i].Normalize("entries["+strconv.Itoa(i)+"]"))
	}
	return in, f
}

func (s *server) listCalendars(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ListCalendars(r.Context())
	if err != nil {
		s.internal(w, "list calendars", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

func (s *server) getCalendar(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.Store.GetCalendar(r.Context(), id)
	if err != nil {
		s.configError(w, "calendar", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) createCalendar(w http.ResponseWriter, r *http.Request) {
	var b calendarBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil {
		badRequest(w, "name is required")
		return
	}
	in, f := calendarInput(b)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.CreateCalendar(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "calendar", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) putCalendar(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b calendarBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil {
		badRequest(w, "name is required")
		return
	}
	in, f := calendarInput(b)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.UpdateCalendar(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "calendar", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteCalendar(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteCalendar(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "calendar", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type importBody struct {
	Text    *string `json:"text"`
	Replace bool    `json:"replace"`
}

// calendarImportResult is the import's answer: how many entries it read and
// the calendar as it is now.
type calendarImportResult struct {
	Imported int            `json:"imported"`
	Calendar store.Calendar `json:"calendar"`
}

// importCalendar reads a list of dates (one per line, or an iCalendar file)
// into a calendar. Nothing is stored when any line is bad.
func (s *server) importCalendar(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b importBody
	if !decode(w, r, &b) {
		return
	}
	if b.Text == nil || strings.TrimSpace(*b.Text) == "" {
		badRequest(w, "text is required")
		return
	}
	if len(*b.Text) > 1<<20 {
		badRequest(w, "text must be at most 1 MiB")
		return
	}
	entries, ps := timecond.ParseImport(*b.Text)
	var f fieldErrs
	addProblems(&f, ps)
	if len(f) == 0 && len(entries) == 0 {
		f.add("text", "holds no dates")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.ImportCalendarEntries(r.Context(), actor(r).String(), id, entries, b.Replace, s.check())
	if err != nil {
		s.configError(w, "calendar", err)
		return
	}
	writeJSON(w, http.StatusOK, calendarImportResult{Imported: len(entries), Calendar: v})
}

// --- time conditions -----------------------------------------------------------

type tcDestBody struct {
	Kind          string `json:"kind"`
	Target        string `json:"target"`
	ThenVoicemail string `json:"thenVoicemail"`
}

type timeConditionBody struct {
	Name         *string     `json:"name"`
	ScheduleID   *int64      `json:"scheduleId"`
	Open         *tcDestBody `json:"open"`
	Closed       *tcDestBody `json:"closed"`
	Holiday      *tcDestBody `json:"holiday"`
	ToggleNumber *string     `json:"toggleNumber"`
}

func (d tcDestBody) dest() timecond.Dest {
	return timecond.Dest{Kind: d.Kind, Target: d.Target, ThenVoicemail: d.ThenVoicemail}
}

func validateTCDest(f *fieldErrs, path string, d timecond.Dest) {
	validateIVRDestination(f, path, d.Kind, d.Target)
	if d.ThenVoicemail != "" {
		if d.Kind != store.IVRAnnouncement {
			f.add(path+".thenVoicemail", `is only for an "announcement" destination`)
		} else if !numberRe.MatchString(d.ThenVoicemail) {
			f.add(path+".thenVoicemail", "must be an extension number (2-10 digits)")
		}
	}
}

func timeConditionInput(b timeConditionBody) (store.TimeConditionInput, fieldErrs) {
	in := store.TimeConditionInput{Open: timecond.Dest{Kind: store.IVRHangup}, Closed: timecond.Dest{Kind: store.IVRHangup}}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.ScheduleID != nil {
		in.ScheduleID = *b.ScheduleID
	}
	if b.Open != nil {
		in.Open = b.Open.dest()
	}
	if b.Closed != nil {
		in.Closed = b.Closed.dest()
	}
	if b.Holiday != nil {
		h := b.Holiday.dest()
		in.Holiday = &h
	}
	if b.ToggleNumber != nil {
		in.ToggleNumber = *b.ToggleNumber
	}
	var f fieldErrs
	if !usernameRe.MatchString(in.Name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	if in.ScheduleID <= 0 {
		f.add("scheduleId", "must name the schedule")
	}
	if !toggleNumberRe.MatchString(in.ToggleNumber) {
		f.add("toggleNumber", "must be empty or 1-4 digits")
	}
	validateTCDest(&f, "open", in.Open)
	validateTCDest(&f, "closed", in.Closed)
	if in.Holiday != nil {
		validateTCDest(&f, "holiday", *in.Holiday)
	}
	return in, f
}

// requireTimeCondition adds a field error when no time condition has that name.
func (s *server) requireTimeCondition(ctx context.Context, f *fieldErrs, path, name string) {
	ok, err := s.Store.TimeConditionNameExists(ctx, name)
	switch {
	case err != nil:
		s.Log.Warn("time condition lookup", "error", err)
		f.add(path, "could not check the time condition")
	case !ok:
		f.add(path, "time condition %q does not exist", name)
	}
}

func (s *server) listTimeConditions(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.ListTimeConditions(r.Context())
	if err != nil {
		s.internal(w, "list time conditions", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

func (s *server) getTimeCondition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.Store.GetTimeCondition(r.Context(), id)
	if err != nil {
		s.configError(w, "time condition", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) createTimeCondition(w http.ResponseWriter, r *http.Request) {
	var b timeConditionBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.ScheduleID == nil {
		badRequest(w, "name and scheduleId are required")
		return
	}
	in, f := timeConditionInput(b)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.CreateTimeCondition(r.Context(), actor(r).String(), in, s.check())
	if err != nil {
		s.configError(w, "time condition", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) putTimeCondition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b timeConditionBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.ScheduleID == nil {
		badRequest(w, "name and scheduleId are required")
		return
	}
	in, f := timeConditionInput(b)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Store.UpdateTimeCondition(r.Context(), actor(r).String(), id, in, s.check())
	if err != nil {
		s.configError(w, "time condition", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteTimeCondition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteTimeCondition(r.Context(), actor(r).String(), id, s.check()); err != nil {
		s.configError(w, "time condition", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type overrideBody struct {
	Override *string `json:"override"`
}

// putTimeConditionOverride is the day/night toggle: force open, force
// closed, or back to the schedule (auto).
func (s *server) putTimeConditionOverride(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b overrideBody
	if !decode(w, r, &b) {
		return
	}
	if b.Override == nil || !store.ValidOverride(*b.Override) {
		var f fieldErrs
		f.add("override", `must be "auto", "open" or "closed"`)
		writeFields(w, f)
		return
	}
	v, err := s.Store.SetTimeConditionOverride(r.Context(), actor(r).String(), id, *b.Override)
	if err != nil {
		s.configError(w, "time condition", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// evaluateTimeCondition answers "what would happen now / at time X". at is an
// RFC 3339 instant, or a local "YYYY-MM-DDTHH:MM" read in the schedule's
// zone; absent means now. ignoreOverride=true evaluates the schedule alone.
func (s *server) evaluateTimeCondition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tc, err := s.Store.GetTimeCondition(r.Context(), id)
	if err != nil {
		s.configError(w, "time condition", err)
		return
	}
	at := time.Now()
	if q := r.URL.Query().Get("at"); q != "" {
		t, err := time.Parse(time.RFC3339, q)
		if err != nil {
			sc, serr := s.Store.GetSchedule(r.Context(), tc.ScheduleID)
			if serr != nil {
				s.configError(w, "schedule", serr)
				return
			}
			loc, lerr := time.LoadLocation(sc.TimeZone)
			if lerr == nil {
				t, err = time.ParseInLocation("2006-01-02T15:04", q, loc)
			}
		}
		if err != nil {
			var f fieldErrs
			f.add("at", "must be an RFC 3339 time, or YYYY-MM-DDTHH:MM in the schedule's time zone")
			writeFields(w, f)
			return
		}
		at = t
	}
	ignore := r.URL.Query().Get("ignoreOverride") == "true"
	ev, err := s.Store.EvaluateTimeCondition(r.Context(), id, at, ignore)
	if err != nil {
		s.configError(w, "time condition", err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}
