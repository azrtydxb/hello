package routing

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

// Fixture trunk IDs.
const (
	tPrimary int64 = 1
	tBackup  int64 = 2
	tOff     int64 = 3
	tV6      int64 = 4
)

// trunkSecret is assembled at runtime so secret scanners do not trip on a
// literal; the trace must never contain it.
var trunkSecret = strings.Join([]string{"pw", "primary", "x9"}, "-")

func dubai(t testing.TB) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Dubai")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// at parses "2006-01-02 15:04" in Asia/Dubai.
func at(t testing.TB, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, dubai(t))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func fixtureConfig() Config {
	weekdays := []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday}
	every := []time.Weekday{0, 1, 2, 3, 4, 5, 6}
	return Config{
		Trunks: []Trunk{
			// Deliberately not in ID order: Compile sorts.
			{ID: tBackup, Name: "carrier-backup", Mode: "ip", Enabled: true,
				Destinations: []Destination{
					{Host: "backup-a.example.net", Priority: 10, Weight: 1},
					{Host: "10.0.1.5", Port: 5080, Priority: 0, Weight: 1},
				},
				SourceCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.1.0/24")}},
			{ID: tPrimary, Name: "carrier-primary", Mode: "registration", Username: "u1", Password: trunkSecret,
				DefaultCallerID: "+97140000001", Enabled: true,
				Destinations: []Destination{{Host: "10.0.0.5", Port: 5060, Weight: 1}},
				SourceCIDRs:  []netip.Prefix{netip.MustParsePrefix("10.0.0.0/29")}},
			{ID: tOff, Name: "carrier-off", Mode: "ip", Enabled: false,
				Destinations: []Destination{{Host: "10.0.3.5", Port: 5060}}},
			{ID: tV6, Name: "carrier-v6", Mode: "ip", Enabled: true, DefaultCallerID: "+97140000004",
				Destinations: []Destination{{Host: "2001:db8::5", Port: 5060, Weight: 1}}},
		},
		Extensions: map[string]string{"101": "+97142220101", "102": "", "103": ""},
		Outbound: []OutboundRoute{
			{ID: 4, Position: 40, Name: "UAE Mobile", MatchKind: "regex", Match: `^05[0-9]{8}$`, Enabled: true,
				Number: Transform{Regex: `^0(5[0-9]{8})$`, Template: "+971${1}"},
				Trunks: []int64{tPrimary, tBackup}},
			{ID: 1, Position: 10, Name: "Emergency", MatchKind: "prefix", Match: "112", Emergency: true, Enabled: true,
				Trunks: []int64{tPrimary, tBackup}},
			{ID: 2, Position: 20, Name: "Disabled Intl", MatchKind: "prefix", Match: "00", Enabled: false,
				Trunks: []int64{tPrimary}},
			{ID: 3, Position: 30, Name: "International", MatchKind: "prefix", Match: "00", Enabled: true,
				SourceExtensions: []string{"101"},
				Number:           Transform{Strip: 2, Prefix: "+"},
				Trunks:           []int64{tPrimary, tBackup}, FailoverCodes: []int{503}},
			{ID: 5, Position: 50, Name: "Office Hours", MatchKind: "regex", Match: `^800[0-9]+$`, Enabled: true,
				Schedule: &Schedule{TimeZone: "Asia/Dubai", Windows: []Window{{Days: weekdays, Start: "08:00", End: "18:00"}}},
				Number:   Transform{Prefix: "+971"},
				Trunks:   []int64{tBackup}},
			{ID: 6, Position: 60, Name: "After Hours", MatchKind: "regex", Match: `^800`, Enabled: true,
				Trunks: []int64{tV6}},
			{ID: 7, Position: 70, Name: "Named", MatchKind: "regex", Match: `^9[0-9]+$`, Enabled: true,
				Number:   Transform{Strip: 1, Regex: `^(?P<area>[0-9]{2})(?P<num>[0-9]+)$`, Template: "+44${area}${num}"},
				CallerID: Transform{Strip: 1, Prefix: "00"},
				Trunks:   []int64{tPrimary}},
			{ID: 8, Position: 80, Name: "Regex miss", MatchKind: "prefix", Match: "7", Enabled: true,
				Number: Transform{Prefix: "0", Regex: `^9`, Template: "8"},
				Trunks: []int64{tPrimary}},
			{ID: 9, Position: 90, Name: "Short", MatchKind: "prefix", Match: "5", Enabled: true,
				Number: Transform{Regex: `^5(.*)$`, Template: "${1}"},
				Trunks: []int64{tPrimary}},
			{ID: 10, Position: 100, Name: "Raw", MatchKind: "prefix", Match: "6", Enabled: true,
				Trunks: []int64{tPrimary}},
			{ID: 11, Position: 110, Name: "All down", MatchKind: "prefix", Match: "4", Enabled: true,
				Trunks: []int64{tOff, tPrimary, tBackup}},
			{ID: 12, Position: 120, Name: "Caller check", MatchKind: "prefix", Match: "3", Enabled: true,
				CallerID: Transform{Prefix: "+9714"},
				Trunks:   []int64{tBackup}},
		},
		Inbound: []InboundRoute{
			{ID: 1, Position: 10, Name: "Sales", DIDKind: "exact", DID: "+971 4 222 3333", TrunkID: tPrimary,
				DestinationKind: "extension", Destination: "101", Enabled: true},
			{ID: 2, Position: 20, Name: "Support header", DIDKind: "prefix", DID: "97142224",
				HeaderName: "X-Department", HeaderRegex: `^support$`,
				DestinationKind: "extension", Destination: "102", Enabled: true},
			{ID: 3, Position: 30, Name: "Support fallback", DIDKind: "prefix", DID: "+97142224",
				DestinationKind: "sip_uri", Destination: "sip:agent@ai.example.com", Enabled: true},
			{ID: 4, Position: 40, Name: "Night forward", DIDKind: "regex", DID: `^\+97142225555$`,
				Schedule:        &Schedule{TimeZone: "Asia/Dubai", Windows: []Window{{Days: every, Start: "18:00", End: "08:00"}}},
				DestinationKind: "external", Destination: "0501234567", Enabled: true},
			{ID: 5, Position: 50, Name: "Day 5555", DIDKind: "exact", DID: "97142225555",
				DestinationKind: "extension", Destination: "102", Enabled: true},
			{ID: 7, Position: 70, Name: "Disabled", DIDKind: "exact", DID: "97142226666",
				DestinationKind: "extension", Destination: "101", Enabled: false},
			{ID: 8, Position: 80, Name: "Forward restricted", DIDKind: "exact", DID: "97142227777",
				DestinationKind: "external", Destination: "0012345", Enabled: true},
			{ID: 9, Position: 90, Name: "CID normalise", DIDKind: "exact", DID: "97142228888",
				CallerID:        Transform{Regex: `^0(.*)$`, Template: "+971${1}"},
				DestinationKind: "extension", Destination: "101", Enabled: true},
			{ID: 10, Position: 100, Name: "Forward mobile", DIDKind: "exact", DID: "97142229999",
				DestinationKind: "external", Destination: "0501234567", Enabled: true},
			{ID: 6, Position: 200, Name: "Domain", DIDKind: "any", TrunkID: tBackup, SIPDomain: "pbx.example.com",
				DestinationKind: "extension", Destination: "103", Enabled: true},
		},
	}
}

