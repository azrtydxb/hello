package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/rtcpxr"
	"github.com/azrtydxb/hello/test/provclient"
	"github.com/azrtydxb/hello/test/sipua"
	"github.com/emiago/sipgo/sip"
)

// labWPProv is the WP sweep's provisioning listener (compose.yaml
// hello-control-wp): the same code and database as the lab's other control
// replicas, with the server-wide transport settings that only environment
// variables carry turned on for the WP models.
const labWPProv = "https://localhost:8444"

// wpModels are the two WP families the sweep runs. Transport is what
// hello-control-wp provisions the model with: the WP822 registers over TCP,
// the WP836 over TLS (SRTP is a TLS-only setting), so between them every
// transport key of the parity work is rendered and read back.
var wpModels = []struct {
	name      string
	transport string
	server    string // the registrar the file names (P47)
}{
	{"WP822", "tcp", labKamailio},
	{"WP836", "tls", "127.0.0.1:5081"},
}

// labHardware is the spec's lab verification list (phone-feature-parity.md,
// "Lab verification list"): each item is "confirmed" when a subtest of this
// run proved Hello's side of it with the provclient/sipua emulation, and
// "needs hardware" when it is about what the handset itself does. The result
// is printed at the end of the run.
var labHardware = []struct {
	n      int
	title  string
	proof  string // the subtest that confirms it; "" means it needs hardware
	reason string
}{
	{1, "message-summary SUBSCRIBE and NOTIFY (the LED itself)", "mwi", "the LED lights on the handset"},
	{2, "check-sync event, digest authentication, reboot form", "check-sync", "P95030 behaviour on the handset"},
	{3, "BLF keys P323 to P325 and speed dial P23001/P23003", "", "key behaviour on the handset"},
	{4, "codec order keys P57 to P64, a G.722 call through the anchor", "", "codec negotiation by the handset"},
	{5, "Park softkey REFER target P60069 and P2336", "park", "the softkey on the handset"},
	{6, "feature sync P2325, DND both ways", "dnd", "P8446 local DND on the WP822"},
	{7, "MESSAGE to and from the handset", "message", "the WP8x6 IM app with P22650"},
	{8, "mid-call roam, 1 h idle behind NAT, OPTIONS keep-alive", "", "roaming and NAT on a real network"},
	{9, "Alert-Info ring tag on the INVITE", "ring-tag", "P1488 rules and Bellcore on the handset"},
	{10, "TLS P130 = 2, SRTP P183, TCP P130 = 1", "transport", "DTLS P60078 and ICE P95002 on 1.0.3.x"},
	{11, "account 2 keys", "lines", "account sharing P22521/P22522 on the handset"},
	{12, "hardening, TR-069 off, P84988", "", "the handset's reaction to a proxy-only setup"},
	{13, "E911 HELD request to Hello", "held", "P8565 and the side key P82317 on the handset"},
	{14, "PTT channels, multicast paging", "", "multicast audio on a real network"},
	{15, "Wi-Fi P83000 to P83003 with a verified profile", "wifi", "the handset joining the network"},
	{16, "door station registering, ring tag and CDR kind", "door", "`*50` and video preview on a real station"},
}

var (
	wpProven   = map[string]bool{}
	wpProvenMu sync.Mutex
)

func wpProve(name string) {
	wpProvenMu.Lock()
	defer wpProvenMu.Unlock()
	wpProven[name] = true
}

// wpEnv is what one model's run shares between its subtests.
type wpEnv struct {
	lc       *labClient
	m        provclient.Model
	transp   string
	server   string
	wp       labDevice // the WP's first line
	line2    labDevice
	other    labDevice // the party that calls, messages and is called
	third    labDevice // rings while the WP picks the call up
	door     labDevice
	phoneID  int64
	token    string
	cfg      provclient.Config
	wpPhone  *sipua.Phone
	wpExtID  int64
	wifiSSID string
	wifiPSK  string
	wifiUser string
	wifiPass string
	city     string
	street   string
	pickup   string // the directed pickup code
	park     string
	retrieve string
	// lastCallID and lastCDR are the callee-leg Call-ID and CDR of the ring-tag
	// call, which the RTCP-XR report names.
	lastCallID string
	lastCDR    int64
}

