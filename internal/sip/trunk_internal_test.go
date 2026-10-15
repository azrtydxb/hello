// Trunk-to-extension calls (trunk plan Task 3): the caller-ID ladder (spec
// S-4) presented on the phone's INVITE, and the CDR fields of spec S-5.
package sip

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// internalPeer builds the routing config the ladder and CDR tests run on: an
// ip trunk whose policy allows every internal number, one "internal" inbound
// route, and extension 1012. cid is the trunk's default caller ID.
func internalPeer(t *testing.T, cid string) routing.Config {
	t.Helper()
	return routing.Config{
		Trunks: []routing.Trunk{{
			ID:              7,
			Name:            "peer",
			Mode:            "ip",
			Enabled:         true,
			SourceCIDRs:     []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
			Destinations:    []routing.Destination{{Host: "10.0.0.5", Weight: 1}},
			DefaultCallerID: cid,
			InternalDialing: routing.Dialing{Mode: "all"},
		}},
		Inbound: []routing.InboundRoute{{
			ID:              1,
			Position:        10,
			Name:            "Any extension",
			DIDKind:         "any",
			DestinationKind: "internal",
			Enabled:         true,
		}},
		Extensions: map[string]string{"1012": ""},
	}
}

// trunkInvite is an INVITE as a trunk peer sends it: no credentials, From in
// the peer's domain, to did at the node. user and name are the From user and
// display name.
func trunkInvite(from *phone, pbx *testPBX, did, user, name string) *sip.Request {
	host, port, _ := sip.ParseAddr(pbx.addr)
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: did, Host: host, Port: port})
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: user, Host: "peer.example"},
		DisplayName: name, Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}}})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: did, Host: host}})
	req.AppendHeader(sip.HeaderClone(&from.contact))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody([]byte(from.sdp))
	req.SetDestination(pbx.addr)
	return req
}

// dialInternal places a trunk INVITE for did from user, walks the call to an
// answer (the callee answers at once) and returns the INVITE the phone was
// forked — its From is what the phone is shown — with bye hanging the call
// up and waiting for the phone to see it.
func dialInternal(t *testing.T, pbx *testPBX, caller, callee *phone, did, user, name string) (in *sip.Request, bye func()) {
	t.Helper()
	dcs, err := caller.dua.WriteInvite(t.Context(), trunkInvite(caller, pbx, did, user, name))
	if err != nil {
		t.Fatalf("trunk INVITE: %v", err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("INVITE from the trunk: %v", err)
	}
	if err := dcs.Ack(t.Context()); err != nil {
		t.Fatalf("ACK: %v", err)
	}
	in = waitReq(t, callee.invites, "INVITE to the extension")
	return in, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := dcs.Bye(ctx); err == nil {
			<-callee.byes
		}
	}
}

