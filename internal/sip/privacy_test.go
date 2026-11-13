package sip

import (
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// anonPBX is a PBX with one carrier trunk (mutated by mut) and extension 100
// calling out through it.
func anonPBX(t *testing.T, mut func(*routing.Trunk), opts ...pbxOpt) (*carrier, *phone) {
	t.Helper()
	cr := newCarrier(t, "acct1", "pw")
	tr := cr.trunk(1, "carrier", "ip")
	if mut != nil {
		mut(&tr)
	}
	r := &fakeRouter{extensions: map[string]bool{"100": true, "200": true}, trunks: []routing.Trunk{tr}}
	r.decide = outboundVia(r, "+971501234567", "+97140000100", false, nil, 1)
	pbx := startPBX(t, callerDevices(), append([]pbxOpt{trunkCfg(newFakeTrunkState()), withRouter(r, nil)}, opts...)...)
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	return cr, a
}

// outInvite places a call to the carrier with extra headers and returns the
// carrier's INVITE.
func outInvite(t *testing.T, cr *carrier, a *phone, hdrs ...sip.Header) *sip.Request {
	t.Helper()
	req := a.inviteReq("0501234567")
	for _, h := range hdrs {
		req.AppendHeader(h)
	}
	ch := make(chan callResult, 1)
	go func() {
		dcs, err := a.callReq(t.Context(), req)
		ch <- callResult{dcs, err}
	}()
	inv := waitReq(t, cr.invites, "INVITE to the carrier")
	r := waitCall(t, ch)
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	hangup(t, r.dcs)
	return inv
}

func wantAnonymous(t *testing.T, inv *sip.Request, identityHeader, identity string) {
	t.Helper()
	from := inv.From()
	if !strings.EqualFold(from.Address.User, "anonymous") || strings.Contains(from.String(), "97140000100") || strings.Contains(from.String(), "100@") {
		t.Fatalf("From reveals the caller: %s", from.String())
	}
	if p := hdr(inv, "Privacy"); !strings.Contains(p, "id") {
		t.Fatalf("Privacy = %q, want id", p)
	}
	if got := hdr(inv, identityHeader); !strings.Contains(got, identity) {
		t.Fatalf("%s = %q, want %s", identityHeader, got, identity)
	}
	if hdr(inv, "P-Asserted-Identity") != "" && identityHeader != "P-Asserted-Identity" {
		t.Fatalf("both identity headers sent")
	}
}

// TestAnonymousOutbound fails if a device's Privacy: id is dropped on a trunk
// leg, if the trunk leg's From reveals the extension, if the trunk's asserted
// identity is not sent, or if an ordinary call is made anonymous.
func TestAnonymousOutbound(t *testing.T) {
	t.Run("ordinary call is not anonymous", func(t *testing.T) {
		cr, a := anonPBX(t, nil)
		inv := outInvite(t, cr, a)
		if inv.From().Address.User != "+97140000100" || hdr(inv, "Privacy") != "" || hdr(inv, "P-Asserted-Identity") != "" {
			t.Fatalf("ordinary call: From %s Privacy %q PAI %q", inv.From().String(), hdr(inv, "Privacy"), hdr(inv, "P-Asserted-Identity"))
		}
	})
	t.Run("device Privacy id is honoured and kept", func(t *testing.T) {
		cr, a := anonPBX(t, nil)
		inv := outInvite(t, cr, a, sip.NewHeader("Privacy", "id"))
		wantAnonymous(t, inv, "P-Asserted-Identity", "<sip:+97140000100@carrier.example>")
	})
	t.Run("device Privacy user and critical are carried with id", func(t *testing.T) {
		cr, a := anonPBX(t, nil)
		inv := outInvite(t, cr, a, sip.NewHeader("Privacy", "user;critical"))
		wantAnonymous(t, inv, "P-Asserted-Identity", "+97140000100")
		if p := hdr(inv, "Privacy"); !strings.Contains(p, "user") || !strings.Contains(p, "critical") {
			t.Fatalf("device privacy values lost: %q", p)
		}
	})
	t.Run("device anonymous From", func(t *testing.T) {
		cr, a := anonPBX(t, nil)
		req := a.inviteReq("0501234567")
		req.From().Address.User, req.From().DisplayName = "anonymous", "Anonymous"
		ch := make(chan callResult, 1)
		go func() { d, err := a.callReq(t.Context(), req); ch <- callResult{d, err} }()
		inv := waitReq(t, cr.invites, "INVITE to the carrier")
		if r := waitCall(t, ch); r.err == nil {
			hangup(t, r.dcs)
		}
		wantAnonymous(t, inv, "P-Asserted-Identity", "+97140000100")
	})
	t.Run("Privacy none is not anonymous", func(t *testing.T) {
		cr, a := anonPBX(t, nil)
		inv := outInvite(t, cr, a, sip.NewHeader("Privacy", "none"))
		if inv.From().Address.User != "+97140000100" || hdr(inv, "Privacy") != "" {
			t.Fatalf("Privacy: none made the call anonymous: %s", inv.From().String())
		}
	})
	t.Run("trunk always, with its own asserted identity in PPI", func(t *testing.T) {
		cr, a := anonPBX(t, func(tr *routing.Trunk) {
			tr.AnonymousPolicy, tr.AssertedIdentity, tr.IdentityHeader = routing.AnonymousAlways, "+97140000999", routing.IdentityPPI
		})
		inv := outInvite(t, cr, a)
		wantAnonymous(t, inv, "P-Preferred-Identity", "<sip:+97140000999@carrier.example>")
	})
	t.Run("extension setting", func(t *testing.T) {
		cr, a := anonPBX(t, nil, withFeatures(func(s *snapshot.Snapshot) {
			s.WithExtensionData([]snapshot.Extension{{Number: "100", AnonymousOutbound: true}})
		}))
		inv := outInvite(t, cr, a)
		wantAnonymous(t, inv, "P-Asserted-Identity", "+97140000100")
	})
	t.Run("a device cannot assert its own identity", func(t *testing.T) {
		cr, a := anonPBX(t, nil)
		inv := outInvite(t, cr, a, sip.NewHeader("Privacy", "id"), sip.NewHeader("P-Asserted-Identity", "<sip:evil@x.example>"))
		if strings.Contains(hdr(inv, "P-Asserted-Identity"), "evil") {
			t.Fatalf("the device's P-Asserted-Identity was forwarded")
		}
	})
}