// TestWPPhonesAsVendors (phone-feature-parity S-38) runs the Grandstream WP822
// and WP836 as the PBX's vendors: each model is provisioned through the lab's
// WP listener with the transport, SRTP, backup server, RTCP-XR, presence,
// lines, Wi-Fi (with 802.1X) and location settings the parity work added, the
// file is read back with the provclient P-value parser, and the model then
// registers with the credentials it parsed and does what the handsets do: it
// subscribes to message-summary, reg and presence, publishes presence and an
// RTCP-XR report, answers a check-sync after a digest challenge, syncs its DND
// key, sends and receives MESSAGE, rings with a reliable provisional response
// (PRACK) and a ring tag, parks a call, picks a call up, takes a door call and
// asks for its location over HELD. It fails if the file lacks or mis-writes a
// key, any of these does not complete, or the CDR of a call lacks its ring tag,
// door kind or the phone's quality report. The handsets' own behaviour stays
// on the hardware list printed at the end ("confirmed" or "needs hardware").
func TestWPPhonesAsVendors(t *testing.T) {
	lc := newLabClient(t)
	hc := provHTTP(t)
	pc := provclient.Client{HTTP: hc}

	for _, wm := range wpModels {
		t.Run(wm.name, func(t *testing.T) {
			lc := &labClient{t: t, c: lc.c}
			var m provclient.Model
			for _, c := range provclient.Models {
				if c.Model == wm.name {
					m = c
				}
			}
			if m.Model == "" {
				t.Fatalf("no provclient profile for %s", wm.name)
			}
			e := &wpEnv{lc: lc, m: m, transp: wm.transport, server: wm.server}
			e.setup(t, hc)
			e.provision(t, pc)
			e.register(t)
			t.Run("transport", e.checkTransport)
			e.relaxSRTP(t)
			t.Run("lines", e.checkLines)
			t.Run("wifi", e.checkWifi)
			t.Run("held", e.checkHELD)
			t.Run("mwi", e.checkMWI)
			t.Run("check-sync", e.checkCheckSync)
			t.Run("dnd", e.checkDND)
			t.Run("presence", e.checkPresence)
			t.Run("message", e.checkMessage)
			t.Run("ring-tag", e.checkRingTagAndPRACK)
			t.Run("park", e.checkPark)
			t.Run("pickup", e.checkPickup)
			t.Run("door", e.checkDoor)
			t.Run("rtcpxr", e.checkRTCPXR)
		})
	}

	t.Run("lab-list", func(t *testing.T) {
		wpProvenMu.Lock()
		defer wpProvenMu.Unlock()
		for _, it := range labHardware {
			status, why := "needs hardware", it.reason
			if it.proof != "" && wpProven[it.proof] {
				status, why = "confirmed", "emulated; hardware still to see: "+it.reason
			}
			t.Logf("lab item %2d %-14s %s (%s)", it.n, status, it.title, why)
			if it.proof != "" && !wpProven[it.proof] {
				t.Errorf("lab item %d (%s) was not confirmed: the %q subtest did not pass", it.n, it.title, it.proof)
			}
		}
	})
}

// newDesk creates an extension with one device and remembers its secret.
func (e *wpEnv) newDesk() (labDevice, int64) {
	d := e.lc.createExtension("desk")[0]
	remember(d.Secret)
	return d, mustInt(e.lc.t, e.lc.extensionID(d.Extension))
}

