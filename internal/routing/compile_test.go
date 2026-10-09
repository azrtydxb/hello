package routing

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

// Indices of the item each validation case appends to the fixture.
const (
	spareT = 4  // len(fixture Trunks)
	spareO = 12 // len(fixture Outbound)
	spareI = 10 // len(fixture Inbound)
)

func spareTrunk() Trunk {
	return Trunk{ID: 50, Name: "spare", Mode: "ip", Enabled: true, Destinations: []Destination{{Host: "192.0.2.50"}}}
}

func spareOut() OutboundRoute {
	return OutboundRoute{ID: 99, Position: 999, Name: "spare", MatchKind: "prefix", Match: "1", Trunks: []int64{tPrimary}, Enabled: true}
}

func spareIn() InboundRoute {
	return InboundRoute{ID: 99, Position: 999, Name: "spare", DIDKind: "exact", DID: "1", DestinationKind: "extension", Destination: "101", Enabled: true}
}

func goodSchedule() *Schedule {
	return &Schedule{TimeZone: "Europe/Berlin", Windows: []Window{{Days: []time.Weekday{1}, Start: "09:00", End: "17:00"}}}
}

func TestCompileValidation(t *testing.T) {
	base := fixtureConfig()
	if len(base.Trunks) != spareT || len(base.Outbound) != spareO || len(base.Inbound) != spareI {
		t.Fatal("fixture size changed; update the spare indices")
	}
	T := func(f string) string { return fmt.Sprintf("trunks[%d].%s", spareT, f) }
	O := func(f string) string { return fmt.Sprintf("outbound[%d].%s", spareO, f) }
	I := func(f string) string { return fmt.Sprintf("inbound[%d].%s", spareI, f) }
	trunk := func(mut func(*Trunk)) func(*Config) {
		return func(c *Config) { tr := spareTrunk(); mut(&tr); c.Trunks = append(c.Trunks, tr) }
	}
	out := func(mut func(*OutboundRoute)) func(*Config) {
		return func(c *Config) { r := spareOut(); mut(&r); c.Outbound = append(c.Outbound, r) }
	}
	in := func(mut func(*InboundRoute)) func(*Config) {
		return func(c *Config) { r := spareIn(); mut(&r); c.Inbound = append(c.Inbound, r) }
	}
	sched := func(mut func(*Schedule)) func(*OutboundRoute) {
		return func(r *OutboundRoute) { s := goodSchedule(); mut(s); r.Schedule = s }
	}
	fe := func(path, msg string) FieldError { return FieldError{Path: path, Message: msg} }

	cases := []struct {
		name string
		mut  func(*Config)
		want []FieldError
	}{
		// Trunks.
		{"trunk id zero", trunk(func(t *Trunk) { t.ID = 0 }), []FieldError{fe(T("id"), "must be positive")}},
		{"trunk id duplicate", trunk(func(t *Trunk) { t.ID = tPrimary }), []FieldError{fe(T("id"), "duplicate trunk id 1")}},
		{"trunk name empty", trunk(func(t *Trunk) { t.Name = " " }), []FieldError{fe(T("name"), "is required")}},
		{"trunk name duplicate", trunk(func(t *Trunk) { t.Name = "carrier-primary" }), []FieldError{fe(T("name"), `duplicate trunk name "carrier-primary"`)}},
		{"trunk mode", trunk(func(t *Trunk) { t.Mode = "peer" }), []FieldError{fe(T("mode"), `must be "registration" or "ip"`)}},
		{"registration without username", trunk(func(t *Trunk) { t.Mode = "registration" }), []FieldError{fe(T("username"), "is required for a registration trunk")}},
		{"negative durations and limit", trunk(func(t *Trunk) { t.RegisterExpires, t.OptionsInterval, t.MaxCalls = -1, -1, -1 }), []FieldError{
			fe(T("registerExpires"), "must not be negative"), fe(T("optionsInterval"), "must not be negative"), fe(T("maxCalls"), "must not be negative")}},
		{"default caller ID", trunk(func(t *Trunk) { t.DefaultCallerID = "office" }), []FieldError{fe(T("defaultCallerId"), "must be a number (+, digits, * and #)")}},
		{"source CIDR", trunk(func(t *Trunk) { t.SourceCIDRs = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), {}} }), []FieldError{fe(T("sourceCidrs[1]"), "is not a valid CIDR")}},
		{"no destinations", trunk(func(t *Trunk) { t.Destinations = nil }), []FieldError{fe(T("destinations"), "at least one destination is required")}},
		{"destination fields", trunk(func(t *Trunk) {
			t.Destinations = []Destination{{Host: "ok"}, {Host: "bad host", Port: 70000, Priority: -1, Weight: 70000}, {Host: ""}}
		}), []FieldError{
			fe(T("destinations[1].host"), "is required and must not contain whitespace"),
			fe(T("destinations[1].port"), "must be 0 (SRV, else 5060) to 65535"),
			fe(T("destinations[1].priority"), "must be 0 to 65535"),
			fe(T("destinations[1].weight"), "must be 0 to 65535"),
			fe(T("destinations[2].host"), "is required and must not contain whitespace"),
		}},
		// Outbound routes.
		{"outbound duplicate position", out(func(r *OutboundRoute) { r.Position = 40 }), []FieldError{fe(O("position"), "duplicate position 40 (also outbound[0])")}},
		{"outbound duplicate id", out(func(r *OutboundRoute) { r.ID = 4 }), []FieldError{fe(O("id"), "duplicate id 4 (also outbound[0])")}},
		{"outbound negative id", out(func(r *OutboundRoute) { r.ID = -1 }), []FieldError{fe(O("id"), "must not be negative")}},
		{"outbound name", out(func(r *OutboundRoute) { r.Name = "" }), []FieldError{fe(O("name"), "is required")}},
		{"match kind", out(func(r *OutboundRoute) { r.MatchKind = "glob" }), []FieldError{fe(O("matchKind"), `must be "prefix" or "regex"`)}},
		{"empty prefix", out(func(r *OutboundRoute) { r.Match = "" }), []FieldError{fe(O("match"), "a prefix must be a non-empty number (+, digits, * and #)")}},
		{"bare + prefix", out(func(r *OutboundRoute) { r.Match = "+" }), []FieldError{fe(O("match"), "a prefix must be a non-empty number (+, digits, * and #)")}},
		{"letters in prefix", out(func(r *OutboundRoute) { r.Match = "0x" }), []FieldError{fe(O("match"), "a prefix must be a non-empty number (+, digits, * and #)")}},
		{"regex does not compile", out(func(r *OutboundRoute) { r.MatchKind, r.Match = "regex", "^(05" }), []FieldError{
			fe(O("match"), "does not compile: error parsing regexp: missing closing ): `^(05`")}},
		{"regex not RE2 (backreference)", out(func(r *OutboundRoute) { r.MatchKind, r.Match = "regex", `^(0)\1$` }), []FieldError{
			fe(O("match"), "does not compile: error parsing regexp: invalid escape sequence: `\\1`")}},
		{"regex at 500 characters is accepted", out(func(r *OutboundRoute) { r.MatchKind, r.Match = "regex", strings.Repeat("0", 500) }), nil},
		{"regex over 500 characters", out(func(r *OutboundRoute) { r.MatchKind, r.Match = "regex", strings.Repeat("0", 501) }), []FieldError{
			fe(O("match"), "is longer than 500 characters")}},
		{"source extension empty", out(func(r *OutboundRoute) { r.SourceExtensions = []string{"101", ""} }), []FieldError{fe(O("sourceExtensions[1]"), "must not be empty")}},
		{"schedule time zone missing", out(sched(func(s *Schedule) { s.TimeZone = "" })), []FieldError{fe(O("schedule.timeZone"), "is required (an IANA name such as Asia/Dubai)")}},
		{"schedule time zone Local", out(sched(func(s *Schedule) { s.TimeZone = "Local" })), []FieldError{fe(O("schedule.timeZone"), `"Local" depends on the host; use an IANA name`)}},
		{"schedule time zone unknown", out(sched(func(s *Schedule) { s.TimeZone = "Mars/Olympus" })), []FieldError{fe(O("schedule.timeZone"), `unknown time zone "Mars/Olympus"`)}},
		{"schedule no windows", out(sched(func(s *Schedule) { s.Windows = nil })), []FieldError{fe(O("schedule.windows"), "at least one window is required (disable the route instead)")}},
		{"schedule window fields", out(sched(func(s *Schedule) {
			s.Windows = append(s.Windows, Window{Days: nil, Start: "8:00", End: "24:00"}, Window{Days: []time.Weekday{2, 7, -1}, Start: "09:60", End: "ab:cd"})
		})), []FieldError{
			fe(O("schedule.windows[1].days"), "at least one day is required"),
			fe(O("schedule.windows[1].start"), `must be "HH:MM" (00:00 to 23:59)`),
			fe(O("schedule.windows[1].end"), `must be "HH:MM" (00:00 to 23:59)`),
			fe(O("schedule.windows[2].days[1]"), "must be 0 (Sunday) to 6 (Saturday)"),
			fe(O("schedule.windows[2].days[2]"), "must be 0 (Sunday) to 6 (Saturday)"),
			fe(O("schedule.windows[2].start"), `must be "HH:MM" (00:00 to 23:59)`),
			fe(O("schedule.windows[2].end"), `must be "HH:MM" (00:00 to 23:59)`),
		}},
		{"number transform fields", out(func(r *OutboundRoute) { r.Number = Transform{Strip: -1, Prefix: "0+", Template: "1"} }), []FieldError{
			fe(O("numberTransform.strip"), "must not be negative"),
			fe(O("numberTransform.prefix"), "may contain only +, digits, * and #, with + only as the first character"),
			fe(O("numberTransform.template"), "a template requires a regex"),
		}},
		{"number transform regex without template", out(func(r *OutboundRoute) { r.Number = Transform{Regex: "^0"} }), []FieldError{
			fe(O("numberTransform.template"), "is required when a regex is set")}},
		{"number transform regex does not compile", out(func(r *OutboundRoute) { r.Number = Transform{Regex: "[", Template: "1"} }), []FieldError{
			fe(O("numberTransform.regex"), "does not compile: error parsing regexp: missing closing ]: `[`")}},
		{"number transform regex too long", out(func(r *OutboundRoute) { r.Number = Transform{Regex: strings.Repeat("a", 501), Template: "1"} }), []FieldError{
			fe(O("numberTransform.regex"), "is longer than 500 characters")}},
		{"number template references a missing group", out(func(r *OutboundRoute) { r.Number = Transform{Regex: "^0(5)$", Template: "+971${2}"} }), []FieldError{
			fe(O("numberTransform.template"), "references group ${2}, but the regex defines only 1")}},
		{"number template references a missing name", out(func(r *OutboundRoute) { r.Number = Transform{Regex: "^0(?P<n>5)$", Template: "${num}"} }), []FieldError{
			fe(O("numberTransform.template"), "references group ${num}, which the regex does not define")}},
		{"number template bare $1", out(func(r *OutboundRoute) { r.Number = Transform{Regex: "^0(5)$", Template: "+971$1"} }), []FieldError{
			fe(O("numberTransform.template"), "a bare $ is not allowed: write ${1} or ${name}")}},
		{"number template literal letters", out(func(r *OutboundRoute) { r.Number = Transform{Regex: "^0(5)$", Template: "tel${1}"} }), []FieldError{
			fe(O("numberTransform.template"), `literal "t" is not allowed: only +, digits, * and # and ${N} or ${name} references`)}},
		{"caller-ID template references a missing group", out(func(r *OutboundRoute) { r.CallerID = Transform{Regex: "^0", Template: "${1}"} }), []FieldError{
			fe(O("callerIdTransform.template"), "references group ${1}, but the regex defines only 0")}},
		{"outbound without trunks", out(func(r *OutboundRoute) { r.Trunks = nil }), []FieldError{fe(O("trunks"), "at least one trunk is required")}},
		{"outbound unknown and duplicate trunks", out(func(r *OutboundRoute) { r.Trunks = []int64{tPrimary, 77, tPrimary, 0} }), []FieldError{
			fe(O("trunks[1]"), "unknown trunk 77"), fe(O("trunks[2]"), "trunk 1 is listed twice"), fe(O("trunks[3]"), "unknown trunk 0")}},
		{"failover codes", out(func(r *OutboundRoute) { r.FailoverCodes = []int{503, 200, 700} }), []FieldError{
			fe(O("failoverCodes[1]"), "must be a SIP failure code (400 to 699)"), fe(O("failoverCodes[2]"), "must be a SIP failure code (400 to 699)")}},
		// Inbound routes.
		{"inbound duplicate position", in(func(r *InboundRoute) { r.Position = 10 }), []FieldError{fe(I("position"), "duplicate position 10 (also inbound[0])")}},
		{"inbound duplicate id", in(func(r *InboundRoute) { r.ID = 1 }), []FieldError{fe(I("id"), "duplicate id 1 (also inbound[0])")}},
		{"inbound name", in(func(r *InboundRoute) { r.Name = "" }), []FieldError{fe(I("name"), "is required")}},
		{"did kind", in(func(r *InboundRoute) { r.DIDKind = "glob" }), []FieldError{fe(I("didKind"), `must be "any", "exact", "prefix" or "regex"`)}},
		{"any with a DID", in(func(r *InboundRoute) { r.DIDKind = "any" }), []FieldError{fe(I("did"), `must be empty when didKind is "any"`)}},
		{"exact DID not a number", in(func(r *InboundRoute) { r.DID = "sales" }), []FieldError{fe(I("did"), "must be a number (+, digits, * and #; whitespace is ignored)")}},
		{"prefix DID empty", in(func(r *InboundRoute) { r.DIDKind, r.DID = "prefix", " " }), []FieldError{fe(I("did"), "must be a number (+, digits, * and #; whitespace is ignored)")}},
		{"DID regex does not compile", in(func(r *InboundRoute) { r.DIDKind, r.DID = "regex", "(" }), []FieldError{
			fe(I("did"), "does not compile: error parsing regexp: missing closing ): `(`")}},
		{"DID regex too long", in(func(r *InboundRoute) { r.DIDKind, r.DID = "regex", strings.Repeat("1", 501) }), []FieldError{fe(I("did"), "is longer than 500 characters")}},
		{"inbound unknown trunk", in(func(r *InboundRoute) { r.TrunkID = 77 }), []FieldError{fe(I("trunkId"), "unknown trunk 77")}},
		{"inbound negative trunk", in(func(r *InboundRoute) { r.TrunkID = -1 }), []FieldError{fe(I("trunkId"), "must be positive (or null for any trunk)")}},
		{"header regex without name", in(func(r *InboundRoute) { r.HeaderRegex = "x" }), []FieldError{fe(I("headerName"), "is required with a header regex")}},
		{"header name without regex", in(func(r *InboundRoute) { r.HeaderName = "X-A" }), []FieldError{fe(I("headerRegex"), "is required with a header name")}},
		{"header name and regex invalid", in(func(r *InboundRoute) { r.HeaderName, r.HeaderRegex = "X A:", "(" }), []FieldError{
			fe(I("headerName"), "must be a SIP header name"), fe(I("headerRegex"), "does not compile: error parsing regexp: missing closing ): `(`")}},
		{"header regex too long", in(func(r *InboundRoute) { r.HeaderName, r.HeaderRegex = "X-A", strings.Repeat("a", 501) }), []FieldError{
			fe(I("headerRegex"), "is longer than 500 characters")}},
		{"inbound schedule", in(func(r *InboundRoute) {
			r.Schedule = &Schedule{TimeZone: "Nowhere/City", Windows: []Window{{Days: []time.Weekday{1}, Start: "09:00", End: "17:00"}}}
		}), []FieldError{
			fe(I("schedule.timeZone"), `unknown time zone "Nowhere/City"`)}},
		{"inbound caller-ID transform", in(func(r *InboundRoute) { r.CallerID = Transform{Regex: "^(0)", Template: "${x}"} }), []FieldError{
			fe(I("callerIdTransform.template"), "references group ${x}, which the regex does not define")}},
		{"destination kind", in(func(r *InboundRoute) { r.DestinationKind = "voicemail" }), []FieldError{fe(I("destinationKind"), `must be "extension", "external", "sip_uri" or "voice_agent"`)}},
		{"extension destination must exist", in(func(r *InboundRoute) { r.Destination = "999" }), []FieldError{fe(I("destination"), `extension "999" does not exist`)}},
		{"external destination must be a number", in(func(r *InboundRoute) { r.DestinationKind, r.Destination = "external", "05 01" }), []FieldError{
			fe(I("destination"), "must be a number (+, digits, * and #)")}},
		{"SIP URI destination", in(func(r *InboundRoute) { r.DestinationKind, r.Destination = "sip_uri", "http://x" }), []FieldError{
			fe(I("destination"), "must be a sip: or sips: URI without whitespace")}},
		{"SIP URI with whitespace", in(func(r *InboundRoute) { r.DestinationKind, r.Destination = "sip_uri", "sip:a b@c" }), []FieldError{
			fe(I("destination"), "must be a sip: or sips: URI without whitespace")}},
		{"sips URI accepted", in(func(r *InboundRoute) { r.DestinationKind, r.Destination = "sip_uri", "sips:agent@ai.example.com" }), nil},
		// Extensions.
		{"extension external number", func(c *Config) { c.Extensions["104"] = "+971 4" }, []FieldError{fe(`extensions["104"].externalNumber`, "must be a number (+, digits, * and #)")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixtureConfig()
			tc.mut(&cfg)
			tbl, errs := Compile(cfg)
			if !slices.Equal(errs, tc.want) {
				t.Errorf("errors\n got %v\nwant %v", errs, tc.want)
			}
			if (tbl == nil) != (len(tc.want) > 0) {
				t.Errorf("table = %v with %d errors", tbl != nil, len(errs))
			}
		})
	}
}

