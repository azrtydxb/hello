package routing

import "testing"

// TestInboundIVRDestination: an inbound route can name an IVR menu; the
// decision carries it, and a route naming a missing menu does not compile.
// It fails if the decision loses the menu name or an unknown menu compiles.
func TestInboundIVRDestination(t *testing.T) {
	cfg := fixtureConfig()
	cfg.IVRMenus = map[string]bool{"main": true}
	cfg.Inbound = append([]InboundRoute{{ID: 20, Position: 1, Name: "Front desk", DIDKind: "exact", DID: "97142220000",
		DestinationKind: "ivr", Destination: "main", Enabled: true}}, cfg.Inbound...)
	tbl := mustCompile(t, cfg)
	d := tbl.Decide(Call{FromTrunk: cfg.Trunks[0].ID, Number: "+97142220000", CallerID: "+971501112222"}, nil)
	if d.Kind != KindInbound || d.IVR != "main" || d.Extension != "" || d.Route != "Front desk" {
		t.Fatalf("decision = %+v", d)
	}
	cfg.IVRMenus = nil
	if _, errs := Compile(cfg); len(errs) == 0 {
		t.Fatal("a route naming a missing IVR menu compiled")
	}
}