// setup creates the extensions, the WP phone and everything it is provisioned
// with, and waits until the SIP nodes hold the devices.
func (e *wpEnv) setup(t *testing.T, hc *http.Client) {
	lc := e.lc
	var wpExt, line2Ext, otherExt, thirdExt int64
	e.wp, wpExt = e.newDesk()
	e.line2, line2Ext = e.newDesk()
	e.other, otherExt = e.newDesk()
	e.third, thirdExt = e.newDesk()
	e.wpExtID = wpExt
	_ = otherExt

	// A door station that may call the WP.
	number := "7" + randDigits(7)
	var door struct{ ID int64 }
	lc.must("POST", "/api/v1/extensions", map[string]any{
		"number": number, "name": "door " + number, "kind": "door",
		"doorTargets": map[string]any{"extensions": []int64{wpExt}, "ringGroups": []int64{}},
	}, &door, 201)
	var dd struct {
		ID     int64
		Secret string
	}
	lc.must("POST", "/api/v1/devices", map[string]any{"extensionId": door.ID, "sipUsername": "door-" + number}, &dd, 201)
	remember(dd.Secret)
	e.door = labDevice{ID: dd.ID, Extension: number, User: "door-" + number, Secret: dd.Secret}

	// The phone, on the first line.
	mac := e.m.OUI + randHex(3)
	var created labPhoneView
	lc.must("POST", "/api/v1/phones", map[string]any{
		"mac": mac, "vendor": e.m.Vendor, "model": e.m.Model, "label": "lab " + e.m.Model,
		"extensionId": wpExt, "deviceId": e.wp.ID, "enabled": true,
	}, &created, 201)
	e.phoneID = created.ID
	e.token = tokenOf(created.ProvisioningURL)
	remember(e.token)

	// Line 2.
	lc.must("PUT", fmt.Sprintf("/api/v1/phones/%d/lines/2", e.phoneID), map[string]any{
		"extensionId": line2Ext, "deviceId": e.line2.ID,
	}, nil, 200)

	// SRTP: a TLS phone's policy is the device's.
	if e.transp == "tls" {
		lc.must("PATCH", fmt.Sprintf("/api/v1/devices/%d", e.wp.ID), map[string]string{"srtp": "required"}, nil, 200)
	}

	// Wi-Fi: a PSK network for the WP822, an 802.1X (PEAP) network for the
	// WP836, whose family takes enterprise profiles.
	suffix := randHex(3)
	e.wifiSSID = "lab-" + suffix
	prof := map[string]any{"name": "lab " + suffix, "ssid": e.wifiSSID, "verified": true}
	if e.transp == "tls" {
		e.wifiUser, e.wifiPass = "wp-"+suffix, "Pw-"+randHex(6)
		var d1x struct {
			ID int64 `json:"id"`
		}
		lc.must("POST", "/api/v1/dot1x-profiles", map[string]any{
			"name": "lab " + suffix, "method": "peap", "identity": e.wifiUser, "password": e.wifiPass,
		}, &d1x, 201)
		prof["security"], prof["dot1xProfileId"] = "wpa2-enterprise", d1x.ID
		remember(e.wifiPass)
	} else {
		e.wifiPSK = "psk-" + randHex(6)
		prof["security"], prof["psk"] = "wpa2-psk", e.wifiPSK
		remember(e.wifiPSK)
	}
	var wp struct {
		ID int64 `json:"id"`
	}
	lc.must("POST", "/api/v1/wifi-profiles", prof, &wp, 201)
	lc.must("PUT", fmt.Sprintf("/api/v1/phones/%d/wifi", e.phoneID), map[string]any{"profileIds": []int64{wp.ID}}, nil, 200)

	// A location for the HELD request.
	e.city, e.street = "Amsterdam", "Damrak"
	var loc struct {
		ID int64 `json:"id"`
	}
	lc.must("POST", "/api/v1/locations", map[string]any{
		"kind": "site", "name": "lab site " + suffix, "country": "NL", "city": e.city,
		"street": e.street, "houseNumber": "1", "postalCode": "1012LG",
	}, &loc, 201)
	lc.must("PUT", fmt.Sprintf("/api/v1/location-assignments/phone/%d", e.phoneID), map[string]any{"locationId": loc.ID}, nil, 204)

	// Directed pickup is scoped to a pickup group.
	lc.must("POST", "/api/v1/pickup-groups", map[string]any{
		"name": "wp-" + suffix, "members": []int64{wpExt, thirdExt},
	}, nil, 201)

	// The feature codes the WP dials: park, retrieve and directed pickup.
	var codes struct {
		Items []struct {
			Code   string `json:"code"`
			Action string `json:"action"`
		} `json:"items"`
	}
	lc.must("GET", "/api/v1/feature-codes", nil, &codes, 200)
	for _, c := range codes.Items {
		switch c.Action {
		case "park":
			e.park = c.Code
		case "park_retrieve":
			e.retrieve = c.Code
		case "pickup_directed":
			e.pickup = c.Code
		}
	}
	if e.park == "" || e.retrieve == "" || e.pickup == "" {
		t.Fatalf("feature codes park %q, retrieve %q, directed pickup %q; want all three", e.park, e.retrieve, e.pickup)
	}
	lc.waitSnapshots(10 * time.Second)
}

// provision fetches the WP's files from the WP listener, the way the model
// does, and parses them.
func (e *wpEnv) provision(t *testing.T, pc provclient.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mac := strings.ToLower(e.macOfPhone(t))
	url := labWPProv + "/p/" + e.token + "/"
	res, err := pc.Provision(ctx, e.m, mac, url)
	if err != nil {
		t.Fatalf("per-device pass %v: %v", res.Fetches, err)
	}
	e.cfg = res.Config
	remember(e.cfg.Password, e.cfg.AdminPassword)
	if e.cfg.User != e.wp.User || e.cfg.Password == "" {
		t.Fatalf("parsed user %q (want %q), password set %v", e.cfg.User, e.wp.User, e.cfg.Password != "")
	}
	// The WP822's name sequence ends provisioned: the dev-MAC file is
	// served, the model and common files may be 404.
	if got := res.Fetches[len(res.Fetches)-1]; got.Name != "cfg.xml" {
		t.Errorf("sequence %v does not end with cfg.xml", res.Fetches)
	}
}

