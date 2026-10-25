package prov

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // the zone rules ship in the binary, not in the container image
)

// TZInfo is one IANA time zone as each vendor's phone wants it. Phones do
// not accept IANA names: Yealink takes a numeric offset plus daylight-saving
// rules, Grandstream a POSIX TZ string, Poly an offset in seconds plus rules,
// Fanvil an offset in quarter hours plus rules, Snom a country code with an
// offset. Everything is derived from the IANA rules for a reference year
// (see NewTimeInfo); a field is empty when the zone has no value for that
// vendor. Known is false when the IANA name is unknown, and then every
// field is empty and the templates leave the zone alone.
type TZInfo struct {
	Known bool
	// POSIX is the TZ string with the daylight-saving rule, for Grandstream
	// P64 and P246: "CET-1CEST,M3.5.0,M10.5.0/3".
	POSIX string
	// OffsetSeconds is the standard offset east of UTC (Poly gmtOffset).
	OffsetSeconds int
	// DST is true when the zone has a daylight-saving rule.
	DST bool
	// SaveMinutes is the daylight-saving shift in minutes.
	SaveMinutes int

	// Yealink: time_zone "+1" or "+5:30", the optional time_zone_name label,
	// and start_time / end_time as "month/week/weekday/hour" (week 5 is the
	// last, weekday 7 is Sunday).
	YealinkOffset string
	YealinkName   string
	YealinkStart  string
	YealinkEnd    string

	// Fanvil: Time Zone is in quarter hours, Time Zone Name "UTC+1". The
	// rule fields are month, week (5 = last), weekday (0 = Sunday) and hour.
	FanvilOffset int
	FanvilName   string
	FanvilStart  Rule
	FanvilEnd    Rule

	// Poly: month, date, hour and dayOfWeek (1 = Sunday) of tcpIpApp.sntp
	// daylightSavings.{start,stop}; Date is the first day of the week the
	// rule falls in, LastInMonth is 1 for the last such weekday of the month.
	PolyStart PolyRule
	PolyEnd   PolyRule

	// Snom: the timezone setting, "BEL+1".
	SnomCode string
}

// Rule is a "weekday of week of month at hour" daylight-saving switch.
type Rule struct {
	Month   int
	Week    int // 1 to 4, 5 is the last
	Weekday int // 0 is Sunday
	Hour    int
	Minute  int
}

// PolyRule is a Rule in Poly's terms.
type PolyRule struct {
	Month       int
	Date        int
	Hour        int
	DayOfWeek   int // 1 is Sunday
	LastInMonth int
}

// yealinkNames are the Yealink time zone labels for zones where the label
// is known; the rules are sent too, so a zone without a label still
// switches correctly.
var yealinkNames = map[string]string{
	"Europe/Brussels":  "Belgium(Brussels)",
	"Europe/London":    "United Kingdom(London)",
	"Europe/Berlin":    "Germany(Berlin)",
	"Europe/Paris":     "France(Paris)",
	"Europe/Madrid":    "Spain(Madrid)",
	"America/New_York": "United States-Eastern Time",
}

// snomCodes are Snom's country-code zones: the country sets the daylight
// saving rules, the number the standard offset. Zones outside this table
// get no Snom timezone, the phone keeps its own.
var snomCodes = map[string]string{
	"Europe/Brussels": "BEL+1", "Europe/London": "GBR-0", "Europe/Paris": "FRA+1",
	"Europe/Berlin": "DEU+1", "Europe/Amsterdam": "NLD+1", "Europe/Madrid": "ESP+1",
	"Europe/Rome": "ITA+1", "Europe/Zurich": "CHE+1", "Europe/Vienna": "AUT+1",
	"Europe/Lisbon": "PRT-0", "America/New_York": "USA-5", "America/Chicago": "USA-6",
	"America/Denver": "USA-7", "America/Los_Angeles": "USA-8", "UTC": "GBR-0",
}

// NewTimeInfo is the clock setup for an IANA zone and NTP server. The
// daylight-saving rules are the ones in force in the year of now.
func NewTimeInfo(zone, ntp string, now time.Time) TimeInfo {
	return TimeInfo{Zone: zone, NTP: ntp, TZ: ZoneInfo(zone, now.Year())}
}

// ZoneInfo derives a zone's vendor settings from the IANA rules for year.
// A zone whose offset changes other than twice a year is sent as its
// standard offset without daylight saving.
func ZoneInfo(name string, year int) TZInfo {
	if name == "" || name == "Local" {
		return TZInfo{}
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return TZInfo{}
	}
	std, dst, start, end, ok := zoneRules(loc, year)
	z := TZInfo{Known: true, OffsetSeconds: std.offset}
	z.YealinkOffset = hoursMinutes(std.offset)
	z.FanvilOffset = std.offset / 900
	z.FanvilName = "UTC"
	if std.offset != 0 {
		z.FanvilName += hoursMinutes(std.offset)
	}
	z.YealinkName = yealinkNames[name]
	z.SnomCode = snomCodes[name]
	z.POSIX = posixName(std) + posixOffset(std.offset)
	if ok {
		z.DST, z.SaveMinutes = true, (dst.offset-std.offset)/60
		z.POSIX += posixName(dst)
		if z.SaveMinutes != 60 {
			z.POSIX += posixOffset(dst.offset)
		}
		z.POSIX += "," + posixRule(start) + "," + posixRule(end)
		z.YealinkStart, z.YealinkEnd = yealinkRule(start), yealinkRule(end)
		z.FanvilStart, z.FanvilEnd = start, end
		z.PolyStart, z.PolyEnd = polyRule(start), polyRule(end)
	}
	return z
}