func mustCompile(t testing.TB, cfg Config) *Table {
	t.Helper()
	tbl, errs := Compile(cfg)
	if len(errs) > 0 {
		t.Fatalf("Compile: %v", errs)
	}
	return tbl
}

// unusable returns a TrunkUsability failing the listed trunks with the
// given reasons; emergencyOverrides lists reasons an emergency call ignores
// (like "full": emergency routes ignore concurrency limits).
func unusable(reasons map[int64]string, emergencyOverrides ...string) TrunkUsability {
	return func(id int64, emergency bool) (bool, string) {
		r, bad := reasons[id]
		if !bad {
			return true, ""
		}
		if emergency && slices.Contains(emergencyOverrides, r) {
			return true, ""
		}
		return false, r
	}
}

// outcome is the part of a Decision a case asserts, besides the trace.
type outcome struct {
	Kind      Kind
	Extension string
	SIPURI    string
	Number    string
	CallerID  string
	Route     string
	Trunks    []string
	Failover  []int
	Emergency bool
	Code      int
}

func outcomeOf(d Decision) outcome {
	o := outcome{Kind: d.Kind, Extension: d.Extension, SIPURI: d.SIPURI, Number: d.Number, CallerID: d.CallerID,
		Route: d.Route, Failover: d.FailoverCodes, Emergency: d.Emergency, Code: d.RejectCode}
	for _, c := range d.Candidates {
		o.Trunks = append(o.Trunks, c.Trunk.Name)
	}
	return o
}

var defaultCodes = []int{408, 480, 500, 502, 503, 504}

const (
	trPrimary = "Trunk carrier-primary usable (credentials: configured); destinations: 10.0.0.5:5060"
	trBackup  = "Trunk carrier-backup usable (credentials: none); destinations: 10.0.1.5:5080, backup-a.example.net"
	trV6      = "Trunk carrier-v6 usable (credentials: none); destinations: [2001:db8::5]:5060"
	srcP      = "Source trunk carrier-primary"
	cidExt101 = "Caller ID +97142220101 (external number of extension 101)"
	cidPrimDf = "Caller ID +97140000001 (default caller ID of trunk carrier-primary)"
	outUAE    = "Outbound to +971501234567 via carrier-primary, carrier-backup; failover on 408, 480, 500, 502, 503, 504"
	uaeMatch  = `Route "UAE Mobile" matched (regex ^05[0-9]{8}$)`
	uaeRw     = "Rewrite 0501234567 -> +971501234567"
	domainTr1 = `Route "Domain" skipped: only for trunk 2, call is from trunk carrier-primary`
)