// macOfPhone reads the phone's MAC from the API.
func (e *wpEnv) macOfPhone(t *testing.T) string {
	var v struct {
		MAC string `json:"mac"`
	}
	e.lc.must("GET", "/api/v1/phones/"+strconv.FormatInt(e.phoneID, 10), nil, &v, 200)
	if v.MAC == "" {
		t.Fatal("the phone has no MAC")
	}
	return strings.ReplaceAll(strings.ReplaceAll(v.MAC, ":", ""), "-", "")
}

// register brings the WP up on the credentials it parsed, and the other
// phones the scenarios need.
func (e *wpEnv) register(t *testing.T) {
	p, err := sipua.New(sipua.Options{
		User: e.cfg.User, Password: e.cfg.Password, Domain: labDomain, Proxy: labSIP1,
		Listen: "0.0.0.0:0", ContactHost: os.Getenv("HELLO_LAB_CONTACT_HOST"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	e.wpPhone = p
	register(t, p)
}

// live starts a phone for d on the lab's first SIP node and registers it; it
// unregisters when the subtest ends, so the next one rings only live phones.
func (e *wpEnv) live(t *testing.T, d labDevice) *sipua.Phone {
	p := phone(t, d, labSIP1)
	register(t, p)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = p.UnregisterAll(ctx)
	})
	return p
}

func (e *wpEnv) want(t *testing.T, name, want string) {
	t.Helper()
	got, ok := e.cfg.Value(name)
	if !ok || got != want {
		t.Errorf("%s %s = %q (set %v), want %q", e.m.Model, name, got, ok, want)
	}
}

func (e *wpEnv) absent(t *testing.T, name string) {
	t.Helper()
	if got, ok := e.cfg.Value(name); ok {
		t.Errorf("%s %s = %q, want it left out of the file", e.m.Model, name, got)
	}
}

// checkTransport covers SRTP, TCP and TLS, the backup server, the RTCP-XR
// collector and the presence keys the template keeps off.
func (e *wpEnv) checkTransport(t *testing.T) {
	host, port, _ := strings.Cut(e.server, ":")
	e.want(t, "P47", e.server)
	switch e.transp {
	case "tcp":
		e.want(t, "P130", "1")
		e.absent(t, "P183") // SRTP is a TLS-only setting
		// The backup registrar: P2312 and P2333 name it, P48 repeats the primary.
		e.want(t, "P2312", "127.0.0.1:5082")
		e.want(t, "P2333", "127.0.0.1:5082")
		e.want(t, "P48", e.server)
	case "tls":
		e.want(t, "P130", "2")
		e.want(t, "P183", "2") // the device's "required"
		e.absent(t, "P2312")   // a TLS phone has no UDP backup
	}
	// RTCP-XR (S-36): the collector is the SIP server itself.
	e.want(t, "P26093", host)
	e.want(t, "P26095", port)
	e.want(t, "P60087", "1")
	e.want(t, "P8489", "1")
	// Presence (S-35): the WP templates keep PUBLISH and the extra SUBSCRIBE off.
	e.want(t, "P188", "0")
	e.want(t, "P2319", "0")
	// A WP takes no wired 802.1X keys (those are the GRP26xx's).
	e.absent(t, "P7901")
	if !t.Failed() {
		wpProve("transport")
	}
}

// relaxSRTP drops the WP836's device policy from required to optional once the
// file has been checked: the sweep's calls offer plain RTP, and the SRTP
// call path has its own lab and unit tests.
func (e *wpEnv) relaxSRTP(t *testing.T) {
	if e.transp != "tls" {
		return
	}
	e.lc.must("PATCH", fmt.Sprintf("/api/v1/devices/%d", e.wp.ID), map[string]string{"srtp": "optional"}, nil, 200)
	e.lc.waitSnapshots(10 * time.Second)
}

// checkLines covers the second account.
func (e *wpEnv) checkLines(t *testing.T) {
	// Line 1 is the file's own account block (P35 and the keys beside it,
	// checked by provision); the account blocks of lines 2 and up follow.
	e.want(t, "P401", "1")
	e.want(t, "P404", e.line2.User)
	if e.transp == "tls" {
		e.want(t, "P448", "2")
	} else {
		e.want(t, "P448", "1")
	}
	if !t.Failed() {
		wpProve("lines")
	}
}

// checkWifi covers the Wi-Fi profile, and the 802.1X (PEAP) one on the WP836.
func (e *wpEnv) checkWifi(t *testing.T) {
	e.want(t, "P83000", e.wifiSSID)
	if e.transp == "tls" {
		e.want(t, "P83002", "5") // WPA2 enterprise
		e.want(t, "P83004", "2") // EAP-PEAP
		e.want(t, "P83014", e.wifiUser)
		e.want(t, "P83015", e.wifiPass)
	} else {
		e.want(t, "P83001", "1")
		e.want(t, "P83002", "4") // WPA2 PSK
		e.want(t, "P83003", e.wifiPSK)
	}
	if !t.Failed() {
		wpProve("wifi")
	}
}

// checkHELD asks for the phone's location the way its E911 client does.
func (e *wpEnv) checkHELD(t *testing.T) {
	const req = `<?xml version="1.0"?><locationRequest xmlns="urn:ietf:params:xml:ns:geopriv:held"><locationType exact="false">civic geodetic</locationType></locationRequest>`
	r, err := http.NewRequestWithContext(context.Background(), http.MethodPost, labWPProv+"/p/"+e.token+"/held", strings.NewReader(req))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/held+xml")
	r.Header.Set("User-Agent", e.m.UserAgent(strings.ToLower(e.macOfPhone(t))))
	res, err := provHTTP(t).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode != 200 || !strings.Contains(string(body), e.city) || !strings.Contains(string(body), e.street) {
		t.Fatalf("HELD = %d, body %q; want 200 naming %s and %s", res.StatusCode, body, e.city, e.street)
	}
	wpProve("held")
}

func (e *wpEnv) ctx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// waitNotify returns the first NOTIFY of event from ch whose body holds want.
func waitNotify(t *testing.T, ch <-chan *sip.Request, event, want string) *sip.Request {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case r := <-ch:
			if h := r.GetHeader("Event"); h != nil && strings.HasPrefix(strings.ToLower(h.Value()), event) && strings.Contains(string(r.Body()), want) {
				return r
			}
		case <-deadline:
			t.Fatalf("no %s NOTIFY holding %q within 20s", event, want)
		}
	}
}

