package prov

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestZoneInfo fails if a common zone is not derived to each vendor's
// format, with its daylight-saving rule, from the IANA rules.
func TestZoneInfo(t *testing.T) {
	for _, c := range []struct {
		zone string
		want TZInfo
	}{
		{"Europe/Brussels", TZInfo{
			Known: true, POSIX: "CET-1CEST,M3.5.0,M10.5.0/3", OffsetSeconds: 3600, DST: true, SaveMinutes: 60,
			YealinkOffset: "+1", YealinkName: "Belgium(Brussels)", YealinkStart: "3/5/7/2", YealinkEnd: "10/5/7/3",
			FanvilOffset: 4, FanvilName: "UTC+1",
			FanvilStart: Rule{3, 5, 0, 2, 0}, FanvilEnd: Rule{10, 5, 0, 3, 0},
			PolyStart: PolyRule{3, 1, 2, 1, 1}, PolyEnd: PolyRule{10, 1, 3, 1, 1}, SnomCode: "BEL+1",
		}},
		{"America/New_York", TZInfo{
			Known: true, POSIX: "EST5EDT,M3.2.0,M11.1.0", OffsetSeconds: -18000, DST: true, SaveMinutes: 60,
			YealinkOffset: "-5", YealinkName: "United States-Eastern Time", YealinkStart: "3/2/7/2", YealinkEnd: "11/1/7/2",
			FanvilOffset: -20, FanvilName: "UTC-5",
			FanvilStart: Rule{3, 2, 0, 2, 0}, FanvilEnd: Rule{11, 1, 0, 2, 0},
			PolyStart: PolyRule{3, 8, 2, 1, 0}, PolyEnd: PolyRule{11, 1, 2, 1, 0}, SnomCode: "USA-5",
		}},
		{"Asia/Dubai", TZInfo{
			Known: true, POSIX: "<+04>-4", OffsetSeconds: 14400, YealinkOffset: "+4", FanvilOffset: 16, FanvilName: "UTC+4",
		}},
		{"UTC", TZInfo{
			Known: true, POSIX: "UTC0", YealinkOffset: "0", FanvilName: "UTC", SnomCode: "GBR-0",
		}},
	} {
		t.Run(c.zone, func(t *testing.T) {
			if got := ZoneInfo(c.zone, 2026); got != c.want {
				t.Fatalf("ZoneInfo(%s)\n got %+v\nwant %+v", c.zone, got, c.want)
			}
		})
	}
}

// TestZoneInfoOther covers half-hour offsets, the southern hemisphere and
// unknown names.
func TestZoneInfoOther(t *testing.T) {
	for _, c := range []struct{ zone, posix, yealink string }{
		{"Asia/Kolkata", "IST-5:30", "+5:30"},
		{"Australia/Sydney", "AEST-10AEDT,M10.1.0,M4.1.0/3", "+10"},
		{"America/Sao_Paulo", "<-03>3", "-3"},
		{"Europe/London", "GMT0BST,M3.5.0/1,M10.5.0", "0"},
	} {
		got := ZoneInfo(c.zone, 2026)
		if got.POSIX != c.posix || got.YealinkOffset != c.yealink {
			t.Errorf("%s: POSIX %q (want %q), Yealink %q (want %q)", c.zone, got.POSIX, c.posix, got.YealinkOffset, c.yealink)
		}
	}
	for _, name := range []string{"", "Local", "Not/AZone"} {
		if z := ZoneInfo(name, 2026); z.Known || z.POSIX != "" {
			t.Errorf("ZoneInfo(%q) = %+v, want the zero value", name, z)
		}
	}
}

// TestZonesRendered fails if a vendor's file carries the IANA name or lacks
// the zone setting derived for it.
func TestZonesRendered(t *testing.T) {
	d := renderSample(false)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	render := func(v Vendor, file string) string {
		t.Helper()
		d.Phone.Vendor = v
		out, err := Render(context.Background(), builtinFor(t, v), file, d)
		if err != nil {
			t.Fatalf("%s %s: %v", v, file, err)
		}
		return string(out)
	}
	d.Time = NewTimeInfo("Europe/Brussels", "pool.ntp.org", now)
	for _, c := range []struct {
		v     Vendor
		file  string
		wants []string
	}{
		{Yealink, d.Phone.MAC + ".cfg", []string{"local_time.time_zone = +1\n", "local_time.time_zone_name = Belgium(Brussels)\n", "local_time.summer_time = 1\n", "local_time.start_time = 3/5/7/2\n", "local_time.end_time = 10/5/7/3\n", "local_time.offset_time = 60\n"}},
		{Grandstream, "cfg" + d.Phone.MAC + ".xml", []string{"<P64>CET-1CEST,M3.5.0,M10.5.0/3</P64>", "<P246>CET-1CEST,M3.5.0,M10.5.0/3</P246>"}},
		{Poly, d.Phone.MAC + "-hello.cfg", []string{`tcpIpApp.sntp.gmtOffset="3600"`, `tcpIpApp.sntp.daylightSavings.start.month="3"`, `tcpIpApp.sntp.daylightSavings.stop.dayOfWeek.lastInMonth="1"`}},
		{Snom, d.Phone.MACUpper, []string{`<timezone perm="R">BEL+1</timezone>`}},
		{Fanvil, d.Phone.MAC + ".cfg", []string{"Time Zone          :4\n", "Time Zone Name     :UTC+1\n", "DST Start Mon      :3\n", "DST End Hour       :3\n"}},
	} {
		s := render(c.v, c.file)
		if strings.Contains(s, "Europe/Brussels") {
			t.Errorf("%s: the IANA name is in the file:\n%s", c.v, s)
		}
		for _, w := range c.wants {
			if !strings.Contains(s, w) {
				t.Errorf("%s: missing %q in:\n%s", c.v, w, s)
			}
		}
	}
	// A zone without daylight saving turns it off; an unknown one leaves
	// the phone's own zone alone.
	d.Time = NewTimeInfo("Asia/Dubai", "pool.ntp.org", now)
	if s := render(Yealink, d.Phone.MAC+".cfg"); !strings.Contains(s, "local_time.time_zone = +4\nlocal_time.summer_time = 0\n") {
		t.Errorf("Dubai:\n%s", s)
	}
	d.Time = NewTimeInfo("Not/AZone", "pool.ntp.org", now)
	if s := render(Yealink, d.Phone.MAC+".cfg"); strings.Contains(s, "time_zone") {
		t.Errorf("unknown zone left a zone setting:\n%s", s)
	}
}