// TestCompileReportsEveryProblem catches Compile stopping at the first
// problem: three independent problems give three FieldErrors, in input
// order.
func TestCompileReportsEveryProblem(t *testing.T) {
	cfg := fixtureConfig()
	cfg.Trunks[0].Mode = "x"
	cfg.Outbound[1].Number = Transform{Regex: "^1", Template: "${1}"}
	cfg.Inbound[2].Destination = "ftp://x"
	tbl, errs := Compile(cfg)
	want := []FieldError{
		{Path: "trunks[0].mode", Message: `must be "registration" or "ip"`},
		{Path: "outbound[1].numberTransform.template", Message: "references group ${1}, but the regex defines only 0"},
		{Path: "inbound[2].destination", Message: "must be a sip: or sips: URI without whitespace"},
	}
	if tbl != nil || !slices.Equal(errs, want) {
		t.Errorf("Compile = %v, %v; want nil, %v", tbl != nil, errs, want)
	}
}

func TestCompileEmpty(t *testing.T) {
	tbl, errs := Compile(Config{})
	if errs != nil || tbl == nil {
		t.Fatalf("empty config: %v", errs)
	}
	d := tbl.Decide(Call{FromExtension: "101", Number: "0501234567"}, nil)
	checkDecision(t, d, outcome{Kind: KindReject, Number: "0501234567", Code: 404}, []string{
		`Internal extension lookup "0501234567" -> no match`,
		`No outbound route matched "0501234567"`,
	})
}

