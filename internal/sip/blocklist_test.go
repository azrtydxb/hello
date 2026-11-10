package sip

import (
	"net/netip"
	"testing"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// TestTrunkCallerPolicy fails if an inbound trunk call that anonymous
// reject, the blocklist or the country restriction should stop is rung or
// answered with anything but the trunk's reject code, if a call it should
// let through is stopped, or if a stopped call's CDR does not say why.
func TestTrunkCallerPolicy(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-in", "ip")
	tr.SourceCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	tr.CallerPolicy = routing.CallerPolicy{
		RejectAnonymous: true, AllowedCountryCodes: []string{"41"}, UnknownCallerAction: routing.UnknownBlock, RejectCode: 603,
	}
	cfg := routing.Config{
		Trunks: []routing.Trunk{tr},
		Inbound: []routing.InboundRoute{{ID: 1, Position: 1, Name: "Main DID", DIDKind: "exact", DID: "+97140000100",
			DestinationKind: "extension", Destination: "100", Enabled: true}},
		Extensions: map[string]string{"100": "", "200": ""},
		Blocklist: []routing.BlockEntry{
			{ID: 1, Pattern: "+4179*", Match: routing.BlockPrefix, Action: routing.ActionBlock},
			{ID: 2, Pattern: "+41795550000", Match: routing.BlockExact, Action: routing.ActionAllow},
		},
	}
	table, errs := routing.Compile(cfg)
	if len(errs) > 0 {
		t.Fatalf("configuration does not compile: %v", errs)
	}
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), func(c *Config, d *Deps) {
		c.Dialing = phonebook.Dialing{IntlPrefix: "00", CountryCode: "41"}
		rs := &snapshot.RoutingState{Router: table, Config: cfg, Blocklist: routing.CompileBlocklist(cfg.Blocklist)}
		d.Snapshots.(*fakeSnaps).p.Load().WithRouting(rs)
	})
	callee := newPhone(t, pbx, "a1", "pa")
	callee.register(t)
	from := newPhone(t, pbx, "carrier", "")

	call := func(user string, privacy bool) error {
		inv := carrierInvite(from, pbx, "+97140000100")
		inv.From().Address.User = user
		if privacy {
			inv.AppendHeader(sip.NewHeader("Privacy", "id"))
		}
		dcs, err := from.dua.WriteInvite(t.Context(), inv)
		if err != nil {
			t.Fatal(err)
		}
		if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
			return err
		}
		if err := dcs.Ack(t.Context()); err != nil {
			t.Fatal(err)
		}
		waitReq(t, callee.invites, "INVITE")
		waitReq(t, callee.acks, "ACK")
		hangup(t, dcs)
		return nil
	}
	cases := []struct {
		name, user string
		privacy    bool
		reason     string // "" means the call goes through
	}{
		{"anonymous user", "anonymous", false, "anonymous"},
		{"privacy id", "+41225550000", true, "anonymous"},
		{"blocklist prefix", "+41791234567", false, "blocklist"},
		{"allow beats the prefix", "+41795550000", false, ""},
		{"national spelling of a blocked prefix", "0791234567", false, "blocklist"},
		{"other country", "+33123456789", false, "country"},
		{"unparseable number", "ab12", false, "country"},
		{"allowed", "+41225550000", false, ""},
	}
	for _, c := range cases {
		err := call(c.user, c.privacy)
		cd := pbx.nextCDR(t)
		if c.reason == "" {
			if err != nil || cd.FinalStatus/100 != 2 {
				t.Errorf("%s: err = %v, CDR status %d, want the call to connect", c.name, err, cd.FinalStatus)
			}
			continue
		}
		if code := responseCode(err); code != 603 {
			t.Errorf("%s: response = %v, want 603", c.name, err)
		}
		if cd.FailureReason != "blocked: "+c.reason || cd.FinalStatus != 603 || cd.Direction != "inbound" {
			t.Errorf("%s: CDR = status %d reason %q direction %q", c.name, cd.FinalStatus, cd.FailureReason, cd.Direction)
		}
		select {
		case <-callee.invites:
			t.Errorf("%s: the callee was rung", c.name)
		default:
		}
	}
	for reason, want := range map[string]float64{"anonymous": 2, "blocklist": 2, "country": 2} {
		if got := pbx.metric(t, "hello_blocked_calls_total", map[string]string{"reason": reason}); got != want {
			t.Errorf("hello_blocked_calls_total{reason=%q} = %v, want %v", reason, got, want)
		}
	}
}
