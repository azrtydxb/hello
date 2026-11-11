package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

type fakeSyslog struct {
	phone   map[int64][]livestate.SyslogLine
	unknown map[string][]livestate.SyslogLine
	down    bool
}

func (f *fakeSyslog) SyslogPhoneLines(_ context.Context, id int64, limit int) ([]livestate.SyslogLine, error) {
	if f.down {
		return nil, errFakeDown
	}
	return f.phone[id][:min(limit, len(f.phone[id]))], nil
}

func (f *fakeSyslog) SyslogUnknownLines(_ context.Context, ip string, limit int) ([]livestate.SyslogLine, error) {
	return f.unknown[ip][:min(limit, len(f.unknown[ip]))], nil
}

func (f *fakeSyslog) SyslogUnknownSources(context.Context) ([]string, error) {
	var out []string
	for ip := range f.unknown {
		out = append(out, ip)
	}
	return out, nil
}

// TestPhoneSyslogAPI fails if a group's syslog setting is not stored and
// validated, or the phone's stored lines and the unknown sources are not
// returned.
func TestPhoneSyslogAPI(t *testing.T) {
	box := testBox(t)
	fs := &fakeSyslog{phone: map[int64][]livestate.SyslogLine{}, unknown: map[string][]livestate.SyslogLine{}}
	e := newEnvConfig(t, Config{Syslog: fs, Prov: ProvConfig{Settings: store.ProvSettings{
		PublicURL: testPublicURL, SIPServer: "192.0.2.10:5060", Domain: testDomain, Expiry: time.Hour,
		Resync: 24 * time.Hour, Timezone: "UTC", NTP: "pool.ntp.org", TokenGrace: 7 * 24 * time.Hour,
	}}}, box)
	c := e.login()
	ext := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	p := (&provEnv{env: e, c: c, box: box})
	e.ext101 = ext
	ph := p.phone("805ec0aabb77", nil)
	id := int64(ph["id"].(float64))

	g := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Lobby", "includeExtensions": true, "includeCompany": true}).json(t)
	gp := fmt.Sprintf("/api/v1/phone-groups/%v/syslog", g["id"])
	if sl, _ := g["syslog"].(map[string]any); sl["enabled"] != false {
		t.Fatalf("a new group has syslog %v, want off", g["syslog"])
	}
	c.must(http.StatusBadRequest, "PUT", gp, map[string]any{"enabled": true, "level": "loud"})
	out := c.must(http.StatusOK, "PUT", gp, map[string]any{"enabled": true, "level": "warning"}).json(t)
	if sl, _ := out["syslog"].(map[string]any); sl["enabled"] != true || sl["level"] != "warning" {
		t.Fatalf("syslog = %v", out["syslog"])
	}
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/999999/syslog", map[string]any{"enabled": true, "level": "info"})

	fs.phone[id] = []livestate.SyslogLine{{At: time.Unix(2, 0).UTC(), Source: "10.0.0.7", Severity: 6, Message: "b"}, {At: time.Unix(1, 0).UTC(), Source: "10.0.0.7", Severity: 6, Message: "a"}}
	r := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/phones/%d/syslog?limit=1", id), nil)
	if !strings.Contains(string(r.body), `"message":"b"`) || strings.Contains(string(r.body), `"message":"a"`) {
		t.Fatalf("lines = %s", r.body)
	}
	c.must(http.StatusBadRequest, "GET", fmt.Sprintf("/api/v1/phones/%d/syslog?limit=0", id), nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/phones/999999/syslog", nil)

	fs.unknown["192.0.2.9"] = []livestate.SyslogLine{{Source: "192.0.2.9", Severity: -1, Message: "who am i"}}
	if r := c.must(http.StatusOK, "GET", "/api/v1/syslog/unknown", nil); !strings.Contains(string(r.body), "192.0.2.9") {
		t.Fatalf("unknown sources = %s", r.body)
	}
	if r := c.must(http.StatusOK, "GET", "/api/v1/syslog/unknown?ip=192.0.2.9", nil); !strings.Contains(string(r.body), "who am i") {
		t.Fatalf("unknown lines = %s", r.body)
	}
	c.must(http.StatusBadRequest, "GET", "/api/v1/syslog/unknown?ip=nope", nil)

	fs.down = true
	c.must(http.StatusServiceUnavailable, "GET", fmt.Sprintf("/api/v1/phones/%d/syslog", id), nil)
}