// TestRouteOrderIsPosition catches routes being tried in slice order
// rather than by Position.
func TestRouteOrderIsPosition(t *testing.T) {
	cfg := Config{
		Trunks: []Trunk{{ID: 1, Name: "t", Mode: "ip", Enabled: true, Destinations: []Destination{{Host: "h"}}}},
		Outbound: []OutboundRoute{
			{ID: 1, Position: 2, Name: "second", MatchKind: "prefix", Match: "0", Trunks: []int64{1}, Enabled: true},
			{ID: 2, Position: 1, Name: "first", MatchKind: "prefix", Match: "0", Trunks: []int64{1}, Enabled: true},
		},
	}
	d := mustCompile(t, cfg).Decide(Call{FromExtension: "1", Number: "01"}, nil)
	if d.Route != "first" {
		t.Errorf("route = %q, want the lower position", d.Route)
	}
}

func TestSchedule(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	utc := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	loc := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, berlin)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	win := func(start, end string, days ...time.Weekday) *Schedule {
		return &Schedule{TimeZone: "Europe/Berlin", Windows: []Window{{Days: days, Start: start, End: end}}}
	}
	fri, sat, sun, mon := time.Friday, time.Saturday, time.Sunday, time.Monday
	cases := []struct {
		name string
		s    *Schedule
		at   time.Time
		open bool
	}{
		{"nil schedule is always open", nil, utc("2026-01-01 00:00"), true},
		{"inside", win("09:00", "17:00", mon), loc("2026-10-05 12:00"), true},
		{"start inclusive", win("09:00", "17:00", mon), loc("2026-10-05 09:00"), true},
		{"before start", win("09:00", "17:00", mon), loc("2026-10-05 08:59"), false},
		{"end exclusive", win("09:00", "17:00", mon), loc("2026-10-05 17:00"), false},
		{"last minute", win("09:00", "17:00", mon), time.Date(2026, 10, 5, 16, 59, 59, 999, berlin), true},
		{"wrong day", win("09:00", "17:00", mon), loc("2026-10-06 12:00"), false},
		{"zone, not UTC: 08:30 UTC is 10:30 CEST", win("10:00", "11:00", mon), utc("2026-10-05 08:30"), true},
		{"zone decides the weekday", win("00:00", "01:00", sat), utc("2026-10-02 22:30"), true}, // Fri UTC, Sat 00:30 Berlin
		// Crossing midnight: Friday 22:00 to Saturday 06:00.
		{"cross-midnight before start", win("22:00", "06:00", fri), loc("2026-10-02 21:59"), false},
		{"cross-midnight evening", win("22:00", "06:00", fri), loc("2026-10-02 23:30"), true},
		{"cross-midnight at midnight", win("22:00", "06:00", fri), loc("2026-10-03 00:00"), true},
		{"cross-midnight next morning", win("22:00", "06:00", fri), loc("2026-10-03 05:59"), true},
		{"cross-midnight end exclusive", win("22:00", "06:00", fri), loc("2026-10-03 06:00"), false},
		{"cross-midnight other day's evening", win("22:00", "06:00", fri), loc("2026-10-03 23:00"), false},
		{"cross-midnight morning of the start day", win("22:00", "06:00", fri), loc("2026-10-02 05:00"), false},
		{"start == end is 24 hours", win("09:00", "09:00", mon), loc("2026-10-06 08:59"), true},
		{"start == end closes at end", win("09:00", "09:00", mon), loc("2026-10-06 09:00"), false},
		{"whole day", win("00:00", "00:00", sun), loc("2026-10-04 23:59"), true},
		{"whole day ends at midnight", win("00:00", "00:00", sun), loc("2026-10-05 00:00"), false},
		{"week wrap: Saturday night into Sunday", win("22:00", "02:00", sat), loc("2026-10-04 01:00"), true},
		{"several windows", &Schedule{TimeZone: "Europe/Berlin", Windows: []Window{
			{Days: []time.Weekday{mon}, Start: "09:00", End: "12:00"},
			{Days: []time.Weekday{mon}, Start: "13:00", End: "17:00"},
		}}, loc("2026-10-05 12:30"), false},
		{"several windows, second", &Schedule{TimeZone: "Europe/Berlin", Windows: []Window{
			{Days: []time.Weekday{mon}, Start: "09:00", End: "12:00"},
			{Days: []time.Weekday{mon}, Start: "13:00", End: "17:00"},
		}}, loc("2026-10-05 13:00"), true},
		// Spring forward, Sunday 2026-03-29: 02:00 CET jumps to 03:00 CEST.
		{"spring: 01:59 CET open", win("01:30", "03:30", sun), utc("2026-03-29 00:59"), true},
		{"spring: next minute is 03:00 CEST, still open", win("01:30", "03:30", sun), utc("2026-03-29 01:00"), true},
		{"spring: 03:30 CEST closes", win("01:30", "03:30", sun), utc("2026-03-29 01:30"), false},
		{"spring: skipped-hour window never opens (before)", win("02:00", "03:00", sun), utc("2026-03-29 00:59"), false},
		{"spring: skipped-hour window never opens (after)", win("02:00", "03:00", sun), utc("2026-03-29 01:00"), false},
		{"spring: Monday 08:00 is 06:00 UTC", win("08:00", "17:00", mon), utc("2026-03-30 06:00"), true},
		{"spring: Monday 05:59 UTC is 07:59 CEST", win("08:00", "17:00", mon), utc("2026-03-30 05:59"), false},
		{"spring: the Friday before, 08:00 is 07:00 UTC", win("08:00", "17:00", fri), utc("2026-03-27 07:00"), true},
		// Fall back, Sunday 2026-10-25: 03:00 CEST returns to 02:00 CET.
		{"fall: 01:59 CEST closed", win("02:00", "03:00", sun), utc("2026-10-24 23:59"), false},
		{"fall: first 02:00 (CEST) open", win("02:00", "03:00", sun), utc("2026-10-25 00:00"), true},
		{"fall: first 02:59 (CEST) open", win("02:00", "03:00", sun), utc("2026-10-25 00:59"), true},
		{"fall: second 02:00 (CET) open again", win("02:00", "03:00", sun), utc("2026-10-25 01:00"), true},
		{"fall: second 02:59 (CET) open", win("02:00", "03:00", sun), utc("2026-10-25 01:59"), true},
		{"fall: 03:00 CET closed", win("02:00", "03:00", sun), utc("2026-10-25 02:00"), false},
		{"fall: Monday 08:00 is 07:00 UTC", win("08:00", "17:00", mon), utc("2026-10-26 07:00"), true},
		{"fall: Monday 06:59 UTC is 07:59 CET", win("08:00", "17:00", mon), utc("2026-10-26 06:59"), false},
		// Invalid schedules are never open.
		{"invalid zone", &Schedule{TimeZone: "Nowhere/City", Windows: []Window{{Days: []time.Weekday{mon}, Start: "00:00", End: "00:00"}}}, loc("2026-10-05 12:00"), false},
		{"invalid window", win("9:00", "17:00", mon), loc("2026-10-05 12:00"), false},
	}
	for _, tc := range cases {
		if got := tc.s.Open(tc.at); got != tc.open {
			t.Errorf("%s: Open(%s) = %v, want %v", tc.name, tc.at.In(berlin).Format("Mon 2006-01-02 15:04 MST"), got, tc.open)
		}
	}
}

