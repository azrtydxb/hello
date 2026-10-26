package timecond

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MaxImportEntries bounds one import and one calendar.
const MaxImportEntries = 1000

// ParseImport reads a list of dates into calendar entries. Two forms are
// accepted, told apart by the text itself:
//
//   - an iCalendar file (BEGIN:VCALENDAR): every VEVENT becomes an entry
//     from its DTSTART, DTEND (exclusive for whole days) and SUMMARY, yearly
//     when it has RRULE:FREQ=YEARLY;
//   - a plain list, one entry per line: a date "2026-12-25", a range
//     "2026-12-24..2026-12-26" or a yearly date "12-25" (no year), then
//     optional words in any order, then the label:
//     "13:00-17:00" limits it to part of each day, "closed" (the default),
//     "open=09:00-13:00,14:00-16:00" opens those hours instead, "yearly"
//     repeats a dated entry every year. Blank lines and lines starting with
//     # are skipped.
//
// Problems name the offending line as "lines[N]" (1-based).
func ParseImport(text string) ([]Entry, []Problem) {
	var entries []Entry
	var ps []Problem
	if strings.Contains(text, "BEGIN:VCALENDAR") {
		entries, ps = parseICS(text)
	} else {
		entries, ps = parseList(text)
	}
	if len(entries) > MaxImportEntries {
		ps = append(ps, Problem{"lines", fmt.Sprintf("at most %d entries per import", MaxImportEntries)})
	}
	return entries, ps
}

func parseList(text string) ([]Entry, []Problem) {
	var entries []Entry
	var ps []Problem
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		path := fmt.Sprintf("lines[%d]", i+1)
		e, err := parseListLine(line)
		if err != "" {
			ps = append(ps, Problem{path, err})
			continue
		}
		if np := e.Normalize(path); len(np) > 0 {
			ps = append(ps, np...)
			continue
		}
		entries = append(entries, e)
	}
	return entries, ps
}

var listDateRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}|\d{2}-\d{2})(\.\.(\d{4}-\d{2}-\d{2}|\d{2}-\d{2}))?`)

const listSep = " \t,;"

// parseListLine parses one line of the list form; err is empty on success.
func parseListLine(line string) (Entry, string) {
	m := listDateRe.FindStringSubmatch(line)
	if m == nil {
		return Entry{}, `must start with a date such as 2026-12-25, 2026-12-24..2026-12-26 or 12-25`
	}
	e := Entry{Mode: ModeClosed, AllDay: true, Start: m[1], End: m[1]}
	if m[3] != "" {
		e.End = m[3]
	}
	e.Yearly = len(e.Start) == 5
	rest := strings.TrimLeft(line[len(m[0]):], listSep)
	for rest != "" {
		word := rest
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			word = rest[:i]
		}
		kw := strings.TrimRight(word, ",;")
		switch {
		case strings.EqualFold(kw, "yearly"), strings.EqualFold(kw, "annual"):
			e.Yearly = true
		case strings.EqualFold(kw, "closed"):
			e.Mode = ModeClosed
		case strings.HasPrefix(strings.ToLower(kw), "open="):
			e.Mode = ModeOpen
			for _, r := range strings.Split(kw[5:], ",") {
				s, en, ok := strings.Cut(r, "-")
				if !ok {
					return Entry{}, `open hours must be like "open=09:00-13:00,14:00-16:00"`
				}
				e.Hours = append(e.Hours, Range{Start: s, End: en})
			}
		case isTimeRange(kw):
			s, en, _ := strings.Cut(kw, "-")
			e.AllDay, e.StartTime, e.EndTime = false, s, en
		default:
			e.Label = strings.TrimSpace(rest)
			rest = ""
			continue
		}
		rest = strings.TrimLeft(rest[len(word):], listSep)
	}
	if e.Yearly {
		e.Start, e.End = yearlyForm(e.Start), yearlyForm(e.End)
	}
	return e, ""
}

func isTimeRange(w string) bool {
	a, b, ok := strings.Cut(w, "-")
	if !ok {
		return false
	}
	_, ok1 := minutes(a, false)
	_, ok2 := minutes(b, true)
	return ok1 && ok2
}

// parseICS reads the VEVENTs of an iCalendar file.
func parseICS(text string) ([]Entry, []Problem) {
	// Unfold continuation lines (RFC 5545 3.1).
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\n ", ""), "\n\t", "")
	var entries []Entry
	var ps []Problem
	type ev struct {
		start, end, summary string
		yearly              bool
		line                int
	}
	var cur *ev
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "BEGIN:VEVENT":
			cur = &ev{line: i + 1}
		case line == "END:VEVENT" && cur != nil:
			path := fmt.Sprintf("lines[%d]", cur.line)
			e, err := icsEntry(cur.start, cur.end, cur.summary, cur.yearly)
			if err != "" {
				ps = append(ps, Problem{path, err})
			} else if np := e.Normalize(path); len(np) > 0 {
				ps = append(ps, np...)
			} else {
				entries = append(entries, e)
			}
			cur = nil
		case cur != nil:
			name, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			prop, _, _ := strings.Cut(name, ";")
			switch strings.ToUpper(prop) {
			case "DTSTART":
				cur.start = val
			case "DTEND":
				cur.end = val
			case "SUMMARY":
				cur.summary = strings.NewReplacer(`\,`, ",", `\;`, ";", `\n`, " ", `\\`, `\`).Replace(val)
			case "RRULE":
				cur.yearly = strings.Contains(strings.ToUpper(val), "FREQ=YEARLY")
			}
		}
	}
	return entries, ps
}

// icsEntry builds an entry from iCalendar date or date-time values.
func icsEntry(dtstart, dtend, summary string, yearly bool) (Entry, string) {
	sd, st, ok := icsDate(dtstart)
	if !ok {
		return Entry{}, "event has no usable DTSTART"
	}
	e := Entry{Label: strings.TrimSpace(summary), Start: sd.Format("2006-01-02"), Mode: ModeClosed, AllDay: true, Yearly: yearly}
	e.End = e.Start
	if dtend != "" {
		ed, et, ok := icsDate(dtend)
		if !ok {
			return Entry{}, "event has no usable DTEND"
		}
		switch {
		case st == "" && et == "":
			// Whole days: DTEND is the day after the last one.
			if last := ed.AddDate(0, 0, -1); !last.Before(sd) {
				e.End = last.Format("2006-01-02")
			}
		case st != "" && et != "" && ed.Format("20060102") == sd.Format("20060102"):
			e.AllDay, e.StartTime, e.EndTime = false, st, et
		case st != "" && et != "":
			e.End = ed.Format("2006-01-02")
		}
	}
	if len(e.Label) > 120 {
		e.Label = e.Label[:120]
	}
	return e, ""
}

// icsDate parses YYYYMMDD or YYYYMMDDTHHMMSS[Z]; tm is "HH:MM" for a
// date-time and empty for a date. A UTC time is taken as written: an import
// has no way to know the schedule's zone, and a holiday list is whole days.
func icsDate(v string) (d time.Time, tm string, ok bool) {
	v = strings.TrimSuffix(strings.TrimSpace(v), "Z")
	if len(v) == 8 {
		d, err := time.Parse("20060102", v)
		return d, "", err == nil
	}
	if len(v) == 15 && v[8] == 'T' {
		d, err := time.Parse("20060102T150405", v)
		return d, d.Format("15:04"), err == nil
	}
	return time.Time{}, "", false
}
