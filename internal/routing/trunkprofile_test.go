package routing

import "testing"

func TestSBCPeerProblems(t *testing.T) {
	ok := func() []FieldError {
		return SBCPeerProblems("ip", []string{"192.0.2.10/32"}, 1, []string{"G722", "PCMU", "PCMA"}, "off")
	}
	if p := ok(); len(p) != 0 {
		t.Fatalf("a valid SBC peer has problems: %v", p)
	}
	for what, tc := range map[string]struct {
		got  []FieldError
		path string
	}{
		"registration": {SBCPeerProblems("registration", []string{"192.0.2.10/32"}, 1, SBCPeerCodecs, "off"), "mode"},
		"no cidr":      {SBCPeerProblems("ip", nil, 1, SBCPeerCodecs, "off"), "sourceCidrs"},
		"no dest":      {SBCPeerProblems("ip", []string{"192.0.2.10/32"}, 0, SBCPeerCodecs, "off"), "destinations"},
		"opus":         {SBCPeerProblems("ip", []string{"192.0.2.10/32"}, 1, []string{"opus"}, "off"), "codecs"},
		"srtp":         {SBCPeerProblems("ip", []string{"192.0.2.10/32"}, 1, SBCPeerCodecs, "required"), "srtp"},
	} {
		if len(tc.got) != 1 || tc.got[0].Path != tc.path {
			t.Errorf("%s: problems %v, want one on %s", what, tc.got, tc.path)
		}
	}
	for _, p := range []string{"", ProfileGeneric, ProfileSBCPeer} {
		if !ValidProfile(p) {
			t.Errorf("ValidProfile(%q) = false", p)
		}
	}
	if ValidProfile("teams") {
		t.Error(`ValidProfile("teams") = true`)
	}
	for _, c := range SBCPeerCodecs {
		if c == "opus" {
			t.Error("SBC peers are offered Opus")
		}
	}
}
