package routing

import (
	"strings"
	"testing"
	"time"
)

// FuzzDecide checks that Decide never panics and always returns a
// well-formed decision, whatever the call.
func FuzzDecide(f *testing.F) {
	tbl := mustCompile(f, fixtureConfig())
	f.Add("101", int64(0), "0501234567", "", "", "", int64(1791187200))
	f.Add("", tPrimary, "+971 4 222 3333", "anonymous", "pbx.example.com", "support", int64(1791230400))
	f.Add("alice", int64(0), "3000", "", "", "", int64(0))
	f.Add("", int64(-1), "\x00\xff", "\n", "", "", int64(-62135596800))
	f.Fuzz(func(t *testing.T, ext string, trunk int64, number, cid, domain, header string, unix int64) {
		c := Call{FromExtension: ext, FromTrunk: trunk, Number: number, CallerID: cid, SIPDomain: domain,
			Header: func(string) string { return header }, At: time.Unix(unix, 0)}
		d := tbl.Decide(c, func(id int64, emergency bool) (bool, string) {
			if (id+int64(len(number)))%3 == 0 && !emergency {
				return false, "full"
			}
			return true, ""
		})
		if len(d.Trace) == 0 {
			t.Fatal("empty trace")
		}
		for _, s := range d.Trace {
			if strings.ContainsAny(s.Text, "\n\r") {
				t.Fatalf("trace step has a line break: %q", s.Text)
			}
		}
		switch d.Kind {
		case KindReject:
			if d.RejectCode < 400 || d.Reason == "" || len(d.Candidates) != 0 {
				t.Fatalf("bad reject %+v", outcomeOf(d))
			}
		case KindOutbound:
			if len(d.Candidates) == 0 || !validNumber(d.Number) || len(d.FailoverCodes) == 0 {
				t.Fatalf("bad outbound %+v", outcomeOf(d))
			}
		case KindInternal, KindInbound:
			if d.Extension == "" && d.SIPURI == "" {
				t.Fatalf("no target %+v", outcomeOf(d))
			}
		default:
			t.Fatalf("kind %q", d.Kind)
		}
	})
}

// FuzzApplyTransform checks that ApplyTransform never panics and that a
// success is always a valid number.
func FuzzApplyTransform(f *testing.F) {
	f.Add(1, "+971", "", "", "0501234567")
	f.Add(0, "", `^0(5[0-9]{8})$`, "+971${1}", "0501234567")
	f.Add(0, "", `^(?P<n>.*)$`, "${n}", "x")
	f.Add(-5, "9+", "(", "$", "")
	f.Add(1<<40, "", "a*", "${0}${0}", "aaaa")
	f.Fuzz(func(t *testing.T, strip int, prefix, regex, template, number string) {
		out, err := ApplyTransform(Transform{Strip: strip, Prefix: prefix, Regex: regex, Template: template}, number)
		if err == nil && !validNumber(out) {
			t.Fatalf("success with invalid number %q", out)
		}
		if err != nil && out != "" {
			t.Fatalf("error %v with result %q", err, out)
		}
	})
}
