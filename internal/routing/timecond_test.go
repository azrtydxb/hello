package routing

import "testing"

// TestInboundTimeConditionDestination: an inbound route can name a time
// condition; the decision carries it, and a route naming a missing one does
// not compile. It fails if the decision loses the name or an unknown time
// condition compiles.
func TestInboundTimeConditionDestination(t *testing.T) {
	cfg := fixtureConfig()
	cfg.TimeConditions = map[string]bool{"office": true}
	cfg.Inbound = append([]InboundRoute{{ID: 21, Position: 1, Name: "Main line", DIDKind: "exact", DID: "97142220001",
		DestinationKind: "time_condition", Destination: "office", Enabled: true}}, cfg.Inbound...)
	tbl := mustCompile(t, cfg)
	d := tbl.Decide(Call{FromTrunk: cfg.Trunks[0].ID, Number: "+97142220001", CallerID: "+971501112222"}, nil)
	if d.Kind != KindInbound || d.TimeCondition != "office" || d.Extension != "" || d.IVR != "" || d.Route != "Main line" {
		t.Fatalf("decision = %+v", d)
	}
	cfg.TimeConditions = nil
	if _, errs := Compile(cfg); len(errs) == 0 {
		t.Fatal("a route naming a missing time condition compiled")
	}
}
