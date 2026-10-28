package config

import "testing"

// TestLoadDSCP fails if the DSCP defaults are not 26 (SIP) and 46 (RTP), a
// configured value is not read by hello-sip and hello-control alike, or a
// value outside 0 to 63 starts (spec S-21).
func TestLoadDSCP(t *testing.T) {
	s, err := LoadSIP(env(nil))
	if err != nil || s.DSCPSIP != 26 || s.DSCPRTP != 46 {
		t.Fatalf("SIP defaults = %d/%d, %v", s.DSCPSIP, s.DSCPRTP, err)
	}
	c, err := LoadControl(provOn(nil))
	if err != nil || c.Prov.DSCPSIP != 26 || c.Prov.DSCPRTP != 46 {
		t.Fatalf("control defaults = %d/%d, %v", c.Prov.DSCPSIP, c.Prov.DSCPRTP, err)
	}
	set := map[string]string{"HELLO_DSCP_SIP": "24", "HELLO_DSCP_RTP": "0"}
	s, err = LoadSIP(env(set))
	if err != nil || s.DSCPSIP != 24 || s.DSCPRTP != 0 {
		t.Fatalf("SIP set = %d/%d, %v", s.DSCPSIP, s.DSCPRTP, err)
	}
	c, err = LoadControl(provOn(set))
	if err != nil || c.Prov.DSCPSIP != 24 || c.Prov.DSCPRTP != 0 {
		t.Fatalf("control set = %d/%d, %v", c.Prov.DSCPSIP, c.Prov.DSCPRTP, err)
	}
	for _, key := range []string{"HELLO_DSCP_SIP", "HELLO_DSCP_RTP"} {
		for _, bad := range []string{"64", "-1", "ef"} {
			if _, err := LoadSIP(env(map[string]string{key: bad})); err == nil {
				t.Errorf("LoadSIP accepted %s=%s", key, bad)
			}
			wantProvErr(t, provOn(map[string]string{key: bad}), key, bad)
		}
	}
}
