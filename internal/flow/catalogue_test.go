package flow

import "testing"

// TestCatalogueConsistent fails if a port repeats in a node, an output
// targets a port that does not exist or is not an input, or a target's port
// type cannot receive its source (an edge joins equal port types; a call may
// also enter a voicemail input).
func TestCatalogueConsistent(t *testing.T) {
	c := Get()
	kinds := map[string]bool{}
	for _, n := range c.Nodes {
		if kinds[n.Kind] {
			t.Errorf("node kind %s listed twice", n.Kind)
		}
		kinds[n.Kind] = true
		ports := map[string]bool{}
		for _, p := range n.Ports {
			if ports[p.ID] {
				t.Errorf("%s: port %s listed twice", n.Kind, p.ID)
			}
			ports[p.ID] = true
			if p.Direction == In && len(p.Targets) > 0 {
				t.Errorf("%s.%s: input with targets", n.Kind, p.ID)
			}
		}
	}
	styles := map[PortType]bool{}
	for _, s := range c.PortTypes {
		styles[s.Type] = true
	}
	for _, n := range c.Nodes {
		for _, p := range n.Ports {
			if !styles[p.Type] {
				t.Errorf("%s.%s: port type %s has no style", n.Kind, p.ID, p.Type)
			}
			for _, tg := range p.Targets {
				dst, ok := c.Port(tg.Kind, tg.Port)
				if !ok || dst.Direction != In {
					t.Errorf("%s.%s -> %s.%s: target is not an input", n.Kind, p.ID, tg.Kind, tg.Port)
					continue
				}
				if dst.Type != p.Type && !(p.Type == PortCall && dst.Type == PortVoicemail) {
					t.Errorf("%s.%s (%s) -> %s.%s (%s): port types differ", n.Kind, p.ID, p.Type, tg.Kind, tg.Port, dst.Type)
				}
			}
		}
	}
}

func TestCanConnect(t *testing.T) {
	c := Get()
	cases := []struct {
		sk, sp, dk, dp string
		ok             bool
		requires       string
	}{
		{KindInboundRoute, "destination", KindExtension, "ring", true, ""},
		{KindInboundRoute, "destination", KindAnnouncement, "call", true, ReqDestinations},
		{KindInboundRoute, "destination", KindIVR, "call", true, ReqIVR},
		{KindExtension, "forwardAlways", KindExtension, "voicemail", false, ""},
		{KindRingGroup, "noAnswer", KindExtension, "voicemail", true, ""},
		{KindRingGroup, "noAnswer", KindSIPURI, "call", false, ""},
		{KindExtension, "member", KindRingGroup, "members", true, ""},
		{KindExtension, "member", KindRingGroup, "ring", false, ""},
		{KindRingGroup, "ring", KindExtension, "ring", false, ""}, // an input is not an output
		{KindTrunk, "inbound", KindInboundRoute, "trunk", true, ""},
		{KindOutboundRoute, "trunks", KindTrunk, "carrier", true, ""},
		{KindFeatureCode, "target", KindRingGroup, "ring", false, ""},
		{"nope", "x", KindExtension, "ring", false, ""},
	}
	for _, tc := range cases {
		ok, req, why := c.CanConnect(tc.sk, tc.sp, tc.dk, tc.dp)
		if ok != tc.ok || req != tc.requires {
			t.Errorf("%s.%s -> %s.%s = %v %q (%s), want %v %q", tc.sk, tc.sp, tc.dk, tc.dp, ok, req, why, tc.ok, tc.requires)
		}
		if !ok && why == "" {
			t.Errorf("%s.%s -> %s.%s refused without a reason", tc.sk, tc.sp, tc.dk, tc.dp)
		}
	}
}

// TestGetIsACopy fails if a caller can change the table for the next one.
func TestGetIsACopy(t *testing.T) {
	a := Get()
	a.Nodes[0].Label = "changed"
	if Get().Nodes[0].Label == "changed" {
		t.Fatal("Get shares state")
	}
}