func drainNotifies(ch <-chan *sip.Request) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func (e *wpEnv) checkMWI(t *testing.T) {
	res, err := e.wpPhone.Subscribe(e.ctx(t), e.wp.Extension, "message-summary", 120,
		[][2]string{{"Accept", "application/simple-message-summary"}}, nil)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("message-summary SUBSCRIBE = %v, %v; want 200", res, err)
	}
	n := waitNotify(t, e.wpPhone.Notifies(), "message-summary", "Messages-Waiting")
	if ct := n.GetHeader("Content-Type"); ct == nil || ct.Value() != "application/simple-message-summary" {
		t.Fatalf("NOTIFY Content-Type = %v", ct)
	}
	wpProve("mwi")
}

func (e *wpEnv) checkCheckSync(t *testing.T) {
	drainNotifies(e.wpPhone.Notifies())
	e.lc.must("POST", fmt.Sprintf("/api/v1/phones/%d/resync", e.phoneID), nil, nil, 202)
	n := waitNotify(t, e.wpPhone.Notifies(), "check-sync", "")
	if h := n.GetHeader("Authorization"); h == nil {
		t.Fatal("the check-sync that reached the phone carried no credentials: the digest challenge was not answered")
	}
	wpProve("check-sync")
}

func (e *wpEnv) checkDND(t *testing.T) {
	ctx := e.ctx(t)
	feat := "application/x-as-feature-event+xml"
	body := `<?xml version="1.0" encoding="UTF-8"?><SetDoNotDisturb xmlns="http://www.ecma-international.org/standards/ecma-323/csta/ed3"><device>` +
		e.wp.Extension + `</device><doNotDisturbOn>true</doNotDisturbOn></SetDoNotDisturb>`
	res, err := e.wpPhone.Subscribe(ctx, e.wp.Extension, "as-feature-event", 300,
		[][2]string{{"Accept", feat}, {"Content-Type", feat}}, []byte(body))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("as-feature-event SUBSCRIBE = %v, %v; want 200", res, err)
	}
	extID := strconv.FormatInt(e.wpExtID, 10)
	eventually(t, 15*time.Second, "the phone's DND key to reach the extension", func() error {
		var x struct {
			DND bool `json:"dnd"`
		}
		if err := e.lc.do("GET", "/api/v1/extensions/"+extID, nil, &x, 200); err != nil {
			return err
		}
		if !x.DND {
			return errors.New("DND is still off")
		}
		return nil
	})
	// Hello to the phone: DND cleared in the UI follows to the key.
	drainNotifies(e.wpPhone.Notifies())
	e.lc.must("PATCH", "/api/v1/extensions/"+extID, map[string]any{"dnd": false}, nil, 200)
	waitNotify(t, e.wpPhone.Notifies(), "as-feature-event", "<doNotDisturbOn>false<")
	wpProve("dnd")
}

