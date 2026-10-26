package timecond

import (
	"strings"
	"testing"
)

// TestParseImportList: dates, ranges, yearly dates, times, open hours and
// labels with spaces and commas. It fails if a line is misread or a label is
// cut.
func TestParseImportList(t *testing.T) {
	text := `# Belgium 2026
2026-12-25 Christmas Day
2026-12-24..2026-12-26 Christmas break
07-21 National day
2026-12-31,Year end, with a comma
2026-06-02 14:00-15:00 Staff meeting
2026-11-29 open=10:00-13:00,14:00-18:00 Shopping Sunday
2026-05-01 yearly Labour Day

`
	es, ps := ParseImport(text)
	if len(ps) != 0 {
		t.Fatalf("problems: %v", ps)
	}
	if len(es) != 7 {
		t.Fatalf("entries = %d: %+v", len(es), es)
	}
	check := func(i int, label, start, end string, yearly, allDay bool, mode string) {
		t.Helper()
		e := es[i]
		if e.Label != label || e.Start != start || e.End != end || e.Yearly != yearly || e.AllDay != allDay || e.Mode != mode {
			t.Errorf("entry %d = %+v", i, e)
		}
	}
	check(0, "Christmas Day", "2026-12-25", "2026-12-25", false, true, ModeClosed)
	check(1, "Christmas break", "2026-12-24", "2026-12-26", false, true, ModeClosed)
	check(2, "National day", "07-21", "07-21", true, true, ModeClosed)
	check(3, "Year end, with a comma", "2026-12-31", "2026-12-31", false, true, ModeClosed)
	check(4, "Staff meeting", "2026-06-02", "2026-06-02", false, false, ModeClosed)
	if es[4].StartTime != "14:00" || es[4].EndTime != "15:00" {
		t.Errorf("window = %+v", es[4])
	}
	check(5, "Shopping Sunday", "2026-11-29", "2026-11-29", false, true, ModeOpen)
	if len(es[5].Hours) != 2 || es[5].Hours[1] != (Range{"14:00", "18:00"}) {
		t.Errorf("hours = %+v", es[5].Hours)
	}
	check(6, "Labour Day", "05-01", "05-01", true, true, ModeClosed)
}

// TestParseImportListProblems: bad lines are reported with their line
// number and the good lines are not imported silently around them.
func TestParseImportListProblems(t *testing.T) {
	_, ps := ParseImport("2026-12-25 fine\nChristmas\n2026-13-01 bad month\n2026-12-25 open=9-10 x\n12-25..12-24 yearly wraps fine\n")
	got := map[string]bool{}
	for _, p := range ps {
		got[p.Path[:strings.Index(p.Path, "]")+1]] = true
	}
	for _, want := range []string{"lines[2]", "lines[3]", "lines[4]"} {
		if !got[want] {
			t.Errorf("no problem for %s: %v", want, ps)
		}
	}
	if got["lines[1]"] || got["lines[5]"] {
		t.Errorf("good lines flagged: %v", ps)
	}
	if _, ps := ParseImport(strings.Repeat("2026-12-25 x\n", MaxImportEntries+1)); len(ps) == 0 {
		t.Error("too many entries accepted")
	}
}

// TestParseImportICS: a public-holiday style calendar: whole days with an
// exclusive end, a yearly rule, a multi-day event and a timed event.
func TestParseImportICS(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20261225\r\nDTEND;VALUE=DATE:20261226\r\nSUMMARY:Christmas\\, Day\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20260721\r\nSUMMARY:National day\r\nRRULE:FREQ=YEARLY\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20261224\r\nDTEND;VALUE=DATE:20261227\r\nSUMMARY:Break\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART:20260602T140000Z\r\nDTEND:20260602T150000Z\r\nSUMMARY:Meet\r\n ing\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	es, ps := ParseImport(ics)
	if len(ps) != 0 || len(es) != 4 {
		t.Fatalf("entries %d, problems %v", len(es), ps)
	}
	if e := es[0]; e.Label != "Christmas, Day" || e.Start != "2026-12-25" || e.End != "2026-12-25" || e.Yearly {
		t.Errorf("christmas = %+v", e)
	}
	if e := es[1]; !e.Yearly || e.Start != "07-21" || e.End != "07-21" {
		t.Errorf("yearly = %+v", e)
	}
	if e := es[2]; e.Start != "2026-12-24" || e.End != "2026-12-26" {
		t.Errorf("break = %+v", e)
	}
	if e := es[3]; e.Label != "Meeting" || e.AllDay || e.StartTime != "14:00" || e.EndTime != "15:00" {
		t.Errorf("timed = %+v", e)
	}
}
