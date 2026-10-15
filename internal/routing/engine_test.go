package routing

import (
	"fmt"
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

	// Internal dialing and the loop guard (trunk plan Task 1).
	verdictOffP = "Internal dialing off on trunk carrier-primary"
	verdictOffB = "Internal dialing off on trunk carrier-backup"
	loopSkipsP  = "Trunk carrier-primary skipped: call came from it"
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
				verdictOffP,
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
				verdictOffP,
				`Route "Sales" matched (DID exact +971 4 222 3333, leading + ignored)`,
				"Caller ID +971501112222 as received",
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
				verdictOffB,
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
				verdictOffB,
				`Route "Sales" skipped: only for trunk 1, call is from trunk carrier-backup`,
				`Route "Domain" matched (any DID)`,
				"Caller ID +971501112222 as received",
				"Destination: extension 103",
			},
		},
		{
			name: "inbound header condition matches (value trimmed)",
			call: withHeader(trunk(tPrimary, "+97142224001", ""), "X-Department", " support "),
			want: outcome{Kind: KindInbound, Extension: "102", Number: "+97142224001", CallerID: "+97140000001", Route: "Support header"},
			trace: []string{
				srcP,
				"Called DID +97142224001",
				verdictOffP,
				`Route "Support header" matched (DID prefix 97142224, leading + ignored)`,
				"Caller ID +97140000001 (default caller ID of trunk carrier-primary)",
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
				verdictOffP,
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
				verdictOffP,
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
				verdictOffP,
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
				Trunks: []string{"carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				srcP,
				"Called DID +97142225555",
				verdictOffP,
				`Route "Night forward" matched (DID regex ^\+97142225555$)`,
				"Caller ID +971509998888 as received",
				"Destination: external number 0501234567, routed outbound as from trunk carrier-primary",
				uaeMatch, uaeRw, loopSkipsP, trBackup,
				"Caller ID +971509998888 (inbound caller ID)",
				"Outbound to +971501234567 via carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "inbound DID regex matches with the leading + added; cross-midnight window next morning",
			call: withAt(trunk(tPrimary, "97142225555", "+971509998888"), tue0759),
			want: outcome{Kind: KindOutbound, Number: "+971501234567", CallerID: "+971509998888", Route: "UAE Mobile",
				Trunks: []string{"carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				srcP,
				"Called DID 97142225555",
				verdictOffP,
				`Route "Night forward" matched (DID regex ^\+97142225555$ on +97142225555)`,
				"Caller ID +971509998888 as received",
				"Destination: external number 0501234567, routed outbound as from trunk carrier-primary",
				uaeMatch, uaeRw, loopSkipsP, trBackup,
				"Caller ID +971509998888 (inbound caller ID)",
				"Outbound to +971501234567 via carrier-backup; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "forwarded call skips the source trunk; no caller ID left to show",
			call: trunk(tPrimary, "97142229999", ""),
			want: outcome{Kind: KindOutbound, Number: "+971501234567", Route: "UAE Mobile",
				Trunks: []string{"carrier-backup"}, Failover: defaultCodes},
			trace: []string{
				srcP,
				"Called DID 97142229999",
				verdictOffP,
				`Route "Forward mobile" matched (DID exact 97142229999)`,
				"Caller ID: none received",
				"Destination: external number 0501234567, routed outbound as from trunk carrier-primary",
				uaeMatch, uaeRw, loopSkipsP, trBackup,
				"Caller ID: none available",
				"Outbound to +971501234567 via carrier-backup; failover on 408, 480, 500, 502, 503, 504",
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
				verdictOffP,
				`Route "Forward mobile" matched (DID exact 97142229999)`,
				"Caller ID: none received",
				"Destination: external number 0501234567, routed outbound as from trunk carrier-primary",
				uaeMatch, uaeRw, loopSkipsP, trBackup,
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
				verdictOffP,
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
				verdictOffP,
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
				verdictOffP,
				`Route "CID normalise" matched (DID exact 97142228888)`,
				"Caller ID 0501234567 as received",
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
				verdictOffP,
				`Route "CID normalise" matched (DID exact 97142228888)`,
				"Caller ID +971501234567 as received",
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
				verdictOffP,
				`Route "CID normalise" matched (DID exact 97142228888)`,
				`Caller ID "anonymous" as received`,
				`Caller ID transform regex ^0(.*)$ did not match "anonymous"; regex step skipped`,
				`Caller ID rewrite "anonymous" failed: result "anonymous" is not a valid number (only +, digits, * and #); presenting the chosen value`,
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
				verdictOffP,
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

// internalConfig is a small config for the internal-number tests: three
// trunks with different internal dialing policies, and one any-DID route
// with the "internal" destination beside fixed-target routes.
func internalConfig() Config {
	return Config{
		Trunks: []Trunk{
			{ID: 1, Name: "peer", Mode: "ip", Enabled: true, DefaultCallerID: "+97140000001",
				Destinations:    []Destination{{Host: "10.2.0.1", Port: 5060}},
				InternalDialing: Dialing{Mode: "patterns", Patterns: []string{"10XX", "+971455501"}}},
			{ID: 2, Name: "open", Mode: "ip", Enabled: true,
				Destinations:    []Destination{{Host: "10.2.0.2", Port: 5060}},
				InternalDialing: Dialing{Mode: "all"}},
			{ID: 3, Name: "closed", Mode: "ip", Enabled: true,
				Destinations: []Destination{{Host: "10.2.0.3", Port: 5060}}},
			{ID: 4, Name: "picky", Mode: "ip", Enabled: true,
				Destinations:    []Destination{{Host: "10.2.0.4", Port: 5060}},
				InternalDialing: Dialing{Mode: "patterns", Patterns: []string{"2XX"}}},
		},
		Extensions: map[string]string{"1012": "", "1013": "+97142220101"},
		Outbound: []OutboundRoute{
			{ID: 1, Position: 10, Name: "Out", MatchKind: "prefix", Match: "0", Enabled: true, Trunks: []int64{1}},
		},
		Inbound: []InboundRoute{
			{ID: 1, Position: 10, Name: "Any extension", DIDKind: "any", DestinationKind: "internal", Enabled: true},
			{ID: 2, Position: 5, Name: "Fixed 1012", DIDKind: "exact", DID: "9912",
				DestinationKind: "extension", Destination: "1012", Enabled: true},
			{ID: 3, Position: 30, Name: "Ladder normalise", DIDKind: "exact", DID: "9913",
				DestinationKind: "extension", Destination: "1013",
				CallerID: Transform{Regex: `^0(.*)$`, Template: "+971${1}"}, Enabled: true},
		},
	}
}

// TestInternalDestination (spec S-1): the "internal" destination rings the
// extension the normalised DID names, whatever the fixed "extension"
// destination still means, and a miss rejects 404 as "not an internal
// number".
func TestInternalDestination(t *testing.T) {
	tbl := mustCompile(t, internalConfig())
	trunk := func(id int64, num, cid string) Call {
		return Call{FromTrunk: id, Number: num, CallerID: cid, At: at(t, "2026-10-05 10:00")}
	}
	cases := []struct {
		name  string
		call  Call
		want  outcome
		trace []string
	}{
		{
			name: "dial-by-extension rings the DID's extension",
			call: trunk(2, "1012", "+971501112222"),
			want: outcome{Kind: KindInbound, Extension: "1012", Number: "1012", CallerID: "+971501112222", Route: "Any extension"},
			trace: []string{
				"Source trunk open",
				"Called DID 1012",
				"Trunk open may dial 1012: all internal numbers",
				`Route "Any extension" matched (any DID)`,
				"Caller ID +971501112222 as received",
				"Destination: internal",
				"Internal lookup 1012 -> extension 1012",
			},
		},
		{
			name: "leading + is ignored, as the DID match ignores it",
			call: trunk(2, "+1012", ""),
			want: outcome{Kind: KindInbound, Extension: "1012", Number: "+1012", CallerID: "<open>", Route: "Any extension"},
			trace: []string{
				"Source trunk open",
				"Called DID +1012",
				"Trunk open may dial +1012: all internal numbers",
				`Route "Any extension" matched (any DID)`,
				"Caller ID <open> (trunk name)",
				"Destination: internal",
				"Internal lookup +1012 -> extension 1012",
			},
		},
		{
			name: "whitespace is stripped, as the DID match strips it",
			call: trunk(2, "10 12", ""),
			want: outcome{Kind: KindInbound, Extension: "1012", Number: "1012", CallerID: "<open>", Route: "Any extension"},
			trace: []string{
				"Source trunk open",
				`Called DID "10 12" normalised to 1012`,
				"Trunk open may dial 1012: all internal numbers",
				`Route "Any extension" matched (any DID)`,
				"Caller ID <open> (trunk name)",
				"Destination: internal",
				"Internal lookup 1012 -> extension 1012",
			},
		},
		{
			name: "not an internal number -> 404",
			call: trunk(2, "9999", ""),
			want: outcome{Kind: KindReject, Number: "9999", CallerID: "<open>", Route: "Any extension", Code: 404},
			trace: []string{
				"Source trunk open",
				"Called DID 9999",
				"Trunk open may dial 9999: all internal numbers",
				`Route "Any extension" matched (any DID)`,
				"Caller ID <open> (trunk name)",
				"Destination: internal",
				`Internal lookup 9999 -> no match`,
				`DID 9999 is not an internal number`,
			},
		},
		{
			name: "extension destination stays a fixed target, ignoring the DID",
			call: trunk(2, "9912", ""),
			want: outcome{Kind: KindInbound, Extension: "1012", Number: "9912", CallerID: "<open>", Route: "Fixed 1012"},
			trace: []string{
				"Source trunk open",
				"Called DID 9912",
				"Trunk open may dial 9912: all internal numbers",
				`Route "Fixed 1012" matched (DID exact 9912)`,
				"Caller ID <open> (trunk name)",
				"Destination: extension 1012",
			},
		},
		{
			name: "ladder: trunk default, then the route transform",
			call: trunk(1, "9913", ""),
			want: outcome{Kind: KindInbound, Extension: "1013", Number: "9913", CallerID: "+97140000001", Route: "Ladder normalise"},
			trace: []string{
				"Source trunk peer",
				"Called DID 9913",
				"Trunk peer may not dial 9913: no pattern matches",
				`Route "Any extension" skipped: internal dialing to 9913 not allowed on trunk peer`,
				`Route "Ladder normalise" matched (DID exact 9913)`,
				"Caller ID +97140000001 (default caller ID of trunk peer)",
				"Caller ID transform regex ^0(.*)$ did not match +97140000001; regex step skipped",
				"Caller ID rewrite +97140000001 -> +97140000001",
				"Destination: extension 1013",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkDecision(t, tbl.Decide(tc.call, nil), tc.want, tc.trace)
		})
	}
}

// TestTrunkInternalDialingPolicy (spec S-2): the mode x pattern x DID
// matrix, the traced verdict, the traced skip on mode off, and the compile
// validation of the policy.
func TestTrunkInternalDialingPolicy(t *testing.T) {
	tbl := mustCompile(t, internalConfig())
	peer, ok := tbl.Trunk(1)
	if !ok {
		t.Fatal("peer trunk missing")
	}

	// The verdict matrix: dialingAllows per mode and pattern class.
	dids := map[string]string{ // DID -> the pattern that must allow it ("" none)
		"1012":       "10XX",
		"1013":       "10XX",
		"+1012":      "10XX", // leading + toggled, as the DID match does
		"+971455501": "+971455501",
		"971455501":  "+971455501",
		"2012":       "", // outside every pattern
		"101X":       "", // X in the dialled number is not a digit
		"+971455502": "",
		"*31#":       "", // feature codes are not in the extension namespace
	}
	if picky, ok := tbl.Trunk(4); !ok {
		t.Fatal("picky trunk missing")
	} else {
		for did := range dids {
			if ok, pat := tbl.dialingAllows(picky, did); ok || pat != "" {
				t.Errorf("picky dialingAllows(%q) = %v, %q; want denied", did, ok, pat)
			}
		}
	}
	for did, want := range dids {
		ok, pat := tbl.dialingAllows(peer, did)
		if ok != (want != "") || pat != want {
			t.Errorf("dialingAllows(%q) = %v, %q; want %v, %q", did, ok, pat, want != "", want)
		}
	}
	// First match in list order wins and is named.
	if _, pat := tbl.dialingAllows(peer, "1012"); pat != "10XX" {
		t.Errorf("winning pattern = %q, want 10XX", pat)
	}
	// Mode "all" allows everything and says so.
	if open, _ := tbl.Trunk(2); open != nil {
		ok, pat := tbl.dialingAllows(open, "7777")
		if !ok || pat != "all" {
			t.Errorf("open dialingAllows(7777) = %v, %q; want true, all", ok, pat)
		}
	}
	// Mode off allows nothing.
	if closed, _ := tbl.Trunk(3); closed != nil {
		if ok, pat := tbl.dialingAllows(closed, "1012"); ok || pat != "" {
			t.Errorf("closed dialingAllows(1012) = %v, %q; want denied", ok, pat)
		}
	}

	// The verdict and the skip are traced, per mode.
	mon := at(t, "2026-10-05 10:00")
	decide := func(id int64, num string) Decision {
		return tbl.Decide(Call{FromTrunk: id, Number: num, At: mon}, nil)
	}
	d := decide(3, "1012")
	checkDecision(t, d, outcome{Kind: KindReject, Number: "1012", Code: 404}, []string{
		"Source trunk closed",
		"Called DID 1012",
		"Internal dialing off on trunk closed",
		`Route "Any extension" skipped: internal dialing off on trunk closed`,
		`No inbound route matched DID 1012`,
	})
	d = decide(4, "1012")
	checkDecision(t, d, outcome{Kind: KindReject, Number: "1012", Code: 404}, []string{
		"Source trunk picky",
		"Called DID 1012",
		"Trunk picky may not dial 1012: no pattern matches",
		`Route "Any extension" skipped: internal dialing to 1012 not allowed on trunk picky`,
		`No inbound route matched DID 1012`,
	})
	// An allowed call's trace names the winning pattern.
	checkDecision(t, decide(1, "1012"), outcome{Kind: KindInbound, Extension: "1012", Number: "1012",
		CallerID: "+97140000001", Route: "Any extension"}, []string{
		"Source trunk peer",
		"Called DID 1012",
		"Trunk peer may dial 1012: pattern 10XX",
		`Route "Any extension" matched (any DID)`,
		"Caller ID +97140000001 (default caller ID of trunk peer)",
		"Destination: internal",
		"Internal lookup 1012 -> extension 1012",
	})

	// The pattern classes beside X patterns and exact numbers: a Go regex as
	// given, overlapping patterns where list order picks the winner (and the
	// trace names it), and a pattern that names a feature code.
	ipTrunk := func(id int64, name string, pats ...string) Trunk {
		return Trunk{ID: id, Name: name, Mode: "ip", Enabled: true,
			Destinations:    []Destination{{Host: fmt.Sprintf("10.4.0.%d", id), Port: 5060}},
			InternalDialing: Dialing{Mode: "patterns", Patterns: pats}}
	}
	ov := mustCompile(t, Config{
		Trunks: []Trunk{
			ipTrunk(1, "x-first", "1XXX", "10[12]2"),
			ipTrunk(2, "re-first", "10[12]2", "1XXX"),
			ipTrunk(3, "regex", `^\+9714555[0-9]{3}$`),
			ipTrunk(4, "codes", "*31#"),
			{ID: 5, Name: "all", Mode: "ip", Enabled: true,
				Destinations: []Destination{{Host: "10.4.0.5", Port: 5060}}, InternalDialing: Dialing{Mode: "all"}},
			{ID: 6, Name: "off", Mode: "ip", Enabled: true,
				Destinations: []Destination{{Host: "10.4.0.6", Port: 5060}}},
		},
		Extensions: map[string]string{"1012": ""},
		Inbound: []InboundRoute{
			{ID: 1, Position: 10, Name: "Any extension", DIDKind: "any", DestinationKind: "internal", Enabled: true},
		},
	})
	for _, c := range []struct {
		trunk int64
		did   string
		want  string // the winning pattern, "" for denied
	}{
		{1, "1012", "1XXX"},
		{2, "1012", "10[12]2"},
		{2, "1013", "1XXX"},
		{3, "+9714555123", `^\+9714555[0-9]{3}$`},
		{3, "9714555123", `^\+9714555[0-9]{3}$`}, // leading + toggled
		{3, "+97145551234", ""},
		{3, "+9714556123", ""},
		{4, "*31#", "*31#"},
	} {
		tr, _ := ov.Trunk(c.trunk)
		if ok, pat := ov.dialingAllows(tr, c.did); ok != (c.want != "") || pat != c.want {
			t.Errorf("%s dialingAllows(%q) = %v, %q; want %v, %q", tr.Name, c.did, ok, pat, c.want != "", c.want)
		}
	}
	for id, verdict := range map[int64]string{
		1: "Trunk x-first may dial 1012: pattern 1XXX",
		2: "Trunk re-first may dial 1012: pattern 10[12]2",
	} {
		d := ov.Decide(Call{FromTrunk: id, Number: "1012", At: mon}, nil)
		if d.Kind != KindInbound || d.Extension != "1012" || len(d.Trace) < 3 || d.Trace[2].Text != verdict {
			t.Errorf("trunk %d dialing 1012: kind %s ext %q trace %v; want inbound 1012 with verdict %q", id, d.Kind, d.Extension, d.Trace, verdict)
		}
	}
	// A feature code stays unreachable from a trunk whatever the mode: even
	// where the policy lets the DID through, it is not in the extension
	// namespace, so the call is rejected 404 and never becomes a feature.
	for _, id := range []int64{4, 5, 6} {
		d := ov.Decide(Call{FromTrunk: id, Number: "*31#", At: mon}, nil)
		if d.Kind != KindReject || d.RejectCode != 404 || d.Extension != "" {
			t.Errorf("trunk %d dialing *31#: kind %s code %d ext %q, want a 404 reject\n%v", id, d.Kind, d.RejectCode, d.Extension, d.Trace)
		}
	}

	// Compile validation of the policy.
	fe := func(path, msg string) FieldError { return FieldError{Path: path, Message: msg} }
	trunkMut := func(mut func(*Trunk)) func(*Config) {
		return func(c *Config) { tr := internalConfig().Trunks[0]; mut(&tr); c.Trunks = []Trunk{tr} }
	}
	cases := []struct {
		name string
		mut  func(*Config)
		want []FieldError
	}{
		{"unknown mode", trunkMut(func(t *Trunk) { t.InternalDialing = Dialing{Mode: "glob", Patterns: []string{"1XX"}} }),
			[]FieldError{fe("trunks[0].internalDialing.mode", `must be "off", "patterns" or "all"`),
				fe("trunks[0].internalDialing.patterns", `patterns are only allowed when mode is "patterns"`)}},
		{"patterns without a pattern", trunkMut(func(t *Trunk) { t.InternalDialing = Dialing{Mode: "patterns"} }),
			[]FieldError{fe("trunks[0].internalDialing.patterns", `at least one pattern is required when mode is "patterns"`)}},
		{"patterns with mode off", trunkMut(func(t *Trunk) { t.InternalDialing = Dialing{Mode: "off", Patterns: []string{"1XX"}} }),
			[]FieldError{fe("trunks[0].internalDialing.patterns", `patterns are only allowed when mode is "patterns"`)}},
		{"patterns with mode all", trunkMut(func(t *Trunk) { t.InternalDialing = Dialing{Mode: "all", Patterns: []string{"1XX"}} }),
			[]FieldError{fe("trunks[0].internalDialing.patterns", `patterns are only allowed when mode is "patterns"`)}},
		{"101 patterns", trunkMut(func(t *Trunk) {
			p := make([]string, 101)
			for i := range p {
				p[i] = "1XX"
			}
			t.InternalDialing = Dialing{Mode: "patterns", Patterns: p}
		}), []FieldError{fe("trunks[0].internalDialing.patterns", "at most 100 patterns are allowed")}},
		{"pattern at 500 characters is accepted", trunkMut(func(t *Trunk) {
			t.InternalDialing = Dialing{Mode: "patterns", Patterns: []string{strings.Repeat("1", 500)}}
		}), nil},
		{"pattern over 500 characters", trunkMut(func(t *Trunk) {
			t.InternalDialing = Dialing{Mode: "patterns", Patterns: []string{strings.Repeat("1", 501)}}
		}), []FieldError{fe("trunks[0].internalDialing.patterns[0]", "is longer than 500 characters")}},
		{"empty pattern", trunkMut(func(t *Trunk) { t.InternalDialing = Dialing{Mode: "patterns", Patterns: []string{""}} }),
			[]FieldError{fe("trunks[0].internalDialing.patterns[0]", "is empty")}},
		{"regex pattern does not compile", trunkMut(func(t *Trunk) { t.InternalDialing = Dialing{Mode: "patterns", Patterns: []string{"("}} }),
			[]FieldError{fe("trunks[0].internalDialing.patterns[0]", "does not compile: error parsing regexp: missing closing ): `(`")}},
		{"X pattern compiles to a digit class", trunkMut(func(t *Trunk) { t.InternalDialing = Dialing{Mode: "patterns", Patterns: []string{"1X2"}} }), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := internalConfig()
			tc.mut(&cfg)
			_, errs := Compile(cfg)
			switch {
			case len(tc.want) == 0 && len(errs) != 0:
				t.Errorf("errors = %v, want none", errs)
			case len(tc.want) > 0 && !slices.Equal(errs, tc.want):
				t.Errorf("errors\n got %v\nwant %v", errs, tc.want)
			}
		})
	}
}

// TestLoopPrevention (spec S-3): a trunk-sourced call may not leave through
// its own trunk, an emptied candidate list is 403 not 503, and outbound
// routing cannot be entered twice.
func TestLoopPrevention(t *testing.T) {
	cfg := Config{
		Trunks: []Trunk{
			{ID: 1, Name: "solo", Mode: "ip", Enabled: true, Destinations: []Destination{{Host: "10.3.0.1"}}},
			{ID: 2, Name: "other", Mode: "ip", Enabled: true, Destinations: []Destination{{Host: "10.3.0.2"}}},
		},
		Extensions: map[string]string{"101": ""},
		Inbound: []InboundRoute{
			{ID: 1, Position: 10, Name: "Fwd solo", DIDKind: "exact", DID: "100", DestinationKind: "external", Destination: "0500000001", Enabled: true},
			{ID: 2, Position: 20, Name: "Fwd both", DIDKind: "exact", DID: "200", DestinationKind: "external", Destination: "0500000002", Enabled: true},
			{ID: 3, Position: 30, Name: "Fwd none", DIDKind: "exact", DID: "300", DestinationKind: "external", Destination: "0500000003", Enabled: true},
		},
		Outbound: []OutboundRoute{
			{ID: 1, Position: 10, Name: "Solo out", MatchKind: "prefix", Match: "0500000001", Trunks: []int64{1}, Enabled: true},
			{ID: 2, Position: 20, Name: "Both out", MatchKind: "prefix", Match: "0500000002", Trunks: []int64{1, 2}, Enabled: true},
			{ID: 3, Position: 30, Name: "None out", MatchKind: "prefix", Match: "0500000003", Trunks: []int64{1, 2}, Enabled: true},
		},
	}
	tbl := mustCompile(t, cfg)
	mon := at(t, "2026-10-05 10:00")
	trunk := func(id int64, num string) Call { return Call{FromTrunk: id, Number: num, At: mon} }
	otherDown := unusable(map[int64]string{2: "unhealthy"})

	cases := []struct {
		name   string
		call   Call
		usable TrunkUsability
		want   outcome
		trace  []string
	}{
		{
			name: "sole trunk is the source -> 403 loop prevented",
			call: trunk(1, "100"),
			want: outcome{Kind: KindReject, Number: "0500000001", Route: "Solo out", Code: 403},
			trace: []string{
				"Source trunk solo",
				"Called DID 100",
				"Internal dialing off on trunk solo",
				`Route "Fwd solo" matched (DID exact 100)`,
				"Caller ID: none received",
				"Destination: external number 0500000001, routed outbound as from trunk solo",
				`Route "Solo out" matched (prefix 0500000001)`,
				"Number 0500000001 unchanged (no rewrite)",
				"Trunk solo skipped: call came from it",
				`Loop prevented: no trunk left on route "Solo out" (solo: call came from it)`,
			},
		},
		{
			name: "the source is skipped, the other trunk carries the call",
			call: trunk(1, "200"),
			want: outcome{Kind: KindOutbound, Number: "0500000002", Route: "Both out",
				Trunks: []string{"other"}, Failover: defaultCodes},
			trace: []string{
				"Source trunk solo",
				"Called DID 200",
				"Internal dialing off on trunk solo",
				`Route "Fwd both" matched (DID exact 200)`,
				"Caller ID: none received",
				"Destination: external number 0500000002, routed outbound as from trunk solo",
				`Route "Both out" matched (prefix 0500000002)`,
				"Number 0500000002 unchanged (no rewrite)",
				"Trunk solo skipped: call came from it",
				"Trunk other usable (credentials: none); destinations: 10.3.0.2",
				"Caller ID: none available",
				"Outbound to 0500000002 via other; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name: "a different source trunk keeps both candidates",
			call: trunk(2, "100"),
			want: outcome{Kind: KindOutbound, Number: "0500000001", Route: "Solo out",
				Trunks: []string{"solo"}, Failover: defaultCodes},
			trace: []string{
				"Source trunk other",
				"Called DID 100",
				"Internal dialing off on trunk other",
				`Route "Fwd solo" matched (DID exact 100)`,
				"Caller ID: none received",
				"Destination: external number 0500000001, routed outbound as from trunk other",
				`Route "Solo out" matched (prefix 0500000001)`,
				"Number 0500000001 unchanged (no rewrite)",
				"Trunk solo usable (credentials: none); destinations: 10.3.0.1",
				"Caller ID: none available",
				"Outbound to 0500000001 via solo; failover on 408, 480, 500, 502, 503, 504",
			},
		},
		{
			name:   "loop filter empties the list even with the rest unusable -> 403",
			call:   trunk(1, "300"),
			usable: otherDown,
			want:   outcome{Kind: KindReject, Number: "0500000003", Route: "None out", Code: 403},
			trace: []string{
				"Source trunk solo",
				"Called DID 300",
				"Internal dialing off on trunk solo",
				`Route "Fwd none" matched (DID exact 300)`,
				"Caller ID: none received",
				"Destination: external number 0500000003, routed outbound as from trunk solo",
				`Route "None out" matched (prefix 0500000003)`,
				"Number 0500000003 unchanged (no rewrite)",
				"Trunk solo skipped: call came from it",
				"Trunk other skipped: unhealthy",
				`Loop prevented: no trunk left on route "None out" (solo: call came from it; other: unhealthy)`,
			},
		},
		{
			name:   "the same route from an extension stays 503",
			call:   Call{FromExtension: "101", Number: "0500000003", At: mon},
			usable: unusable(map[int64]string{1: "unhealthy", 2: "unhealthy"}),
			want:   outcome{Kind: KindReject, Number: "0500000003", Route: "None out", Code: 503},
			trace: []string{
				`Internal extension lookup "0500000003" -> no match`,
				`Route "None out" matched (prefix 0500000003)`,
				"Number 0500000003 unchanged (no rewrite)",
				"Trunk solo skipped: unhealthy",
				"Trunk other skipped: unhealthy",
				`No usable trunk on route "None out" (solo: unhealthy; other: unhealthy)`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkDecision(t, tbl.Decide(tc.call, tc.usable), tc.want, tc.trace)
		})
	}

	// The hop guard: a call that has already been routed outbound once
	// cannot enter outbound routing again (invariant break, 403).
	var d Decision
	src, _ := tbl.Trunk(1)
	tbl.outboundRoute(&d, "0500000001", outboundSource{trunk: src, hopped: true}, Call{At: mon}, nil)
	checkDecision(t, d, outcome{Kind: KindReject, Code: 403}, []string{
		"Loop prevented: the call has already been routed outbound once",
	})
}
