package ringtag

import (
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/timecond"
)

func table() *Table {
	return NewTable(
		[]Tag{{Name: "vip", BellcoreSlot: 4}, {Name: "after-hours", BellcoreSlot: 2}, {Name: "carrier", BellcoreSlot: 5}},
		[]Rule{
			{Position: 1, Name: "boss", Match: Match{CallerPattern: "1XX"}, Tag: "vip"},
			{Position: 2, Match: Match{Trunk: "carrier-in", DID: "+97140000XXX"}, Tag: "carrier"},
			{Position: 3, Name: "nights", Match: Match{Window: &Window{TimeZone: "UTC", Ranges: []timecond.DayRange{
				{Day: 1, Start: "20:00", End: "23:00"}}}}, Tag: "after-hours"},
		})
}

// TestChooseOrder fails if the order of S-40 is broken: a call's kind beats a
// rule, a rule beats the group's tag, the group's tag beats external, and
// external beats internal.
func TestChooseOrder(t *testing.T) {
	monday9pm := time.Date(2026, 10, 26, 21, 0, 0, 0, time.UTC) // a Monday
	tue9pm := monday9pm.AddDate(0, 0, 1)
	tb := table()
	for _, tc := range []struct {
		name string
		in   Input
		tag  string
		why  string
	}{
		{"internal", Input{CallerNumber: "200", At: tue9pm}, Internal, "default"},
		{"trunk is external", Input{CallerNumber: "+1555", FromTrunk: true, At: tue9pm}, External, "trunk"},
		{"group tag", Input{CallerNumber: "200", GroupTag: "vip", At: tue9pm}, "vip", "group"},
		{"group default tag", Input{CallerNumber: "200", GroupTag: RingGroup, At: tue9pm}, RingGroup, "group"},
		{"group beats trunk", Input{CallerNumber: "+1", FromTrunk: true, GroupTag: RingGroup, At: tue9pm}, RingGroup, "group"},
		{"rule beats group", Input{CallerNumber: "150", GroupTag: RingGroup, At: tue9pm}, "vip", "rule:1 boss"},
		{"rule beats trunk", Input{CallerNumber: "150", FromTrunk: true, At: tue9pm}, "vip", "rule:1 boss"},
		{"first rule wins", Input{CallerNumber: "150", At: monday9pm}, "vip", "rule:1 boss"},
		{"trunk and did", Input{CallerNumber: "+1", FromTrunk: true, Trunk: "carrier-in", DID: "+97140000123", At: tue9pm}, "carrier", "rule:2"},
		{"did mismatch", Input{CallerNumber: "+1", FromTrunk: true, Trunk: "carrier-in", DID: "+97140009999", At: tue9pm}, External, "trunk"},
		{"other trunk", Input{CallerNumber: "+1", FromTrunk: true, Trunk: "other", DID: "+97140000123", At: tue9pm}, External, "trunk"},
		{"time window in", Input{CallerNumber: "200", At: monday9pm}, "after-hours", "rule:3 nights"},
		{"time window out", Input{CallerNumber: "200", At: tue9pm}, Internal, "default"},
		{"kind page beats rule", Input{Kind: KindPage, CallerNumber: "150", At: monday9pm}, Page, "kind:page"},
		{"kind intercom", Input{Kind: KindIntercom, CallerNumber: "200"}, Page, "kind:intercom"},
		{"kind alarm beats group", Input{Kind: KindAlarm, GroupTag: "vip"}, Alarm, "kind:alarm"},
		{"kind door", Input{Kind: KindDoor, FromTrunk: true}, Doorbell, "kind:door"},
		{"emergency callback", Input{Kind: KindEmergencyCallback, CallerNumber: "150"}, EmergencyCallback, "kind:emergency-callback"},
		{"plain call kind", Input{Kind: "call", CallerNumber: "200", At: tue9pm}, Internal, "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tb.Choose(tc.in)
			if got.Tag != tc.tag || got.Why != tc.why {
				t.Fatalf("Choose = %+v, want %s (%s)", got, tc.tag, tc.why)
			}
		})
	}
}

// TestNilTable fails if a missing table cannot choose the default tags.
func TestNilTable(t *testing.T) {
	var tb *Table
	if got := tb.Choose(Input{}); got.Tag != Internal {
		t.Fatalf("nil table chose %+v", got)
	}
	if got := tb.Choose(Input{FromTrunk: true}); got.Tag != External {
		t.Fatalf("nil table chose %+v", got)
	}
	if tb.Slot("doorbell") != 4 || tb.Slot("unknown") != 1 {
		t.Fatalf("nil table slots: doorbell %d, unknown %d", tb.Slot("doorbell"), tb.Slot("unknown"))
	}
}