func TestRouteMatchAndRewrite(t *testing.T) {
	tbl := mustCompile(t, fixtureConfig())
	mon10 := at(t, "2026-10-05 10:00") // Monday
	fri10 := at(t, "2026-10-09 10:00") // Friday
	mon23 := at(t, "2026-10-05 23:00")
	tue0759 := at(t, "2026-10-06 07:59")
	ext := func(from, num string) Call { return Call{FromExtension: from, Number: num, At: mon10} }
	trunk := func(id int64, num, cid string) Call {
		return Call{FromTrunk: id, Number: num, CallerID: cid, At: mon10}
	}
	withAt := func(c Call, a time.Time) Call { c.At = a; return c }
	withDomain := func(c Call, d string) Call { c.SIPDomain = d; return c }
	withHeader := func(c Call, name, v string) Call {
		c.Header = func(n string) string {
			if strings.EqualFold(n, name) {
				return v
			}
			return ""
		}
		return c
	}
	primaryUnhealthy := unusable(map[int64]string{tPrimary: "unhealthy"})
	primaryFull := unusable(map[int64]string{tPrimary: "full"}, "full")
	allDown := unusable(map[int64]string{tPrimary: "unhealthy", tBackup: "full"})

	cases := []struct {
		name   string
		call   Call
		usable TrunkUsability
		want   outcome
		trace  []string
	}{
		{
			name: "internal extension",
			call: ext("101", "102"),
			want: outcome{Kind: KindInternal, Extension: "102", Number: "102", CallerID: "101"},
			trace: []string{
				`Internal extension lookup "102" -> extension 102`,
			},
		},
		{
			name: "emergency, caller ID falls back to trunk default",
			call: ext("102", "112"),
			want: outcome{Kind: KindOutbound, Number: "112", CallerID: "+97140000001", Route: "Emergency",
				Trunks: []string{"carrier-primary", "carrier-backup"}, Failover: defaultCodes, Emergency: true},
			trace: []string{
				`Internal extension lookup "112" -> no match`,
				`Route "Emergency" matched (prefix 112; emergency)`,
				"Number 112 unchanged (no rewrite)",
				trPrimary, trBackup, cidPrimDf,
				"Outbound to 112 via carrier-primary, carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name:   "emergency passes emergency=true to usability (full trunk still used)",
			call:   ext("101", "112"),
			usable: primaryFull,
			want: outcome{Kind: KindOutbound, Number: "112", CallerID: "+97142220101", Route: "Emergency",
				Trunks: []string{"carrier-primary", "carrier-backup"}, Failover: defaultCodes, Emergency: true},
			trace: []string{
				`Internal extension lookup "112" -> no match`,
				`Route "Emergency" matched (prefix 112; emergency)`,
				"Number 112 unchanged (no rewrite)",
				trPrimary, trBackup, cidExt101,
				"Outbound to 112 via carrier-primary, carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name:   "non-emergency skips a full trunk",
			call:   ext("101", "0501234567"),
			usable: primaryFull,
			want: outcome{Kind: KindOutbound, Number: "+971501234567", CallerID: "+97142220101", Route: "UAE Mobile",
				Trunks: []string{"carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "0501234567" -> no match`,
				uaeMatch, uaeRw,
				"Trunk carrier-primary skipped: full",
				trBackup, cidExt101,
				"Outbound to +971501234567 via carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "disabled route skipped, source extension allowed, strip and prefix, route failover codes",
			call: ext("101", "00441234567"),
			want: outcome{Kind: KindOutbound, Number: "+441234567", CallerID: "+97142220101", Route: "International",
				Trunks: []string{"carrier-primary", "carrier-backup"}, Failover: []int{503}},
			trace: []string{
				`Internal extension lookup "00441234567" -> no match`,
				`Route "Disabled Intl" skipped: disabled`,
				`Route "International" matched (prefix 00)`,
				"Rewrite 00441234567 -> +441234567",
				trPrimary, trBackup, cidExt101,
				"Outbound to +441234567 via carrier-primary, carrier-backup; failover on 503",
			},
		},
		{
			name: "source extension not allowed -> 404",
			call: ext("102", "00441234567"),
			want: outcome{Kind: KindReject, Number: "00441234567", Code: 404},
			trace: []string{
				`Internal extension lookup "00441234567" -> no match`,
				`Route "Disabled Intl" skipped: disabled`,
				`Route "International" skipped: extension 102 is not in its source extensions`,
				`No outbound route matched "00441234567"`,
			},
		},
		{
			name: "regex match and template rewrite",
			call: ext("101", "0501234567"),
			want: outcome{Kind: KindOutbound, Number: "+971501234567", CallerID: "+97142220101", Route: "UAE Mobile",
				Trunks: []string{"carrier-primary", "carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "0501234567" -> no match`,
				uaeMatch, uaeRw, trPrimary, trBackup, cidExt101, outUAE,
			},
		},
		{
			name:   "unhealthy primary skipped; caller ID falls back to extension number (backup has no default)",
			call:   ext("103", "0501234567"),
			usable: primaryUnhealthy,
			want: outcome{Kind: KindOutbound, Number: "+971501234567", CallerID: "103", Route: "UAE Mobile",
				Trunks: []string{"carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "0501234567" -> no match`,
				uaeMatch, uaeRw,
				"Trunk carrier-primary skipped: unhealthy",
				trBackup,
				"Caller ID 103 (extension number)",
				"Outbound to +971501234567 via carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name:   "every trunk unusable -> 503 with reasons",
			call:   ext("101", "0501234567"),
			usable: allDown,
			want:   outcome{Kind: KindReject, Number: "+971501234567", Route: "UAE Mobile", Code: 503},
			trace: []string{
				`Internal extension lookup "0501234567" -> no match`,
				uaeMatch, uaeRw,
				"Trunk carrier-primary skipped: unhealthy",
				"Trunk carrier-backup skipped: full",
				`No usable trunk on route "UAE Mobile" (carrier-primary: unhealthy; carrier-backup: full)`,
			},
		},
		{
			name:   "usability without a reason is traced as unusable",
			call:   ext("101", "0501234567"),
			usable: func(int64, bool) (bool, string) { return false, "" },
			want:   outcome{Kind: KindReject, Number: "+971501234567", Route: "UAE Mobile", Code: 503},
			trace: []string{
				`Internal extension lookup "0501234567" -> no match`,
				uaeMatch, uaeRw,
				"Trunk carrier-primary skipped: unusable",
				"Trunk carrier-backup skipped: unusable",
				`No usable trunk on route "UAE Mobile" (carrier-primary: unusable; carrier-backup: unusable)`,
			},
		},
		{
			name: "regex does not match -> 404",
			call: ext("101", "050123"),
			want: outcome{Kind: KindReject, Number: "050123", Code: 404},
			trace: []string{
				`Internal extension lookup "050123" -> no match`,
				`No outbound route matched "050123"`,
			},
		},
		{
			name: "schedule open",
			call: ext("101", "8001234"),
			want: outcome{Kind: KindOutbound, Number: "+9718001234", CallerID: "+97142220101", Route: "Office Hours",
				Trunks: []string{"carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "8001234" -> no match`,
				`Route "Office Hours" matched (regex ^800[0-9]+$)`,
				"Rewrite 8001234 -> +9718001234",
				trBackup, cidExt101,
				"Outbound to +9718001234 via carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "schedule closed is traced and the next route wins",
			call: withAt(ext("102", "8001234"), fri10),
			want: outcome{Kind: KindOutbound, Number: "8001234", CallerID: "+97140000004", Route: "After Hours",
				Trunks: []string{"carrier-v6"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "8001234" -> no match`,
				`Route "Office Hours" skipped: schedule closed (Fri 10:00 Asia/Dubai)`,
				`Route "After Hours" matched (regex ^800)`,
				"Number 8001234 unchanged (no rewrite)",
				trV6,
				"Caller ID +97140000004 (default caller ID of trunk carrier-v6)",
				"Outbound to 8001234 via carrier-v6; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "schedule end is exclusive",
			call: withAt(ext("101", "8001234"), at(t, "2026-10-08 18:00")), // Thursday
			want: outcome{Kind: KindOutbound, Number: "8001234", CallerID: "+97142220101", Route: "After Hours",
				Trunks: []string{"carrier-v6"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "8001234" -> no match`,
				`Route "Office Hours" skipped: schedule closed (Thu 18:00 Asia/Dubai)`,
				`Route "After Hours" matched (regex ^800)`,
				"Number 8001234 unchanged (no rewrite)",
				trV6, cidExt101,
				"Outbound to 8001234 via carrier-v6; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "schedule evaluated in its zone, not the call's",
			call: withAt(ext("101", "8001234"), time.Date(2026, 10, 8, 13, 59, 0, 0, time.UTC)), // Thu 17:59 Dubai
			want: outcome{Kind: KindOutbound, Number: "+9718001234", CallerID: "+97142220101", Route: "Office Hours",
				Trunks: []string{"carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "8001234" -> no match`,
				`Route "Office Hours" matched (regex ^800[0-9]+$)`,
				"Rewrite 8001234 -> +9718001234",
				trBackup, cidExt101,
				"Outbound to +9718001234 via carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "named groups in template; caller-ID transform",
			call: ext("101", "9201234567"),
			want: outcome{Kind: KindOutbound, Number: "+44201234567", CallerID: "0097142220101", Route: "Named",
				Trunks: []string{"carrier-primary"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "9201234567" -> no match`,
				`Route "Named" matched (regex ^9[0-9]+$)`,
				"Rewrite 9201234567 -> +44201234567",
				trPrimary, cidExt101,
				"Caller ID rewrite +97142220101 -> 0097142220101",
				"Outbound to +44201234567 via carrier-primary; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "caller-ID transform applied to the trunk default",
			call: ext("102", "9201234567"),
			want: outcome{Kind: KindOutbound, Number: "+44201234567", CallerID: "0097140000001", Route: "Named",
				Trunks: []string{"carrier-primary"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "9201234567" -> no match`,
				`Route "Named" matched (regex ^9[0-9]+$)`,
				"Rewrite 9201234567 -> +44201234567",
				trPrimary, cidPrimDf,
				"Caller ID rewrite +97140000001 -> 0097140000001",
				"Outbound to +44201234567 via carrier-primary; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "regex that does not match is a traced no-op",
			call: ext("101", "7123"),
			want: outcome{Kind: KindOutbound, Number: "07123", CallerID: "+97142220101", Route: "Regex miss",
				Trunks: []string{"carrier-primary"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "7123" -> no match`,
				`Route "Regex miss" matched (prefix 7)`,
				"Number transform regex ^9 did not match 07123; regex step skipped",
				"Rewrite 7123 -> 07123",
				trPrimary, cidExt101,
				"Outbound to 07123 via carrier-primary; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "template drops the matched prefix",
			call: ext("101", "5123"),
			want: outcome{Kind: KindOutbound, Number: "123", CallerID: "+97142220101", Route: "Short",
				Trunks: []string{"carrier-primary"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "5123" -> no match`,
				`Route "Short" matched (prefix 5)`,
				"Rewrite 5123 -> 123",
				trPrimary, cidExt101,
				"Outbound to 123 via carrier-primary; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "rewrite to empty -> 484",
			call: ext("101", "5"),
			want: outcome{Kind: KindReject, Number: "5", Route: "Short", Code: 484},
			trace: []string{
				`Internal extension lookup "5" -> no match`,
				`Route "Short" matched (prefix 5)`,
				`Rewrite 5 failed: result "" is not a valid number (only +, digits, * and #)`,
			},
		},
		{
			name: "number with letters is rejected even without a transform -> 484",
			call: ext("101", "6abc"),
			want: outcome{Kind: KindReject, Number: "6abc", Route: "Raw", Code: 484},
			trace: []string{
				`Internal extension lookup "6abc" -> no match`,
				`Route "Raw" matched (prefix 6)`,
				`Number "6abc" rejected: not a valid number (only +, digits, * and #)`,
			},
		},
		{
			name:   "disabled trunk skipped without asking usability; others unusable -> 503",
			call:   ext("101", "4000"),
			usable: unusable(map[int64]string{tPrimary: "unhealthy", tBackup: "full"}),
			want:   outcome{Kind: KindReject, Number: "4000", Route: "All down", Code: 503},
			trace: []string{
				`Internal extension lookup "4000" -> no match`,
				`Route "All down" matched (prefix 4)`,
				"Number 4000 unchanged (no rewrite)",
				"Trunk carrier-off skipped: disabled",
				"Trunk carrier-primary skipped: unhealthy",
				"Trunk carrier-backup skipped: full",
				`No usable trunk on route "All down" (carrier-off: disabled; carrier-primary: unhealthy; carrier-backup: full)`,
			},
		},
		{
			name: "nil usability: every enabled trunk is usable, route order kept",
			call: ext("101", "4000"),
			want: outcome{Kind: KindOutbound, Number: "4000", CallerID: "+97142220101", Route: "All down",
				Trunks: []string{"carrier-primary", "carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "4000" -> no match`,
				`Route "All down" matched (prefix 4)`,
				"Number 4000 unchanged (no rewrite)",
				"Trunk carrier-off skipped: disabled",
				trPrimary, trBackup, cidExt101,
				"Outbound to 4000 via carrier-primary, carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "caller-ID transform on the extension number",
			call: ext("103", "3000"),
			want: outcome{Kind: KindOutbound, Number: "3000", CallerID: "+9714103", Route: "Caller check",
				Trunks: []string{"carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				`Internal extension lookup "3000" -> no match`,
				`Route "Caller check" matched (prefix 3)`,
				"Number 3000 unchanged (no rewrite)",
				trBackup,
				"Caller ID 103 (extension number)",
				"Caller ID rewrite 103 -> +9714103",
				"Outbound to 3000 via carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "caller-ID rewrite that is not a number -> 500",
			call: ext("alice", "3000"),
			want: outcome{Kind: KindReject, Number: "3000", Route: "Caller check", Code: 500},
			trace: []string{
				`Internal extension lookup "3000" -> no match`,
				`Route "Caller check" matched (prefix 3)`,
				"Number 3000 unchanged (no rewrite)",
				trBackup,
				`Caller ID "alice" (extension number)`,
				`Caller ID rewrite "alice" failed: result "+9714alice" is not a valid number (only +, digits, * and #)`,
			},
		},
		{
			name: "control characters are quoted in the trace",
			call: ext("101", "\n112"),
			want: outcome{Kind: KindReject, Number: "\n112", Code: 404},
			trace: []string{
				`Internal extension lookup "\n112" -> no match`,
				`No outbound route matched "\n112"`,
			},
		},
		{
			name: "empty number -> 404",
			call: ext("101", ""),
			want: outcome{Kind: KindReject, Code: 404},
			trace: []string{
				`Internal extension lookup "" -> no match`,
				`No outbound route matched ""`,
			},
		},
		{
			name:  "no source -> 500",
			call:  Call{Number: "102"},
			want:  outcome{Kind: KindReject, Code: 500},
			trace: []string{"Invalid call: no source extension or trunk"},
		},
		{
			name:  "both sources -> 500",
			call:  Call{FromExtension: "101", FromTrunk: tPrimary, Number: "102"},
			want:  outcome{Kind: KindReject, Code: 500},
			trace: []string{"Invalid call: both a source extension and a source trunk are set"},
		},
		// Inbound.
		{
			name: "inbound exact DID with trunk condition",
			call: trunk(tPrimary, "+97142223333", "+971501112222"),
			want: outcome{Kind: KindInbound, Extension: "101", Number: "+97142223333", CallerID: "+971501112222", Route: "Sales"},
			trace: []string{
				srcP,
				"Called DID +97142223333",
				`Route "Sales" matched (DID exact +971 4 222 3333)`,
				"Caller ID +971501112222 as received",
				"Destination: extension 101",
			},
		},
		{
			name: "inbound DID whitespace and missing + normalised",
			call: trunk(tPrimary, " 971 42223333 ", "+971501112222"),
			want: outcome{Kind: KindInbound, Extension: "101", Number: "97142223333", CallerID: "+971501112222", Route: "Sales"},
			trace: []string{
				srcP,
				`Called DID " 971 42223333 " normalised to 97142223333`,
				`Route "Sales" matched (DID exact +971 4 222 3333, leading + ignored)`,
				"Caller ID +971501112222 as received",
				"Destination: extension 101",
			},
		},
		{
			name: "inbound tabs and newlines in the DID",
			call: trunk(tPrimary, "\t+971 4 222 3333\n", ""),
			want: outcome{Kind: KindInbound, Extension: "101", Number: "+97142223333", Route: "Sales"},
			trace: []string{
				srcP,
				`Called DID "\t+971 4 222 3333\n" normalised to +97142223333`,
				`Route "Sales" matched (DID exact +971 4 222 3333)`,
				"Caller ID: none received",
				"Destination: extension 101",
			},
		},
		{
			name: "inbound trunk condition fails; domain condition fails -> 404",
			call: trunk(tBackup, "+97142223333", "+971501112222"),
			want: outcome{Kind: KindReject, Number: "+97142223333", Code: 404},
			trace: []string{
				"Source trunk carrier-backup",
				"Called DID +97142223333",
				`Route "Sales" skipped: only for trunk 1, call is from trunk carrier-backup`,
				`Route "Domain" skipped: SIP domain "" is not pbx.example.com`,
				"No inbound route matched DID +97142223333",
			},
		},
		{
			name: "inbound any DID with SIP domain (case-insensitive)",
			call: withDomain(trunk(tBackup, "+97142223333", "+971501112222"), "PBX.Example.com"),
			want: outcome{Kind: KindInbound, Extension: "103", Number: "+97142223333", CallerID: "+971501112222", Route: "Domain"},
			trace: []string{
				"Source trunk carrier-backup",
				"Called DID +97142223333",
				`Route "Sales" skipped: only for trunk 1, call is from trunk carrier-backup`,
				`Route "Domain" matched (any DID)`,
				"Caller ID +971501112222 as received",
				"Destination: extension 103",
			},
		},
		{
			name: "inbound header condition matches (value trimmed)",
			call: withHeader(trunk(tPrimary, "+97142224001", ""), "X-Department", " support "),
			want: outcome{Kind: KindInbound, Extension: "102", Number: "+97142224001", Route: "Support header"},
			trace: []string{
				srcP,
				"Called DID +97142224001",
				`Route "Support header" matched (DID prefix 97142224, leading + ignored)`,
				"Caller ID: none received",
				"Destination: extension 102",
			},
		},
		{
			name: "inbound header condition fails; SIP URI destination",
			call: withHeader(trunk(tPrimary, "+97142224001", "+971501112222"), "X-Department", "sales"),
			want: outcome{Kind: KindInbound, SIPURI: "sip:agent@ai.example.com", Number: "+97142224001", CallerID: "+971501112222", Route: "Support fallback"},
			trace: []string{
				srcP,
				"Called DID +97142224001",
				`Route "Support header" skipped: header X-Department "sales" does not match ^support$`,
				`Route "Support fallback" matched (DID prefix +97142224)`,
				"Caller ID +971501112222 as received",
				"Destination: SIP URI sip:agent@ai.example.com",
			},
		},
		{
			name: "inbound header absent (no Header func)",
			call: trunk(tPrimary, "+97142224001", ""),
			want: outcome{Kind: KindInbound, SIPURI: "sip:agent@ai.example.com", Number: "+97142224001", Route: "Support fallback"},
			trace: []string{
				srcP,
				"Called DID +97142224001",
				`Route "Support header" skipped: header X-Department "" does not match ^support$`,
				`Route "Support fallback" matched (DID prefix +97142224)`,
				"Caller ID: none received",
				"Destination: SIP URI sip:agent@ai.example.com",
			},
		},
		{
			name: "inbound schedule closed is traced; next route wins",
			call: trunk(tPrimary, "+97142225555", "+971509998888"),
			want: outcome{Kind: KindInbound, Extension: "102", Number: "+97142225555", CallerID: "+971509998888", Route: "Day 5555"},
			trace: []string{
				srcP,
				"Called DID +97142225555",
				`Route "Night forward" skipped: schedule closed (Mon 10:00 Asia/Dubai)`,
				`Route "Day 5555" matched (DID exact 97142225555, leading + ignored)`,
				"Caller ID +971509998888 as received",
				"Destination: extension 102",
			},
		},
		{
			name: "inbound external destination recurses into outbound routing",
			call: withAt(trunk(tPrimary, "+97142225555", "+971509998888"), mon23),
			want: outcome{Kind: KindOutbound, Number: "+971501234567", CallerID: "+971509998888", Route: "UAE Mobile",
				Trunks: []string{"carrier-primary", "carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				srcP,
				"Called DID +97142225555",
				`Route "Night forward" matched (DID regex ^\+97142225555$)`,
				"Caller ID +971509998888 as received",
				"Destination: external number 0501234567, routed outbound as from trunk carrier-primary",
				uaeMatch, uaeRw, trPrimary, trBackup,
				"Caller ID +971509998888 (inbound caller ID)",
				outUAE,
			},
		},
		{
			name: "inbound DID regex matches with the leading + added; cross-midnight window next morning",
			call: withAt(trunk(tPrimary, "97142225555", "+971509998888"), tue0759),
			want: outcome{Kind: KindOutbound, Number: "+971501234567", CallerID: "+971509998888", Route: "UAE Mobile",
				Trunks: []string{"carrier-primary", "carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				srcP,
				"Called DID 97142225555",
				`Route "Night forward" matched (DID regex ^\+97142225555$ on +97142225555)`,
				"Caller ID +971509998888 as received",
				"Destination: external number 0501234567, routed outbound as from trunk carrier-primary",
				uaeMatch, uaeRw, trPrimary, trBackup,
				"Caller ID +971509998888 (inbound caller ID)",
				outUAE,
			},
		},
		{
			name: "forwarded call without caller ID uses the first candidate's default",
			call: trunk(tPrimary, "97142229999", ""),
			want: outcome{Kind: KindOutbound, Number: "+971501234567", CallerID: "+97140000001", Route: "UAE Mobile",
				Trunks: []string{"carrier-primary", "carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				srcP,
				"Called DID 97142229999",
				`Route "Forward mobile" matched (DID exact 97142229999)`,
				"Caller ID: none received",
				"Destination: external number 0501234567, routed outbound as from trunk carrier-primary",
				uaeMatch, uaeRw, trPrimary, trBackup, cidPrimDf, outUAE,
			},
		},
		{
			name:   "forwarded call with no caller ID available at all",
			call:   trunk(tPrimary, "97142229999", ""),
			usable: primaryUnhealthy,
			want: outcome{Kind: KindOutbound, Number: "+971501234567", Route: "UAE Mobile",
				Trunks: []string{"carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				srcP,
				"Called DID 97142229999",
				`Route "Forward mobile" matched (DID exact 97142229999)`,
				"Caller ID: none received",
				"Destination: external number 0501234567, routed outbound as from trunk carrier-primary",
				uaeMatch, uaeRw,
				"Trunk carrier-primary skipped: unhealthy",
				trBackup,
				"Caller ID: none available",
				"Outbound to +971501234567 via carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "forwarded call never uses routes restricted to source extensions -> 404",
			call: trunk(tPrimary, "97142227777", "+971501112222"),
			want: outcome{Kind: KindReject, Number: "97142227777", CallerID: "+971501112222", Route: "Forward restricted", Code: 404},
			trace: []string{
				srcP,
				"Called DID 97142227777",
				`Route "Forward restricted" matched (DID exact 97142227777)`,
				"Caller ID +971501112222 as received",
				"Destination: external number 0012345, routed outbound as from trunk carrier-primary",
				`Route "Disabled Intl" skipped: disabled`,
				`Route "International" skipped: restricted to source extensions, and the call is from trunk carrier-primary`,
				`No outbound route matched "0012345"`,
			},
		},
		{
			name: "inbound disabled route skipped -> 404",
			call: trunk(tPrimary, "97142226666", ""),
			want: outcome{Kind: KindReject, Number: "97142226666", Code: 404},
			trace: []string{
				srcP,
				"Called DID 97142226666",
				`Route "Disabled" skipped: disabled`,
				domainTr1,
				"No inbound route matched DID 97142226666",
			},
		},
		{
			name: "inbound caller-ID transform",
			call: trunk(tPrimary, "97142228888", "0501234567"),
			want: outcome{Kind: KindInbound, Extension: "101", Number: "97142228888", CallerID: "+971501234567", Route: "CID normalise"},
			trace: []string{
				srcP,
				"Called DID 97142228888",
				`Route "CID normalise" matched (DID exact 97142228888)`,
				"Caller ID rewrite 0501234567 -> +971501234567",
				"Destination: extension 101",
			},
		},
		{
			name: "inbound caller-ID transform regex miss keeps the value",
			call: trunk(tPrimary, "97142228888", "+971501234567"),
			want: outcome{Kind: KindInbound, Extension: "101", Number: "97142228888", CallerID: "+971501234567", Route: "CID normalise"},
			trace: []string{
				srcP,
				"Called DID 97142228888",
				`Route "CID normalise" matched (DID exact 97142228888)`,
				"Caller ID transform regex ^0(.*)$ did not match +971501234567; regex step skipped",
				"Caller ID rewrite +971501234567 -> +971501234567",
				"Destination: extension 101",
			},
		},
		{
			name: "inbound caller ID that is not a number is presented as received",
			call: trunk(tPrimary, "97142228888", "anonymous"),
			want: outcome{Kind: KindInbound, Extension: "101", Number: "97142228888", CallerID: "anonymous", Route: "CID normalise"},
			trace: []string{
				srcP,
				"Called DID 97142228888",
				`Route "CID normalise" matched (DID exact 97142228888)`,
				`Caller ID transform regex ^0(.*)$ did not match "anonymous"; regex step skipped`,
				`Caller ID rewrite "anonymous" failed: result "anonymous" is not a valid number (only +, digits, * and #); presenting it as received`,
				"Destination: extension 101",
			},
		},
		{
			name: "inbound junk DID -> 404",
			call: trunk(tPrimary, "abc", ""),
			want: outcome{Kind: KindReject, Number: "abc", Code: 404},
			trace: []string{
				srcP,
				`Called DID "abc"`,
				domainTr1,
				`No inbound route matched DID "abc"`,
			},
		},
		{
			name:  "unknown source trunk -> 403",
			call:  trunk(99, "97142223333", ""),
			want:  outcome{Kind: KindReject, Code: 403},
			trace: []string{"Source trunk 99 is not configured"},
		},
		{
			name:  "disabled source trunk -> 403",
			call:  trunk(tOff, "97142223333", ""),
			want:  outcome{Kind: KindReject, Code: 403},
			trace: []string{"Source trunk carrier-off is disabled"},
		},
	}
	if len(cases) < 40 {
		t.Fatalf("only %d cases; the plan requires at least 40", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := tbl.Decide(tc.call, tc.usable)
			checkDecision(t, d, tc.want, tc.trace)
		})
	}
}

func checkDecision(t *testing.T, d Decision, want outcome, trace []string) {
	t.Helper()
	got := outcomeOf(d)
	if !equalOutcome(got, want) {
		t.Errorf("decision\n got %+v\nwant %+v", got, want)
	}
	texts := make([]string, len(d.Trace))
	for i, s := range d.Trace {
		texts[i] = s.Text
		if s.N != i+1 {
			t.Errorf("step %d numbered %d", i+1, s.N)
		}
	}
	if !slices.Equal(texts, trace) {
		t.Errorf("trace\n got %q\nwant %q", texts, trace)
	}
	switch {
	case d.Kind == KindReject && (len(texts) == 0 || d.Reason != texts[len(texts)-1]):
		t.Errorf("reject reason %q is not the last trace step", d.Reason)
	case d.Kind != KindReject && d.Reason != "":
		t.Errorf("reason %q on a %s decision", d.Reason, d.Kind)
	}
	for _, s := range texts {
		if strings.Contains(s, trunkSecret) {
			t.Errorf("trace step contains the trunk password: %q", s)
		}
	}
}

func equalOutcome(a, b outcome) bool {
	return a.Kind == b.Kind && a.Extension == b.Extension && a.SIPURI == b.SIPURI && a.Number == b.Number &&
		a.CallerID == b.CallerID && a.Route == b.Route && slices.Equal(a.Trunks, b.Trunks) &&
		slices.Equal(a.Failover, b.Failover) && a.Emergency == b.Emergency && a.Code == b.Code
}

func TestDecideNilTable(t *testing.T) {
	var tbl *Table
	d := tbl.Decide(Call{FromExtension: "101", Number: "102"}, nil)
	checkDecision(t, d, outcome{Kind: KindReject, Code: 503}, []string{"No routing table loaded"})
	if _, ok := tbl.Trunk(1); ok {
		t.Error("nil table has a trunk")
	}
	if _, ok := tbl.TrunkForSource(netip.MustParseAddr("10.0.0.1"), ""); ok {
		t.Error("nil table matched a source")
	}
}

func TestDecideCandidates(t *testing.T) {
	tbl := mustCompile(t, fixtureConfig())
	d := tbl.Decide(Call{FromExtension: "101", Number: "0501234567"}, nil)
	if len(d.Candidates) != 2 {
		t.Fatalf("candidates = %d", len(d.Candidates))
	}
	b := d.Candidates[1]
	want := []Destination{{Host: "10.0.1.5", Port: 5080, Priority: 0, Weight: 1}, {Host: "backup-a.example.net", Priority: 10, Weight: 1}}
	if b.Trunk.ID != tBackup || !slices.Equal(b.Destinations, want) {
		t.Errorf("backup candidate = %+v %+v", b.Trunk.ID, b.Destinations)
	}
	// The Decision owns its slices: changing them leaves the Table alone.
	d.FailoverCodes[0] = 999
	b.Destinations[0].Host = "mutated"
	d2 := tbl.Decide(Call{FromExtension: "101", Number: "0501234567"}, nil)
	if d2.FailoverCodes[0] != 408 || d2.Candidates[1].Destinations[0].Host != "10.0.1.5" {
		t.Errorf("table mutated through a decision: %v %v", d2.FailoverCodes, d2.Candidates[1].Destinations)
	}
	tr, _ := tbl.Trunk(tBackup)
	if tr.Destinations[1].Host != "10.0.1.5" {
		t.Errorf("trunk destinations reordered in place: %+v", tr.Destinations)
	}
}

// TestFailoverCodeDefaults catches a route with no failover codes not
// getting the spec default, and a route's own codes being ignored.
func TestFailoverCodeDefaults(t *testing.T) {
	tbl := mustCompile(t, fixtureConfig())
	d := tbl.Decide(Call{FromExtension: "101", Number: "0501234567"}, nil)
	if !slices.Equal(d.FailoverCodes, []int{408, 480, 500, 502, 503, 504}) {
		t.Errorf("default failover codes = %v", d.FailoverCodes)
	}
	d = tbl.Decide(Call{FromExtension: "101", Number: "00441234567"}, nil)
	if !slices.Equal(d.FailoverCodes, []int{503}) {
		t.Errorf("route failover codes = %v", d.FailoverCodes)
	}
}

// TestConfigCopied catches the Table aliasing the caller's Config.
func TestConfigCopied(t *testing.T) {
	cfg := fixtureConfig()
	tbl := mustCompile(t, cfg)
	cfg.Trunks[1].Destinations[0].Host = "mutated"
	cfg.Trunks[1].Name = "mutated"
	cfg.Outbound[0].Trunks[0] = tV6
	cfg.Extensions["0501234567"] = ""
	d := tbl.Decide(Call{FromExtension: "101", Number: "0501234567"}, nil)
	if d.Kind != KindOutbound || d.Candidates[0].Trunk.Name != "carrier-primary" || d.Candidates[0].Destinations[0].Host != "10.0.0.5" {
		t.Errorf("table follows the caller's config: %+v", outcomeOf(d))
	}
}

func TestDestinationOrdering(t *testing.T) {
	dsts := []Destination{
		{Host: "p1-zero", Priority: 1, Weight: 0},
		{Host: "p1-heavy", Priority: 1, Weight: 3},
		{Host: "p1-light", Priority: 1, Weight: 1},
		{Host: "p0", Priority: 0, Weight: 5},
		{Host: "p2", Priority: 2, Weight: 0},
	}
	first := map[string]int{}
	const n = 4000
	for i := range n {
		num := "05012" + strings.Repeat("0", 5-len(itoa(i))) + itoa(i)
		o := orderDestinations(dsts, destinationSeed(num, 7))
		again := orderDestinations(dsts, destinationSeed(num, 7))
		if !slices.Equal(o, again) {
			t.Fatalf("order for %s not deterministic: %v vs %v", num, o, again)
		}
		hosts := make([]string, len(o))
		for k, d := range o {
			hosts[k] = d.Host
		}
		if hosts[0] != "p0" || hosts[3] != "p1-zero" || hosts[4] != "p2" {
			t.Fatalf("order for %s = %v: priority ascending, weight 0 last within its priority", num, hosts)
		}
		first[hosts[1]]++
	}
	// Weights 3:1 → the heavy destination comes first about 75% of the time.
	if share := float64(first["p1-heavy"]) / n; share < 0.70 || share > 0.80 {
		t.Errorf("heavy destination first in %.2f of calls, want about 0.75 (%v)", share, first)
	}
	if first["p1-light"] == 0 {
		t.Error("light destination never first")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func TestApplyTransform(t *testing.T) {
	cases := []struct {
		tr      Transform
		in, out string
		errSub  string
	}{
		{Transform{}, "0501234567", "0501234567", ""},
		{Transform{Strip: 1, Prefix: "+971"}, "0501234567", "+971501234567", ""},
		{Transform{Strip: 99}, "123", "", "not a valid number"},
		{Transform{Strip: 3, Prefix: "9"}, "123", "9", ""},
		{Transform{Regex: `^0(5[0-9]{8})$`, Template: "+971${1}"}, "0501234567", "+971501234567", ""},
		{Transform{Regex: `^0(?P<n>[0-9]+)$`, Template: "00971${n}"}, "0501234567", "00971501234567", ""},
		{Transform{Regex: `^0`, Template: "+971"}, "0501234567", "+971501234567", ""},           // partial match keeps the rest
		{Transform{Regex: `^9`, Template: "8"}, "0501234567", "0501234567", ""},                 // no match: no-op
		{Transform{Regex: `(5)`, Template: "${0}${1}"}, "0501234567", "05501234567", ""},        // leftmost match only
		{Transform{Prefix: "*31#"}, "0501234567", "*31#0501234567", ""},                         // * and # allowed
		{Transform{Regex: `^(.*)$`, Template: "${1}"}, "05a", "", "not a valid number"},         // runtime check
		{Transform{Regex: `^(5)(.*)$`, Template: "${2}"}, "5", "", "not a valid number"},        // empty result
		{Transform{Regex: `^(.*)$`, Template: "+${1}"}, "+971", "", "not a valid number"},       // "++971"
		{Transform{Regex: `^(.*)$`, Template: "${2}"}, "1", "", "the regex defines only 1"},     // validation
		{Transform{Regex: `^(.*)$`, Template: "$1"}, "1", "", "bare $"},                         // syntax
		{Transform{Regex: `(`, Template: "1"}, "1", "", "transform.regex: does not compile"},    // compile
		{Transform{Template: "1"}, "1", "", "transform.template: a template requires a regex"},  // orphan template
		{Transform{Strip: -1}, "1", "", "transform.strip: must not be negative"},                // negative
		{Transform{Prefix: "9+"}, "1", "", "transform.prefix"},                                  // + not first
		{Transform{Regex: strings.Repeat("a", 501), Template: "1"}, "1", "", "longer than 500"}, // length
	}
	for _, tc := range cases {
		out, err := ApplyTransform(tc.tr, tc.in)
		switch {
		case tc.errSub == "" && err != nil:
			t.Errorf("ApplyTransform(%+v, %q): %v", tc.tr, tc.in, err)
		case tc.errSub != "" && (err == nil || !strings.Contains(err.Error(), tc.errSub)):
			t.Errorf("ApplyTransform(%+v, %q) err = %v, want containing %q", tc.tr, tc.in, err, tc.errSub)
		case out != tc.out:
			t.Errorf("ApplyTransform(%+v, %q) = %q, want %q", tc.tr, tc.in, out, tc.out)
		}
	}
}

// TestTemplateGroups catches the template group check accepting a
// reference to a group the regex does not define (or rejecting a valid one).
func TestTemplateGroups(t *testing.T) {
	re := `^(?P<cc>[0-9]{3})([0-9]+)$`
	ok := []string{"${0}", "${1}", "${2}", "${cc}", "+${cc}${2}", "00${1}${2}", "*${2}#"}
	bad := map[string]string{
		"${3}":           "references group ${3}, but the regex defines only 2",
		"${name}":        "references group ${name}, which the regex does not define",
		"${01}":          "reference ${01} has a leading zero",
		"$1":             "a bare $ is not allowed: write ${1} or ${name}",
		"$cc":            "a bare $ is not allowed: write ${1} or ${name}",
		"$$":             "a bare $ is not allowed: write ${1} or ${name}",
		"${1":            "unterminated ${ reference",
		"${}":            "empty ${} reference",
		"${a-b}":         "reference ${a-b} is not a group number or name",
		"x${1}":          `literal "x" is not allowed: only +, digits, * and # and ${N} or ${name} references`,
		"${99999999999}": "references group ${99999999999}, but the regex defines only 2",
	}
	for _, tmpl := range ok {
		var errs []FieldError
		if _, good := compileTransform(Transform{Regex: re, Template: tmpl}, "t", &errs); !good {
			t.Errorf("template %q rejected: %v", tmpl, errs)
		}
	}
	for tmpl, msg := range bad {
		var errs []FieldError
		compileTransform(Transform{Regex: re, Template: tmpl}, "t", &errs)
		if len(errs) != 1 || errs[0] != (FieldError{Path: "t.template", Message: msg}) {
			t.Errorf("template %q: errs = %v, want %q", tmpl, errs, msg)
		}
	}
}