func (e *wpEnv) checkPresence(t *testing.T) {
	ctx := e.ctx(t)
	// Event: reg for the phone's own extension.
	res, err := e.wpPhone.Subscribe(ctx, e.wp.Extension, "reg", 120, nil, nil)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("reg SUBSCRIBE = %v, %v; want 200", res, err)
	}
	n := waitNotify(t, e.wpPhone.Notifies(), "reg", "registered")
	if ct := n.GetHeader("Content-Type"); ct == nil || ct.Value() != "application/reginfo+xml" {
		t.Fatalf("reg NOTIFY Content-Type = %v", ct)
	}
	// PUBLISH presence, then another phone reads it back.
	pidf := `<?xml version="1.0"?><presence xmlns="urn:ietf:params:xml:ns:pidf" entity="sip:` + e.wp.Extension + `@` + labDomain + `">` +
		`<tuple id="t1"><status><basic>open</basic></status></tuple><note>wp sweep note</note></presence>`
	res, err = e.wpPhone.Send(ctx, sip.PUBLISH, e.wp.Extension,
		[][2]string{{"Event", "presence"}, {"Expires", "300"}, {"Content-Type", "application/pidf+xml"}}, []byte(pidf))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("presence PUBLISH = %v, %v; want 200", res, err)
	}
	if h := res.GetHeader("SIP-ETag"); h == nil || h.Value() == "" {
		t.Fatal("PUBLISH answered without a SIP-ETag")
	}
	watcher := e.live(t, e.other)
	res, err = watcher.Subscribe(ctx, e.wp.Extension, "presence", 120, [][2]string{{"Accept", "application/pidf+xml"}}, nil)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("presence SUBSCRIBE = %v, %v; want 200", res, err)
	}
	waitNotify(t, watcher.Notifies(), "presence", "wp sweep note")
	wpProve("presence")
}

func (e *wpEnv) checkMessage(t *testing.T) {
	ctx := e.ctx(t)
	peer := e.live(t, e.other)
	text := "hello from the WP " + randHex(3)
	res, err := e.wpPhone.Send(ctx, sip.MESSAGE, e.other.Extension, [][2]string{{"Content-Type", "text/plain"}}, []byte(text))
	if err != nil || (res.StatusCode != 200 && res.StatusCode != 202) {
		t.Fatalf("MESSAGE from the WP = %v, %v; want 200 or 202", res, err)
	}
	waitMessage(t, peer.Messages(), text)
	// And to the WP, from the console.
	back := "hello to the WP " + randHex(3)
	e.lc.must("POST", "/api/v1/messages", map[string]any{"targetKind": "extension", "target": e.wp.Extension, "body": back}, nil, 202)
	waitMessage(t, e.wpPhone.Messages(), back)
	wpProve("message")
}

func waitMessage(t *testing.T, ch <-chan *sip.Request, want string) {
	t.Helper()
	deadline := time.After(25 * time.Second)
	for {
		select {
		case r := <-ch:
			if strings.Contains(string(r.Body()), want) {
				return
			}
		case <-deadline:
			t.Fatalf("no MESSAGE holding %q within 25s", want)
		}
	}
}

// calls the WP once from the other phone, answering with the given ringing.
// The WP's side of the exchange is returned for the caller to check.
func (e *wpEnv) callWP(t *testing.T, caller labDevice, reliable bool) (*sipua.Incoming, *sipua.Outgoing, *sipua.Phone) {
	ctx := e.ctx(t)
	cp := e.live(t, caller)
	in := make(chan *sipua.Incoming, 1)
	go func() {
		c, err := e.wpPhone.Next(ctx)
		if err != nil {
			in <- nil
			return
		}
		if reliable {
			_ = c.RingReliable(1)
		} else {
			_ = c.Ring()
		}
		in <- c
	}()
	type dialed struct {
		o   *sipua.Outgoing
		err error
	}
	dc := make(chan dialed, 1)
	go func() {
		o, err := cp.Dial(ctx, e.wp.Extension, sdpOffer)
		dc <- dialed{o, err}
	}()
	call := <-in
	if call == nil {
		t.Fatal("the WP did not receive the call")
	}
	if reliable {
		deadline := time.After(10 * time.Second)
		select {
		case r := <-e.wpPhone.Pracks():
			h := r.GetHeader("RAck")
			if h == nil || !strings.HasPrefix(h.Value(), "1 ") || !strings.HasSuffix(h.Value(), " INVITE") {
				t.Fatalf("PRACK RAck = %v, want \"1 <cseq> INVITE\"", h)
			}
		case <-deadline:
			t.Fatal("Hello did not PRACK the WP's reliable 180")
		}
	}
	if err := call.Answer(sdpAnswer); err != nil {
		t.Fatal(err)
	}
	d := <-dc
	if d.err != nil || d.o.Status != 200 {
		t.Fatalf("call to the WP = %+v, %v", d.o, d.err)
	}
	return call, d.o, cp
}

