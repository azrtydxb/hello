package store

import (
	"context"
	"net/netip"
	"testing"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
)

// TestPhoneSyslog fails if a group's syslog is on by default, is not
// provisioned with its level once on, is provisioned without a deployment
// host, or if a source address is not mapped to its phone, or an address two
// phones share is.
func TestPhoneSyslog(t *testing.T) {
	st, _, ext := provStore(t)
	ctx := context.Background()
	mk := func(mac string) int64 {
		c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: mac, Vendor: prov.Grandstream, Model: "WP826", ExtensionID: ext, Enabled: true, Realm: "hello.test"})
		if err != nil {
			t.Fatal(err)
		}
		return c.Phone.ID
	}
	a, b, c := mk("c074ad000901"), mk("c074ad000902"), mk("c074ad000903")
	g, err := st.CreatePhoneGroup(ctx, "test", GroupInput{Name: "Syslog"})
	if err != nil {
		t.Fatal(err)
	}
	if g.Syslog.Enabled || g.Syslog.Level != "info" {
		t.Fatalf("new group syslog = %+v", g.Syslog)
	}
	for id, ip := range map[int64]string{a: "10.9.0.1", b: "10.9.0.2", c: "10.9.0.2"} {
		if _, err := st.db.ExecContext(ctx, `UPDATE phones SET group_id = $2, last_fetch_ip = $3 WHERE id = $1`, id, g.ID, ip); err != nil {
			t.Fatal(err)
		}
	}
	st.prov.SyslogHost = "syslog.hello.test"
	if info, err := st.syslogInfo(ctx, a); err != nil || info != nil {
		t.Fatalf("off group: %+v, %v", info, err)
	}
	if _, err := st.SetPhoneGroupSyslog(ctx, "test", g.ID, phonebook.Syslog{Enabled: true, Level: "warning"}); err != nil {
		t.Fatal(err)
	}
	if info, err := st.syslogInfo(ctx, a); err != nil || info == nil || info.Host != "syslog.hello.test" || info.Level != 3 {
		t.Fatalf("on group: %+v, %v", info, err)
	}
	st.prov.SyslogHost = ""
	if info, _ := st.syslogInfo(ctx, a); info != nil {
		t.Fatalf("no deployment host but %+v", info)
	}
	ips, err := st.PhoneIPs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ips[netip.MustParseAddr("10.9.0.1")] != a {
		t.Errorf("phone a not mapped: %v", ips)
	}
	if _, shared := ips[netip.MustParseAddr("10.9.0.2")]; shared {
		t.Errorf("an address two phones share is attributed: %v", ips)
	}
	if _, err := st.SetPhoneGroupSyslog(ctx, "test", 999999, phonebook.Syslog{Level: "info"}); err == nil {
		t.Error("unknown group accepted")
	}
}
