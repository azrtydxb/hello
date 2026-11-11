package routing

import (
	"testing"

	"github.com/azrtydxb/hello/internal/phonebook"
)

var swiss = phonebook.Dialing{IntlPrefix: "00", CountryCode: "41"}

func entry(id int64, pattern, match, action string, trunk int64) BlockEntry {
	return BlockEntry{ID: id, Pattern: pattern, Match: match, Action: action, TrunkID: trunk}
}

func TestBlocklistOrder(t *testing.T) {
	trunk := &Trunk{ID: 7, Name: "carrier"}
	bl := CompileBlocklist([]BlockEntry{
		entry(1, "+4479*", BlockPrefix, ActionBlock, 0),
		entry(2, "+447911*", BlockPrefix, ActionBlock, 0),
		entry(3, "+447911123456", BlockExact, ActionAllow, 0),
		entry(4, "+41225550000", BlockExact, ActionBlock, 0),
		entry(5, "+41225550001", BlockExact, ActionBlock, 9), // another trunk
		entry(6, "+41225550002", BlockExact, ActionBlock, 7),
		entry(7, "+4122555*", BlockPrefix, ActionAllow, 0),
	})
	cases := []struct {
		name, caller string
		blocked      bool
		entry        int64
	}{
		{"prefix blocks", "+447900000000", true, 1},
		{"longer prefix wins for reporting", "+447911000000", true, 2},
		{"allow beats a block prefix", "+447911123456", false, 3},
		{"national spelling is normalised", "0041 22 555 0000", false, 7}, // allow prefix beats the exact block
		{"other trunk's entry does not apply", "+41225550001", false, 7},
		{"trunk entry applies on its trunk", "+41225550002", false, 7},
		{"unlisted", "+33123456789", false, 0},
		{"exact block", "+41795550000", false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := bl.Check(trunk, c.caller, false, swiss)
			if v.Blocked != c.blocked {
				t.Fatalf("blocked = %v, want %v (%s)", v.Blocked, c.blocked, v.Trace)
			}
			if c.entry != 0 && (v.Entry == nil || v.Entry.ID != c.entry) {
				t.Fatalf("entry = %+v, want id %d", v.Entry, c.entry)
			}
			if v.Blocked && (v.Code != 603 || v.Reason != ReasonBlocklist) {
				t.Fatalf("blocked verdict = %+v", v)
			}
		})
	}
	// With no allow prefix, the exact block and the trunk scope apply.
	bl = CompileBlocklist([]BlockEntry{
		entry(4, "+41225550000", BlockExact, ActionBlock, 0),
		entry(5, "+41225550001", BlockExact, ActionBlock, 9),
		entry(6, "+41225550002", BlockExact, ActionBlock, 7),
	})
	for caller, want := range map[string]bool{"022 555 0000": true, "+41225550001": false, "+41225550002": true} {
		if got := bl.Check(trunk, caller, false, swiss).Blocked; got != want {
			t.Errorf("%s blocked = %v, want %v", caller, got, want)
		}
	}
	var nilList *Blocklist
	if nilList.Check(trunk, "+41225550000", false, swiss).Blocked {
		t.Error("a nil blocklist blocked a call")
	}
}

func TestBlocklistRejectCode(t *testing.T) {
	bl := CompileBlocklist([]BlockEntry{entry(1, "+41225550000", BlockExact, ActionBlock, 0)})
	tr := &Trunk{ID: 1, Name: "t", CallerPolicy: CallerPolicy{RejectCode: 486}}
	if v := bl.Check(tr, "+41225550000", false, swiss); !v.Blocked || v.Code != 486 {
		t.Fatalf("verdict = %+v, want 486", v)
	}
}