// checkRingTagAndPRACK is a call to the WP with a reliable provisional
// response: Hello offers 100rel, PRACKs the 180 and still connects, and the
// INVITE carries the ring tag's Alert-Info, which the CDR records.
func (e *wpEnv) checkRingTagAndPRACK(t *testing.T) {
	call, out, _ := e.callWP(t, e.other, true)
	if h := call.Request.GetHeader("Supported"); h == nil || !strings.Contains(strings.ToLower(h.Value()), "100rel") {
		t.Errorf("the INVITE to the WP carries Supported %v, want 100rel", h)
	}
	if h := call.Request.GetHeader("Alert-Info"); h == nil || h.Value() == "" {
		t.Error("the INVITE to the WP carries no Alert-Info (ring tag)")
	}
	e.lastCallID = call.Request.CallID().Value()
	if err := out.Hangup(e.ctx(t)); err != nil {
		t.Fatal(err)
	}
	cdr := e.cdrFor(t, e.other.Extension, e.wp.Extension)
	if cdr.RingTag == "" {
		t.Errorf("CDR %d has no ring tag", cdr.ID)
	}
	e.lastCDR = cdr.ID
	if !t.Failed() {
		wpProve("ring-tag")
	}
}

type wpCDR struct {
	ID      int64  `json:"id"`
	Source  string `json:"source"`
	Kind    string `json:"kind"`
	RingTag string `json:"ringTag"`
	RTCPXR  []struct {
		Device    string   `json:"device"`
		Extension string   `json:"extension"`
		Final     bool     `json:"final"`
		MOSLQ     *float64 `json:"mosLq"`
	} `json:"rtcpxr"`
}

// cdrFor waits for the CDR of a call from src to dst and returns its detail.
func (e *wpEnv) cdrFor(t *testing.T, src, dst string) wpCDR {
	var out wpCDR
	eventually(t, 20*time.Second, "the CDR of the call", func() error {
		var list struct {
			Items []labCDR `json:"items"`
		}
		if err := e.lc.do("GET", "/api/v1/cdrs?limit=200", nil, &list, 200); err != nil {
			return err
		}
		var best int64
		for _, c := range list.Items {
			if c.Source == src && c.Destination == dst && c.ID > best {
				best = c.ID
			}
		}
		if best == 0 {
			return errors.New("no CDR yet")
		}
		return e.lc.do("GET", fmt.Sprintf("/api/v1/cdrs/%d", best), nil, &out, 200)
	})
	return out
}

func (e *wpEnv) checkPark(t *testing.T) {
	ctx := e.ctx(t)
	if e.transp == "tls" { // the WP8x6 file carries the codes its softkeys use
		if e.cfg.ParkCode != e.park || e.cfg.ParkRetrieveCode != e.retrieve {
			t.Fatalf("file park %q retrieve %q (P60069, P2336), want %q and %q", e.cfg.ParkCode, e.cfg.ParkRetrieveCode, e.park, e.retrieve)
		}
	}
	call, out, _ := e.callWP(t, e.other, false)
	res, err := call.Refer(ctx, "<sip:"+e.park+"@"+labDomain+">")
	if err != nil || res.StatusCode != 202 {
		t.Fatalf("Park REFER = %v, %v; want 202", res, err)
	}
	var slot string
	eventually(t, 15*time.Second, "the call in the parked-calls list", func() error {
		var list struct {
			Items []livestate.ParkedCall `json:"items"`
		}
		if err := e.lc.do("GET", "/api/v1/parked-calls", nil, &list, 200); err != nil {
			return err
		}
		for _, p := range list.Items {
			if p.ParkedBy == e.wp.Extension {
				slot = p.Slot
				return nil
			}
		}
		return errors.New("nothing parked by the WP yet")
	})
	select {
	case <-call.Ended():
	case <-time.After(10 * time.Second):
		t.Fatal("the WP was not released by the park")
	}
	picker := e.live(t, e.third)
	got, err := picker.Dial(ctx, e.retrieve+slot, sdpOffer)
	if err != nil || got.Status != 200 {
		t.Fatalf("retrieve %s%s = %+v, %v; want 200", e.retrieve, slot, got, err)
	}
	if err := out.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got.Ended():
	case <-time.After(10 * time.Second):
		t.Fatal("the retriever's call did not end with the parked party's")
	}
	wpProve("park")
}