// TestInternalCallerIDLadder fails if a phone sees, for a trunk call with a
// number, without a number, and without either default, something other than
// the received caller ID, the trunk's default caller ID, and the bracketed
// trunk name respectively, or if the route transform is not applied to
// whatever the ladder produced (spec S-4).
func TestInternalCallerIDLadder(t *testing.T) {
	cases := []struct {
		name      string
		defCID    string // the trunk's default caller ID
		transform *routing.Transform
		fromUser  string
		fromName  string
		wantUser  string // the From user the phone sees ("" for none)
		wantName  string // the From display name the phone sees
		trace     string // the ladder's trace step
	}{
		{
			name:     "the received caller ID, name included",
			fromUser: "+971501112222", fromName: "ExPeer",
			wantUser: "+971501112222", wantName: "ExPeer",
			trace: "Caller ID +971501112222 as received",
		},
		{
			name:     "the trunk's default caller ID when none is received",
			defCID:   "+97140000001",
			fromUser: "", fromName: "ExPeer",
			wantUser: "+97140000001", wantName: "ExPeer",
			trace: "Caller ID +97140000001 (default caller ID of trunk peer)",
		},
		{
			name:     "the bracketed trunk name when there is no default either",
			fromUser: "", fromName: "",
			wantUser: "", wantName: "<peer>",
			trace: "Caller ID <peer> (trunk name)",
		},
		{
			name:      "the route transform applies to the ladder's choice",
			transform: &routing.Transform{Regex: `^\+971(.*)$`, Template: "0${1}"},
			fromUser:  "+971501112222", fromName: "ExPeer",
			wantUser: "0501112222", wantName: "ExPeer",
			trace: "Caller ID rewrite +971501112222 -> 0501112222",
		},
		{
			name:      "the route transform applies to the trunk's default too",
			defCID:    "+97140000001",
			transform: &routing.Transform{Regex: `^\+971(.*)$`, Template: "0${1}"},
			fromUser:  "", fromName: "ExPeer",
			wantUser: "040000001", wantName: "ExPeer",
			trace: "Caller ID rewrite +97140000001 -> 040000001",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := internalPeer(t, tc.defCID)
			if tc.transform != nil {
				cfg.Inbound[0].CallerID = *tc.transform
			}
			st := newFakeTrunkState()
			pbx := startPBX(t, []snapshot.Device{dev(1, "a1", "1012", "pa")}, trunkCfg(st), withEngine(t, cfg))
			callee := newPhone(t, pbx, "a1", "pa")
			callee.register(t)
			caller := newPhone(t, pbx, "carrier", "")
			in, bye := dialInternal(t, pbx, caller, callee, "1012", tc.fromUser, tc.fromName)
			if in.From().Address.User != tc.wantUser || in.From().DisplayName != tc.wantName {
				t.Fatalf("phone sees From %q %q, want user %q name %q", in.From().Address.User, in.From().DisplayName, tc.wantUser, tc.wantName)
			}
			waitReq(t, callee.acks, "ACK to the extension")
			bye()
			cd := pbx.nextCDR(t)
			if !strings.Contains(traceText(cd.Trace), tc.trace+"\n") {
				t.Fatalf("trace lacks %q:\n%s", tc.trace, traceText(cd.Trace))
			}
		})
	}
}

// TestTrunkInternalCDR fails if a trunk-to-internal call's CDR lacks
// direction inbound, the resolved extension as rewritten destination, the
// route name, or the policy verdict and caller-ID steps in the trace (spec
// S-5). A leading + on the DID never reaches the CDR: the record names the
// extension the call resolved to.
func TestTrunkInternalCDR(t *testing.T) {
	st := newFakeTrunkState()
	pbx := startPBX(t, []snapshot.Device{dev(1, "a1", "1012", "pa")}, trunkCfg(st), withEngine(t, internalPeer(t, "")))
	callee := newPhone(t, pbx, "a1", "pa")
	callee.register(t)
	caller := newPhone(t, pbx, "carrier", "")

	_, bye := dialInternal(t, pbx, caller, callee, "1012", "+971555000111", "")
	bye()
	cd := pbx.nextCDR(t)
	if cd.Direction != "inbound" || cd.OriginalDestination != "1012" || cd.RewrittenDestination != "1012" ||
		cd.Route != "Any extension" || cd.Trunk != "peer" || cd.Source != "+971555000111" || cd.FinalStatus != 200 {
		t.Fatalf("CDR = %+v", cd)
	}
	wantTrace(t, cd.Trace,
		"Trunk peer may dial 1012: all internal numbers",
		"Caller ID +971555000111 as received",
		"Destination: internal",
		"Internal lookup 1012 -> extension 1012",
	)

	// A leading + on the DID is resolved away: the CDR records the
	// extension, and with nothing received the ladder names the trunk.
	_, bye = dialInternal(t, pbx, caller, callee, "+1012", "", "")
	bye()
	cd = pbx.nextCDR(t)
	if cd.Direction != "inbound" || cd.OriginalDestination != "+1012" || cd.RewrittenDestination != "1012" ||
		cd.Source != "<peer>" || cd.FinalStatus != 200 {
		t.Fatalf("CDR for +1012 = %+v", cd)
	}
	wantTrace(t, cd.Trace,
		"Trunk peer may dial +1012: all internal numbers",
		"Caller ID <peer> (trunk name)",
		"Internal lookup +1012 -> extension 1012",
	)
}
