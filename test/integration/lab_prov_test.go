package integration

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/test/provclient"
	"github.com/azrtydxb/hello/test/sipua"
)

// labProv is where the lab's provisioning listener is published
// (compose.yaml x-prov-env). The listener terminates TLS itself in the
// lab, so the boot path is fetched over HTTPS here; on kw the ingress
// serves it on plain HTTP as DHCP option 66 says.
const (
	labProv     = "https://localhost:8443"
	labProvBoot = labProv + "/p/boot/"
)

// provHTTP is an HTTP client that trusts only the lab's provisioning CA,
// copied out of the hello-control-1 container.
func provHTTP(t *testing.T) *http.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.crt")
	if out, err := compose("cp", "hello-control-1:/etc/hello/prov/ca.crt", path).CombinedOutput(); err != nil {
		t.Fatalf("copy the lab CA: %v\n%s", err, out)
	}
	pem, err := os.ReadFile(path) //nolint:gosec // G304: the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("the lab CA file holds no certificate")
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
}

// labPhoneView is the part of the phone JSON the test checks.
type labPhoneView struct {
	ID              int64  `json:"id"`
	BootArmed       bool   `json:"bootArmed"`
	BootReclaimed   bool   `json:"bootReclaimed"`
	UAMismatch      bool   `json:"uaMismatch"`
	FirmwareSeen    string `json:"firmwareSeen"`
	ProvisioningURL string `json:"provisioningUrl"`
	SecretRotated   bool   `json:"secretRotated"`
}

// TestProvisioningAsVendors is spec S-18's end-to-end proof: for each
// first-class vendor a phone assigned to an extension boots from the DHCP
// option 66 URL, takes its per-device URL in the trust-on-first-use
// hand-off, fetches its files over HTTPS in its vendor's sequence with its
// User-Agent, and registers through Kamailio with the credentials it
// parsed. It fails if any vendor's sequence is not served, the hand-off
// carries an account or another URL than the phone's, a parsed field
// differs from the device, or the registration is not answered 200 OK.
func TestProvisioningAsVendors(t *testing.T) {
	lc := newLabClient(t)
	hc := provHTTP(t)
	pc := provclient.Client{HTTP: hc}

	// One extension every phone watches on a BLF key.
	blf := "7" + randDigits(7)
	lc.must("POST", "/api/v1/extensions", map[string]string{"number": blf, "name": "blf " + blf}, nil, 201)

	for _, m := range provclient.Models {
		t.Run(m.Vendor, func(t *testing.T) {
			lc := &labClient{t: t, c: lc.c}
			d := lc.createExtension("desk")[0]
			devID := d.ID
			mac := m.OUI + randHex(3)
			var created labPhoneView
			lc.must("POST", "/api/v1/phones", map[string]any{
				"mac": mac, "vendor": m.Vendor, "model": m.Model, "label": "lab " + m.Vendor,
				"extensionId": mustInt(t, lc.extensionID(d.Extension)), "deviceId": devID,
				"blf": []string{blf}, "enabled": true,
			}, &created, 201)
			remember(tokenOf(created.ProvisioningURL))
			if !created.BootArmed || !created.SecretRotated {
				t.Fatalf("new phone: bootArmed %v, secretRotated %v; want both", created.BootArmed, created.SecretRotated)
			}
			// Binding rotated the device's secret: wait until the SIP nodes
			// hold it before the phone registers.
			lc.waitSnapshots(10 * time.Second)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			boot, err := pc.Boot(ctx, m, mac, labProvBoot)
			if err != nil {
				t.Fatalf("boot pass %v: %v", boot.Fetches, err)
			}
			want := strings.TrimSuffix(created.ProvisioningURL, "{mac}")
			if boot.Config.ProvURL != want {
				t.Fatalf("hand-off URL is not the phone's per-device URL (%d vs %d chars)", len(boot.Config.ProvURL), len(want))
			}
			if boot.Config.Password != "" || boot.Config.AdminPassword != "" || boot.Config.User != "" {
				t.Fatal("the boot hand-off carries an account or the admin password")
			}

			prov, err := pc.Provision(ctx, m, mac, boot.Config.ProvURL)
			if err != nil {
				t.Fatalf("per-device pass %v: %v", prov.Fetches, err)
			}
			c := prov.Config
			remember(c.Password, c.AdminPassword)
			if c.Password == "" || c.AdminPassword == "" {
				t.Fatal("the device file lacks the SIP secret or the admin password")
			}
			checks := []struct {
				field     string
				got, want any
			}{
				{"server", net.JoinHostPort(c.Server, strconv.Itoa(c.Port)), labKamailio},
				{"user", c.User, d.User},
				{"display name", c.DisplayName, "test " + d.Extension},
				{"BLF", fmt.Sprint(c.BLF), fmt.Sprint([]string{blf})},
			}
			if c.AuthUser != "" || m.Vendor != "snom" { // Snom's auth-name key is unconfirmed
				checks = append(checks, struct {
					field     string
					got, want any
				}{"auth user", c.AuthUser, d.User})
			}
			for _, ck := range checks {
				if ck.got != ck.want {
					t.Errorf("parsed %s = %v, want %v", ck.field, ck.got, ck.want)
				}
			}
			if t.Failed() {
				t.FailNow()
			}

			p, err := sipua.New(sipua.Options{
				User: c.User, Password: c.Password, Domain: labDomain, Proxy: net.JoinHostPort(c.Server, strconv.Itoa(c.Port)),
				Listen: "0.0.0.0:0", ContactHost: os.Getenv("HELLO_LAB_CONTACT_HOST"),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(p.Close)
			register(t, p)

			var got labPhoneView
			lc.must("GET", "/api/v1/phones/"+strconv.FormatInt(created.ID, 10), nil, &got, 200)
			if got.BootArmed || got.BootReclaimed || got.UAMismatch || got.FirmwareSeen != m.Firmware {
				t.Fatalf("phone after provisioning: bootArmed %v, bootReclaimed %v, uaMismatch %v, firmwareSeen %q (want false, false, false, %q)",
					got.BootArmed, got.BootReclaimed, got.UAMismatch, got.FirmwareSeen, m.Firmware)
			}
			eventually(t, 15*time.Second, "the fetch audit to record the hand-off", func() error {
				var fetches struct {
					Items []struct {
						Result string `json:"result"`
					} `json:"items"`
				}
				if err := lc.do("GET", "/api/v1/phones/"+strconv.FormatInt(created.ID, 10)+"/fetches", nil, &fetches, 200); err != nil {
					return err
				}
				var results []string
				for _, f := range fetches.Items {
					results = append(results, f.Result)
				}
				if !slices.Contains(results, "boot_handoff") || !slices.Contains(results, "served") {
					return fmt.Errorf("results %v lack boot_handoff and served", results)
				}
				return nil
			})

			// A second boot from the same source inside the hand-off grace
			// is the same boot cycle: the same hand-off, not a reclaim.
			again, err := pc.Boot(ctx, m, mac, labProvBoot)
			if err != nil || again.Config.ProvURL != boot.Config.ProvURL {
				t.Fatalf("a repeat boot inside the grace did not get the same hand-off (%v, %v)", again.Fetches, err)
			}
		})
	}
}

// randHex returns n random bytes as lowercase hex.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// tokenOf is the token in a per-device URL, for TestNoSecretsInLogs.
func tokenOf(u string) string {
	_, rest, ok := strings.Cut(u, "/p/")
	if !ok {
		return ""
	}
	tok, _, _ := strings.Cut(rest, "/")
	return tok
}