// checkPickup is the WP picking up a call ringing a colleague in its pickup
// group, with the directed code.
func (e *wpEnv) checkPickup(t *testing.T) {
	ctx := e.ctx(t)
	caller := e.live(t, e.other)
	ringer := e.live(t, e.third)
	type dialed struct {
		o   *sipua.Outgoing
		err error
	}
	dc := make(chan dialed, 1)
	go func() {
		o, err := caller.Dial(ctx, e.third.Extension, sdpOffer)
		dc <- dialed{o, err}
	}()
	ringing, err := ringer.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ringing.Ring(); err != nil {
		t.Fatal(err)
	}
	got, err := e.wpPhone.Dial(ctx, e.pickup+e.third.Extension, sdpAnswer)
	if err != nil || got.Status != 200 {
		t.Fatalf("directed pickup %s%s = %+v, %v; want 200", e.pickup, e.third.Extension, got, err)
	}
	d := <-dc
	if d.err != nil || d.o.Status != 200 {
		t.Fatalf("the caller of the picked-up call = %+v, %v; want 200", d.o, d.err)
	}
	select {
	case <-ringing.Canceled():
	case <-time.After(10 * time.Second):
		t.Fatal("the colleague's phone kept ringing after the pickup")
	}
	if err := got.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.o.Ended():
	case <-time.After(10 * time.Second):
		t.Fatal("the caller was not released when the WP hung up")
	}
	wpProve("pickup")
}

// checkDoor is a call from a door station: the WP rings with the doorbell tag
// and the CDR's kind is door.
func (e *wpEnv) checkDoor(t *testing.T) {
	call, out, _ := e.callWP(t, e.door, false)
	if h := call.Request.GetHeader("Alert-Info"); h == nil || h.Value() == "" {
		t.Error("the door call carries no Alert-Info")
	}
	if err := out.Hangup(e.ctx(t)); err != nil {
		t.Fatal(err)
	}
	cdr := e.cdrFor(t, e.door.Extension, e.wp.Extension)
	if cdr.Kind != "door" || cdr.RingTag != "doorbell" {
		t.Errorf("CDR %d kind %q, ring tag %q; want door and doorbell", cdr.ID, cdr.Kind, cdr.RingTag)
	}
	if !t.Failed() {
		wpProve("door")
	}
}

// checkRTCPXR is the WP's call-end quality report (RFC 6035) for the ring-tag
// call: it is stored and shown on that call's CDR.
func (e *wpEnv) checkRTCPXR(t *testing.T) {
	if e.lastCallID == "" || e.lastCDR == 0 {
		t.Skip("the ring-tag subtest did not leave a call to report on")
	}
	body := "VQSessionReport: CallTerm\r\nLocalMetrics:\r\nTimestamps: START=2026-11-12T10:00:00Z STOP=2026-11-12T10:00:05Z\r\n" +
		"PacketLoss: NLR=1.0 JDR=0.2\r\nDelay: RTD=30 IAJ=6\r\nQualityEst: MOSLQ=4.1 MOSCQ=3.9\r\n" +
		"DialogID: " + e.lastCallID + ";to-tag=t;from-tag=f\r\n"
	res, err := e.wpPhone.SendTo(e.ctx(t), sip.PUBLISH, "collector", e.wp.User,
		[][2]string{{"Event", rtcpxr.Event}, {"Content-Type", rtcpxr.ContentType}}, []byte(body))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("vq-rtcpxr PUBLISH = %v, %v; want 200", res, err)
	}
	eventually(t, 15*time.Second, "the report on the CDR", func() error {
		var c wpCDR
		if err := e.lc.do("GET", fmt.Sprintf("/api/v1/cdrs/%d", e.lastCDR), nil, &c, 200); err != nil {
			return err
		}
		for _, q := range c.RTCPXR {
			if q.Device == e.wp.User && q.Final && q.MOSLQ != nil && *q.MOSLQ > 4.09 && *q.MOSLQ < 4.11 {
				return nil
			}
		}
		return fmt.Errorf("CDR %d carries reports %+v, none from %s with MOS-LQ 4.1", e.lastCDR, c.RTCPXR, e.wp.User)
	})
	wpProve("rtcpxr")
}