// TestHeader fails if the canonical Alert-Info is not
// <http://127.0.0.1/Bellcore-drN>;info=hello-<tag> with the tag's slot, or if
// the Snom form is used for any but the three built-in classes.
func TestHeader(t *testing.T) {
	tb := table()
	for _, tc := range []struct{ tag, ua, want string }{
		{Internal, "Yealink SIP-T54W", "<http://127.0.0.1/Bellcore-dr1>;info=hello-internal"},
		{External, "", "<http://127.0.0.1/Bellcore-dr2>;info=hello-external"},
		{RingGroup, "Grandstream WP836", "<http://127.0.0.1/Bellcore-dr3>;info=hello-ringgroup"},
		{Doorbell, "", "<http://127.0.0.1/Bellcore-dr4>;info=hello-doorbell"},
		{Alarm, "", "<http://127.0.0.1/Bellcore-dr5>;info=hello-alarm"},
		{"vip", "", "<http://127.0.0.1/Bellcore-dr4>;info=hello-vip"},
		{Internal, "snom D785", "<http://127.0.0.1/Bellcore-dr1>;info=alert-internal"},
		{External, "snom D785", "<http://127.0.0.1/Bellcore-dr2>;info=alert-external"},
		{RingGroup, "snom D785", "<http://127.0.0.1/Bellcore-dr3>;info=alert-group"},
		{Doorbell, "snom D785", "<http://127.0.0.1/Bellcore-dr4>;info=hello-doorbell"},
	} {
		if got := tb.Header(tc.tag, tc.ua); got != tc.want {
			t.Errorf("Header(%q, %q) = %q, want %q", tc.tag, tc.ua, got, tc.want)
		}
	}
}

// TestValidateMatch fails if a bad pattern, caller kind or window passes.
func TestValidateMatch(t *testing.T) {
	good := Match{CallerPattern: "+9715XXXXXXXX", DID: "^40.*", Trunk: "t", CallerKind: CallerDoor,
		Window: &Window{TimeZone: "Asia/Dubai", Ranges: []timecond.DayRange{{Day: 1, Start: "08:00", End: "17:00"}}}}
	if ps := ValidateMatch("m", good); len(ps) != 0 {
		t.Fatalf("good match refused: %v", ps)
	}
	for name, m := range map[string]Match{
		"bad regex":  {CallerPattern: "("},
		"bad did":    {DID: "[a"},
		"bad kind":   {CallerKind: "robot"},
		"bad zone":   {Window: &Window{TimeZone: "Mars/Base", Ranges: good.Window.Ranges}},
		"no ranges":  {Window: &Window{TimeZone: "UTC"}},
		"bad range":  {Window: &Window{TimeZone: "UTC", Ranges: []timecond.DayRange{{Day: 9, Start: "08:00", End: "17:00"}}}},
		"long trunk": {Trunk: string(make([]byte, 65))},
	} {
		if ps := ValidateMatch("m", m); len(ps) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	if !(Match{}).Empty() || good.Empty() {
		t.Error("Empty is wrong")
	}
}

// TestNames fails if the built-in set is not the seven tags of S-39 or a tag
// name outside 1 to 32 of a-z 0-9 - is valid.
func TestNames(t *testing.T) {
	if n := len(Builtins()); n != 7 {
		t.Fatalf("%d built-in tags, want 7", n)
	}
	for _, ok := range []string{"a", "emergency-callback", "door-2", "0"} {
		if !ValidName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "Upper", "has space", "under_score", "a.b", "0123456789012345678901234567890123"} {
		if ValidName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if !IsBuiltin("alarm") || IsBuiltin("vip") {
		t.Error("IsBuiltin is wrong")
	}
}

// TestBadRulesSkipped fails if a rule naming an unknown tag, or one that does
// not compile, is applied.
func TestBadRulesSkipped(t *testing.T) {
	tb := NewTable(nil, []Rule{
		{Position: 1, Match: Match{CallerPattern: "1XX"}, Tag: "ghost"},
		{Position: 2, Match: Match{CallerPattern: "("}, Tag: Alarm},
	})
	if got := tb.Choose(Input{CallerNumber: "150"}); got.Tag != Internal {
		t.Fatalf("Choose = %+v", got)
	}
}

// BenchmarkChoose guards the 5 microsecond budget of S-40 (spec Constraints).
func BenchmarkChoose(b *testing.B) {
	tb := table()
	in := Input{CallerNumber: "200", FromTrunk: true, Trunk: "x", DID: "+97140000123", At: time.Now()}
	in.At = time.Date(2026, 10, 27, 21, 0, 0, 0, time.UTC)
	for b.Loop() {
		tb.Choose(in)
	}
}