func TestTrunkForSource(t *testing.T) {
	cfg := fixtureConfig()
	cfg.Trunks[2].SourceCIDRs = []netip.Prefix{netip.MustParsePrefix("10.9.0.0/16")} // carrier-off (disabled)
	cfg.ResolvedIPs = map[int64][]netip.Addr{
		tV6:     {netip.MustParseAddr("2001:db8::5")},
		tBackup: {netip.MustParseAddr("::ffff:203.0.113.7")},
	}
	tbl := mustCompile(t, cfg)

	// Shared carrier IP: backup also covers primary's range.
	shared := fixtureConfig()
	shared.Trunks[0].SourceCIDRs = append(shared.Trunks[0].SourceCIDRs, netip.MustParsePrefix("10.0.0.0/24"))
	// Without the fixture's any-DID "Domain" route, which is bound to
	// carrier-backup and so would claim every DID for it.
	anyDID := shared.Inbound[len(shared.Inbound)-1]
	shared.Inbound = append(shared.Inbound[:len(shared.Inbound)-1],
		InboundRoute{ID: 20, Position: 300, Name: "Backup DID", DIDKind: "exact", DID: "+97149990000", TrunkID: tBackup,
			DestinationKind: "extension", Destination: "102", Enabled: true},
		InboundRoute{ID: 21, Position: 301, Name: "Backup disabled", DIDKind: "exact", DID: "97149990001", TrunkID: tBackup,
			DestinationKind: "extension", Destination: "102", Enabled: false},
	)
	sharedTbl := mustCompile(t, shared)
	shared.Inbound = append(shared.Inbound, anyDID)
	sharedAnyTbl := mustCompile(t, shared)

	cases := []struct {
		name string
		tbl  *Table
		ip   string
		did  string
		want int64 // 0: no trunk
	}{
		{"source CIDR", tbl, "10.0.0.3", "", tPrimary},
		{"outside every CIDR", tbl, "192.0.2.1", "", 0},
		{"just outside the /29", tbl, "10.0.0.8", "", 0},
		{"IPv4-mapped IPv6 address", tbl, "::ffff:10.0.0.3", "", tPrimary},
		{"resolved IPv6 destination", tbl, "2001:db8::5", "", tV6},
		{"resolved destination given as mapped IPv6", tbl, "203.0.113.7", "", tBackup},
		{"disabled trunk never matches", tbl, "10.9.1.1", "", 0},
		{"shared IP, no DID: lowest ID", sharedTbl, "10.0.0.3", "", tPrimary},
		{"shared IP, DID routed to the lowest-ID trunk", sharedTbl, "10.0.0.3", "+97142223333", tPrimary},
		{"shared IP, DID routed to the other trunk (+ and space normalised)", sharedTbl, "10.0.0.3", "9714 9990000", tBackup},
		{"shared IP, DID only on any-trunk routes: lowest ID", sharedTbl, "10.0.0.3", "+97142224001", tPrimary},
		{"shared IP, DID on a disabled route: lowest ID", sharedTbl, "10.0.0.3", "97149990001", tPrimary},
		{"shared IP, unknown DID: lowest ID", sharedTbl, "10.0.0.3", "123", tPrimary},
		{"shared IP, an any-DID route bound to the other trunk claims every DID", sharedAnyTbl, "10.0.0.3", "123", tBackup},
		{"only one trunk matches: DID irrelevant", sharedTbl, "10.0.0.200", "+97142223333", tBackup},
	}
	for _, tc := range cases {
		tr, ok := tc.tbl.TrunkForSource(netip.MustParseAddr(tc.ip), tc.did)
		var got int64
		if ok {
			got = tr.ID
		}
		if got != tc.want || ok != (tc.want != 0) {
			t.Errorf("%s: TrunkForSource(%s, %q) = %d, %v; want %d", tc.name, tc.ip, tc.did, got, ok, tc.want)
		}
	}
	if _, ok := tbl.TrunkForSource(netip.Addr{}, ""); ok {
		t.Error("the zero address matched a trunk")
	}
	if tr, ok := tbl.Trunk(tV6); !ok || tr.Name != "carrier-v6" {
		t.Errorf("Trunk(%d) = %v, %v", tV6, tr, ok)
	}
	if _, ok := tbl.Trunk(99); ok {
		t.Error("Trunk(99) found")
	}
}
