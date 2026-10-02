package sip

import (
	"net/netip"
	"testing"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
)

// withEngine compiles cfg with the real routing engine into the PBX's
// snapshot.
func withEngine(t *testing.T, cfg routing.Config) pbxOpt {
	t.Helper()
	table, errs := routing.Compile(cfg)
	if len(errs) > 0 {
		t.Fatalf("test configuration does not compile: %v", errs)
	}
	return func(_ *Config, d *Deps) {
		d.Snapshots.(*fakeSnaps).p.Load().WithRouting(&snapshot.RoutingState{Router: table, Config: cfg})
	}
}

// TestEngineOutboundRewriteAndFailover fails if, with the real routing
// engine, a dialled national number is not rewritten, presented with the
// extension's external number, failed over from a 503 to the backup, and
// traced from the route match through both attempts.
func TestEngineOutboundRewriteAndFailover(t *testing.T) {
	primary, backup := newCarrier(t, "acct1", "pw1"), newCarrier(t, "acct2", "pw2")
	cfg := routing.Config{
		Trunks: []routing.Trunk{primary.trunk(1, "carrier-primary", "ip"), backup.trunk(2, "carrier-backup", "ip")},
		Outbound: []routing.OutboundRoute{{ID: 1, Position: 1, Name: "UAE Mobile", MatchKind: "regex", Match: `^05([0-9]{8})$`,
			Number: routing.Transform{Regex: `^05([0-9]{8})$`, Template: "+9715${1}"}, Trunks: []int64{1, 2}, Enabled: true}},
		Extensions: map[string]string{"100": "+97140000100", "200": ""},
	}
	st := newFakeTrunkState()
	pbx := startPBX(t, callerDevices(), trunkCfg(st), withEngine(t, cfg))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	primary.inviteCode.Store(503)
	r := waitCall(t, dial(t.Context(), a, "0501234567"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	inv := waitReq(t, backup.invites, "INVITE to the backup")
	if inv.Recipient.User != "+971501234567" || inv.From().Address.User != "+97140000100" {
		t.Fatalf("backup INVITE: R-URI %s From %s", inv.Recipient.String(), inv.From().Address.String())
	}
	waitReq(t, backup.acks, "ACK")
	hangup(t, r.dcs)
	cd := pbx.nextCDR(t)
	wantTrace(t, cd.Trace,
		`Internal extension lookup "0501234567" -> no match`,
		"carrier-primary ("+primary.addr+") -> 503 Service Unavailable",
		"Failover permitted for 503",
		"carrier-backup -> 200 OK",
		"Call established")
	if cd.Route != "UAE Mobile" || cd.RewrittenDestination != "+971501234567" || cd.Trunk != "carrier-backup" || cd.Direction != "outbound" {
		t.Fatalf("CDR = %+v", cd)
	}
	for i, s := range cd.Trace {
		if s.N != i+1 {
			t.Fatalf("trace numbering broken at %d: %+v", i, cd.Trace)
		}
	}
}

// TestEngineRejectMapping fails if a decision's reject code is not sent to
// the caller as is with the engine's reason in the CDR.
func TestEngineRejectMapping(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-x", "ip")
	tr.Enabled = false // every candidate unusable: 503 from the engine
	cfg := routing.Config{
		Trunks: []routing.Trunk{tr},
		Outbound: []routing.OutboundRoute{{ID: 1, Position: 1, Name: "Out", MatchKind: "prefix", Match: "0",
			Trunks: []int64{1}, Enabled: true}},
		Extensions: map[string]string{"100": "", "200": ""},
	}
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), withEngine(t, cfg))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	r := waitCall(t, dial(t.Context(), a, "0501234567"))
	if code := responseCode(r.err); code != 503 {
		t.Fatalf("call = %v, want the engine's 503", r.err)
	}
	cd := pbx.nextCDR(t)
	if cd.FinalStatus != 503 || cd.FailureReason == "" || cd.TerminationSide != "system" {
		t.Fatalf("CDR = %+v", cd)
	}
	wantTrace(t, cd.Trace, "Trunk carrier-x skipped: disabled")
	r = waitCall(t, dial(t.Context(), a, "999")) // no route at all
	if code := responseCode(r.err); code != 404 {
		t.Fatalf("unrouted call = %v, want 404", r.err)
	}
	pbx.nextCDR(t)
}

// TestEngineInboundDID fails if, with the real engine, an INVITE from a
// trunk's source CIDR to a DID is not rung on the extension its inbound
// route names.
func TestEngineInboundDID(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-in", "ip")
	tr.SourceCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	cfg := routing.Config{
		Trunks: []routing.Trunk{tr},
		Inbound: []routing.InboundRoute{{ID: 1, Position: 1, Name: "Main DID", DIDKind: "exact", DID: "+97140000100",
			DestinationKind: "extension", Destination: "100", Enabled: true}},
		Extensions: map[string]string{"100": "", "200": ""},
	}
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), withEngine(t, cfg))
	callee := newPhone(t, pbx, "a1", "pa")
	callee.register(t) // REGISTER is digest-authenticated whatever the source
	from := newPhone(t, pbx, "carrier", "")
	inv := carrierInvite(from, pbx, "+97140000100")
	dcs, err := from.dua.WriteInvite(t.Context(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("inbound call: %v", err)
	}
	_ = dcs.Ack(t.Context())
	waitReq(t, callee.invites, "INVITE to extension 100")
	waitReq(t, callee.acks, "ACK")
	hangup(t, dcs)
	cd := pbx.nextCDR(t)
	if cd.Direction != "inbound" || cd.Trunk != "carrier-in" || cd.Route != "Main DID" {
		t.Fatalf("CDR = %+v", cd)
	}
	wantTrace(t, cd.Trace, "Source 127.0.0.1 identifies trunk carrier-in", "Call established")
}
