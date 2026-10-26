package integration

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/test/provclient"
	"github.com/azrtydxb/hello/test/sipua"
)

// TestParkSoftkeyAsWP836 drives the Park softkey of a Grandstream WP836 the
// way the phone does. The WP836 is provisioned from the lab's server; the
// park code it parses (P60069) is what its softkey sends as a blind REFER
// during a call, and the retrieve code (P2336) is what its Retrieve softkey
// dials with the slot. It fails if
//   - the WP836's file lacks the park or retrieve code,
//   - the REFER to the park code is not accepted, does not park the other
//     party in a slot of the live parked-calls list, or does not release the
//     WP836 (BYE),
//   - the other party is dropped by the park,
//   - the retrieve code plus the slot does not connect a third phone to the
//     parked party,
//   - the parked party hanging up does not end the retriever's call.
//
// The registration and the call media run on the runner; the WP836 is a
// scripted phone that applies what it parsed, so its softkeys' behaviour on
// real hardware stays a lab item (needs hardware).
func TestParkSoftkeyAsWP836(t *testing.T) {
	lc := newLabClient(t)
	hc := provHTTP(t)
	pc := provclient.Client{HTTP: hc}
	m := provclient.WP836

	wp := lc.createExtension("desk")[0]
	mac := m.OUI + randHex(3)
	var created labPhoneView
	lc.must("POST", "/api/v1/phones", map[string]any{
		"mac": mac, "vendor": m.Vendor, "model": m.Model, "label": "lab WP836",
		"extensionId": mustInt(t, lc.extensionID(wp.Extension)), "deviceId": wp.ID, "enabled": true,
	}, &created, 201)
	remember(tokenOf(created.ProvisioningURL))
	other := lc.devices("desk")[0]  // the caller, parked
	picker := lc.devices("desk")[0] // retrieves with the Retrieve softkey's code
	lc.waitSnapshots(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	boot, err := pc.Boot(ctx, m, mac, labProvBoot)
	if err != nil {
		t.Fatalf("boot pass %v: %v", boot.Fetches, err)
	}
	prov, err := pc.Provision(ctx, m, mac, boot.Config.ProvURL)
	if err != nil {
		t.Fatalf("per-device pass %v: %v", prov.Fetches, err)
	}
	cfg := prov.Config
	remember(cfg.Password, cfg.AdminPassword)
	if cfg.ParkCode == "" || cfg.ParkRetrieveCode == "" {
		t.Fatalf("the WP836 file carries park code %q and retrieve code %q (P60069, P2336); want both", cfg.ParkCode, cfg.ParkRetrieveCode)
	}

	// The WP836 registers with the credentials it parsed.
	wpPhone, err := sipua.New(sipua.Options{
		User: cfg.User, Password: cfg.Password, Domain: labDomain, Proxy: labSIP1,
		Listen: "0.0.0.0:0", ContactHost: os.Getenv("HELLO_LAB_CONTACT_HOST"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wpPhone.Close)
	callerPhone, pickerPhone := phone(t, other, labSIP1), phone(t, picker, labSIP1)
	for _, p := range []*sipua.Phone{wpPhone, callerPhone, pickerPhone} {
		register(t, p)
	}

	// The caller rings the WP836, which answers; the WP836's user presses
	// Park: a blind REFER to the park code.
	in := answerNext(ctx, wpPhone)
	out, err := callerPhone.Dial(ctx, wp.Extension, sdpOffer)
	if err != nil || out.Status != 200 {
		t.Fatalf("call to the WP836 = %+v, %v", out, err)
	}
	call := <-in
	if call == nil {
		t.Fatal("the WP836 did not receive the call")
	}
	res, err := call.Refer(ctx, "<sip:"+cfg.ParkCode+"@"+labDomain+">")
	if err != nil || res.StatusCode != 202 {
		t.Fatalf("Park softkey REFER = %v, %v; want 202", res, err)
	}
	var slot string
	eventually(t, 15*time.Second, "the call to appear in the parked-calls list", func() error {
		var list struct {
			Items []livestate.ParkedCall `json:"items"`
		}
		if err := lc.do("GET", "/api/v1/parked-calls", nil, &list, 200); err != nil {
			return err
		}
		for _, p := range list.Items {
			if p.ParkedBy == wp.Extension {
				slot = p.Slot
				return nil
			}
		}
		return errors.New("no call is parked by the WP836 yet")
	})
	select {
	case <-call.Ended():
	case <-time.After(10 * time.Second):
		t.Fatal("the WP836 was not released after the park (no BYE)")
	}
	select {
	case <-out.Ended():
		t.Fatal("parking dropped the other party")
	case <-time.After(500 * time.Millisecond):
	}

	// The third phone retrieves with the Retrieve softkey's code and the slot.
	got, err := pickerPhone.Dial(ctx, cfg.ParkRetrieveCode+slot, sdpOffer)
	if err != nil || got.Status != 200 {
		t.Fatalf("retrieve %s%s = %+v, %v; want 200", cfg.ParkRetrieveCode, slot, got, err)
	}
	if _, err := strconv.Atoi(slot); err != nil {
		t.Fatalf("slot %q is not a number", slot)
	}
	// The parked party hangs up: the retriever is released.
	if err := out.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got.Ended():
	case <-time.After(10 * time.Second):
		t.Fatal("the retriever's call did not end when the parked party hung up")
	}
}