// zoneAbbr is one side of a daylight-saving pair.
type zoneAbbr struct {
	name   string
	offset int // seconds east of UTC
}

// zoneSwitch is one change of offset.
type zoneSwitch struct {
	before, after zoneAbbr
	beforeDST     bool
	instant       time.Time // the first second with the new offset
}

// zoneRules finds the standard and daylight offsets of loc in year and the
// two wall-clock switches. ok is false unless there are exactly two.
func zoneRules(loc *time.Location, year int) (std, dst zoneAbbr, start, end Rule, ok bool) {
	at := func(t time.Time) (zoneAbbr, bool) {
		l := t.In(loc)
		n, off := l.Zone()
		return zoneAbbr{n, off}, l.IsDST()
	}
	std, _ = at(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC))
	var switches []zoneSwitch
	for day := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); day.Year() == year; day = day.AddDate(0, 0, 1) {
		next := day.AddDate(0, 0, 1)
		a, aDST := at(day)
		b, _ := at(next)
		if a.offset == b.offset {
			continue
		}
		lo, hi := day, next
		for hi.Sub(lo) > time.Second {
			mid := lo.Add(hi.Sub(lo) / 2)
			if m, _ := at(mid); m.offset == a.offset {
				lo = mid
			} else {
				hi = mid
			}
		}
		after, _ := at(hi)
		switches = append(switches, zoneSwitch{a, after, aDST, hi})
	}
	if len(switches) != 2 || switches[0].beforeDST == switches[1].beforeDST {
		return std, zoneAbbr{}, Rule{}, Rule{}, false
	}
	toDST, toStd := switches[0], switches[1]
	if switches[0].beforeDST {
		toDST, toStd = switches[1], switches[0]
	}
	std, dst = toDST.before, toDST.after
	// A rule's wall time is read on the clock in force just before it.
	return std, dst, wallRule(toDST.instant, std.offset), wallRule(toStd.instant, dst.offset), true
}

// wallRule is the rule form of an instant seen on a clock at offset.
func wallRule(instant time.Time, offset int) Rule {
	w := instant.UTC().Add(time.Duration(offset) * time.Second)
	days := time.Date(w.Year(), w.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	week := (w.Day()-1)/7 + 1
	if w.Day()+7 > days {
		week = 5
	}
	return Rule{Month: int(w.Month()), Week: week, Weekday: int(w.Weekday()), Hour: w.Hour(), Minute: w.Minute()}
}

// hoursMinutes formats an offset as "+1", "-5", "+5:30", or "0".
func hoursMinutes(offset int) string {
	if offset == 0 {
		return "0"
	}
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	h, m := offset/3600, offset%3600/60
	if m == 0 {
		return fmt.Sprintf("%s%d", sign, h)
	}
	return fmt.Sprintf("%s%d:%02d", sign, h, m)
}

// posixName is the abbreviation of a POSIX TZ string: letters only, or the
// quoted numeric form for zones tzdata gives no letter abbreviation.
func posixName(z zoneAbbr) string {
	letters := len(z.name) >= 3 && strings.IndexFunc(z.name, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < 'a' || r > 'z')
	}) < 0
	if letters {
		return z.name
	}
	return "<" + z.name + ">"
}

// posixOffset is the offset of a POSIX TZ string: hours west of UTC, so
// UTC+1 is "-1" and UTC-5 is "5".
func posixOffset(offset int) string {
	w := -offset
	sign := ""
	if w < 0 {
		sign, w = "-", -w
	}
	if w%3600 == 0 {
		return fmt.Sprintf("%s%d", sign, w/3600)
	}
	return fmt.Sprintf("%s%d:%02d", sign, w/3600, w%3600/60)
}

// posixRule is "Mm.w.d" with "/h[:mm]" unless the switch is at 02:00.
func posixRule(r Rule) string {
	s := fmt.Sprintf("M%d.%d.%d", r.Month, r.Week, r.Weekday)
	switch {
	case r.Hour == 2 && r.Minute == 0:
	case r.Minute == 0:
		s += fmt.Sprintf("/%d", r.Hour)
	default:
		s += fmt.Sprintf("/%d:%02d", r.Hour, r.Minute)
	}
	return s
}

// yealinkRule is "month/week/weekday/hour", weekday 1 Monday to 7 Sunday.
func yealinkRule(r Rule) string {
	wd := r.Weekday
	if wd == 0 {
		wd = 7
	}
	return fmt.Sprintf("%d/%d/%d/%d", r.Month, r.Week, wd, r.Hour)
}

// polyRule: the date is the first day of the week the rule falls in; the
// last weekday of the month is flagged instead.
func polyRule(r Rule) PolyRule {
	p := PolyRule{Month: r.Month, Hour: r.Hour, DayOfWeek: r.Weekday + 1, Date: 1 + 7*(r.Week-1)}
	if r.Week == 5 {
		p.Date, p.LastInMonth = 1, 1
	}
	return p
}