func TestCallerPolicy(t *testing.T) {
	anon := &Trunk{Name: "a", CallerPolicy: CallerPolicy{RejectAnonymous: true}}
	ch := &Trunk{Name: "ch", CallerPolicy: CallerPolicy{AllowedCountryCodes: []string{"41", "49"}, UnknownCallerAction: UnknownBlock}}
	chAllow := &Trunk{Name: "ch2", CallerPolicy: CallerPolicy{AllowedCountryCodes: []string{"41"}, UnknownCallerAction: UnknownAllow}}
	open := &Trunk{Name: "open"}
	cases := []struct {
		name    string
		trunk   *Trunk
		caller  string
		privacy bool
		reason  string // "" means allowed
	}{
		{"anonymous user", anon, "anonymous", false, ReasonAnonymous},
		{"empty user", anon, "", false, ReasonAnonymous},
		{"privacy id", anon, "+41225550000", true, ReasonAnonymous},
		{"named caller passes", anon, "+41225550000", false, ""},
		{"anonymous allowed when not rejected", open, "anonymous", false, ""},
		{"e164 allowed country", ch, "+41225550000", false, ""},
		{"second allowed country", ch, "+4930123456", false, ""},
		{"e164 other country", ch, "+33123456789", false, ReasonCountry},
		{"national with country code set", ch, "0225550000", false, ""},
		{"00 prefix", ch, "0033123456789", false, ReasonCountry},
		{"unparseable blocked", ch, "ab12", false, ReasonCountry},
		{"short number blocked", ch, "123", false, ReasonCountry},
		{"anonymous follows unknown action: block", ch, "anonymous", false, ReasonCountry},
		{"unparseable allowed", chAllow, "ab12", false, ""},
		{"anonymous follows unknown action: allow", chAllow, "anonymous", false, ""},
		{"no restriction, unparseable allowed", open, "ab12", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := (*Blocklist)(nil).Check(c.trunk, c.caller, c.privacy, swiss)
			if (v.Reason != "") != v.Blocked || v.Reason != c.reason {
				t.Fatalf("verdict = %+v, want reason %q", v, c.reason)
			}
		})
	}
	// National numbers are unknown when no country code is configured.
	if v := (*Blocklist)(nil).Check(ch, "0225550000", false, phonebook.Dialing{}); !v.Blocked {
		t.Error("a national number with no HELLO_DIAL_COUNTRY_CODE passed the restriction")
	}
	// An allow entry beats the anonymous reject and the country restriction.
	bl := CompileBlocklist([]BlockEntry{entry(1, "+33123456789", BlockExact, ActionAllow, 0)})
	if v := bl.Check(ch, "+33123456789", false, swiss); v.Blocked {
		t.Errorf("allow entry did not beat the country restriction: %+v", v)
	}
	// A block entry is checked before the country restriction, after anonymous.
	bl = CompileBlocklist([]BlockEntry{entry(2, "+41225550000", BlockExact, ActionBlock, 0)})
	if v := bl.Check(ch, "+41225550000", false, swiss); v.Reason != ReasonBlocklist {
		t.Errorf("reason = %q, want blocklist", v.Reason)
	}
}

func TestNormalizeBlockPattern(t *testing.T) {
	cases := []struct {
		raw, match, want string
		bad              bool
	}{
		{"+41 22 555 00 00", BlockExact, "+41225550000", false},
		{"0041225550000", BlockExact, "+41225550000", false},
		{"022 555 00 00", BlockExact, "+41225550000", false},
		{"123", BlockExact, "", true},
		{"anonymous", BlockExact, "", true},
		{"+44", BlockPrefix, "+44*", false},
		{"+44*", BlockPrefix, "+44*", false},
		{"0044 79", BlockPrefix, "+4479*", false},
		{"079", BlockPrefix, "+4179*", false},
		{"abc", BlockPrefix, "", true},
		{"", BlockPrefix, "", true},
		{"+44", "regex", "", true},
	}
	for _, c := range cases {
		got, why := NormalizeBlockPattern(c.raw, c.match, swiss)
		if (why != "") != c.bad || got != c.want {
			t.Errorf("%q (%s) = %q, %q; want %q bad=%v", c.raw, c.match, got, why, c.want, c.bad)
		}
	}
}

func TestValidateCallerPolicy(t *testing.T) {
	var errs []FieldError
	ValidateCallerPolicy(CallerPolicy{}, "p", &errs)
	if len(errs) != 0 {
		t.Fatalf("zero policy: %v", errs)
	}
	ValidateCallerPolicy(CallerPolicy{RejectCode: 500, UnknownCallerAction: "maybe", AllowedCountryCodes: []string{"041", "4a", "1234"}}, "p", &errs)
	if len(errs) != 5 {
		t.Fatalf("errors = %v, want 5", errs)
	}
}

func BenchmarkBlocklistCheck(b *testing.B) {
	var entries []BlockEntry
	for i := 0; i < 10000; i++ {
		entries = append(entries, entry(int64(i+1), "+4122"+string(rune('0'+i%10))+"0"+string(rune('0'+(i/10)%10))+string(rune('0'+(i/100)%10))+string(rune('0'+(i/1000)%10)), BlockExact, ActionBlock, 0))
	}
	entries = append(entries, entry(99999, "+4479*", BlockPrefix, ActionBlock, 0))
	bl := CompileBlocklist(entries)
	tr := &Trunk{ID: 1, Name: "t", CallerPolicy: CallerPolicy{AllowedCountryCodes: []string{"41"}}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		bl.Check(tr, "+41795551234", false, swiss)
	}
}
