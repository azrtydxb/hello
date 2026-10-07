package redirect

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/prov"
)

// liveURL is the URL the live test registers; it is restored afterwards.
const liveURL = "https://prov.hello.kw.watteel.lab/p/redirect-live-test/{mac}"

// TestRedirectLive registers each vendor's live-test phone with the real
// redirect service, reads it back where the API allows, and restores the
// previous registration (spec S-11). It runs only with
// HELLO_PROV_LIVE_REDIRECT=1; a vendor whose credential (HELLO_PROV_*) or
// live-test key (HELLO_PROV_LIVE_*) is missing is skipped with the key
// named, which reports it as not verified.
func TestRedirectLive(t *testing.T) {
	if os.Getenv("HELLO_PROV_LIVE_REDIRECT") != "1" {
		t.Skip("HELLO_PROV_LIVE_REDIRECT is not 1")
	}
	dep := Deployment(config.ProvRedirect{
		SnomKeyID: os.Getenv("HELLO_PROV_SNOM_KEY_ID"), SnomKeySecret: os.Getenv("HELLO_PROV_SNOM_KEY_SECRET"),
		YealinkKey: os.Getenv("HELLO_PROV_YEALINK_KEY"), YealinkSecret: os.Getenv("HELLO_PROV_YEALINK_SECRET"),
		YMCSClientID: os.Getenv("HELLO_PROV_YMCS_CLIENT_ID"), YMCSClientSecret: os.Getenv("HELLO_PROV_YMCS_CLIENT_SECRET"),
		YMCSRegion:   os.Getenv("HELLO_PROV_YMCS_REGION"),
		GDMSClientID: os.Getenv("HELLO_PROV_GDMS_CLIENT_ID"), GDMSClientSecret: os.Getenv("HELLO_PROV_GDMS_CLIENT_SECRET"),
		GDMSUsername: os.Getenv("HELLO_PROV_GDMS_USERNAME"), GDMSPassword: os.Getenv("HELLO_PROV_GDMS_PASSWORD"),
		GDMSRegion: os.Getenv("HELLO_PROV_GDMS_REGION"), GDMSSiteID: os.Getenv("HELLO_PROV_GDMS_SITE_ID"),
	})
	for _, c := range []struct {
		vendor prov.Vendor
		creds  []string // env keys of the credential group
		live   []string // env keys of the live-test phone
	}{
		{prov.Snom, []string{"HELLO_PROV_SNOM_KEY_ID", "HELLO_PROV_SNOM_KEY_SECRET"}, []string{"HELLO_PROV_LIVE_SNOM_MAC"}},
		{prov.Yealink, nil, []string{"HELLO_PROV_LIVE_YEALINK_MAC", "HELLO_PROV_LIVE_YEALINK_SERIAL"}},
		{prov.Grandstream, []string{
			"HELLO_PROV_GDMS_CLIENT_ID", "HELLO_PROV_GDMS_CLIENT_SECRET", "HELLO_PROV_GDMS_USERNAME",
			"HELLO_PROV_GDMS_PASSWORD", "HELLO_PROV_GDMS_REGION", "HELLO_PROV_GDMS_SITE_ID",
		}, []string{"HELLO_PROV_LIVE_GDMS_MAC", "HELLO_PROV_LIVE_GDMS_SERIAL"}},
	} {
		t.Run(string(c.vendor), func(t *testing.T) {
			if _, ok := dep[c.vendor]; !ok {
				if c.vendor == prov.Yealink {
					t.Skip("not verified: HELLO_PROV_YEALINK_KEY (RPS) or HELLO_PROV_YMCS_CLIENT_ID (YMCS) is not set")
				}
			}
			for _, k := range append(c.creds, c.live...) {
				if os.Getenv(k) == "" {
					t.Skipf("not verified: %s is not set", k)
				}
			}
			mac, serial := os.Getenv(c.live[0]), ""
			if len(c.live) > 1 {
				serial = os.Getenv(c.live[1])
			}
			liveRoundTrip(t, c.vendor, dep[c.vendor], mac, serial)
		})
	}
}

func liveRoundTrip(t *testing.T, v prov.Vendor, creds Credentials, mac, serial string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var settings []byte
	if v == prov.Yealink && creds[KeyYealinkKey] == "" {
		settings = []byte(`{"api":"ymcs"}`) // YMCS only when chosen (spec S-11)
	}
	cl, err := New(v, creds, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	if g, ok := cl.(*gdms); ok {
		// GDMS keeps no URL: add the device, and remove it again only if
		// it was not there before.
		existed, err := g.add(ctx, mac, serial)
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		if !existed {
			if err := g.Unregister(ctx, mac); err != nil {
				t.Fatalf("restore (unregister): %v", err)
			}
		}
		return
	}
	prev, had, err := cl.Lookup(ctx, mac)
	if err != nil {
		t.Fatalf("lookup before: %v", err)
	}
	test := strings.ReplaceAll(liveURL, "{mac}", strings.ToLower(mac))
	if err := cl.Register(ctx, mac, serial, test); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, found, err := cl.Lookup(ctx, mac)
	if err != nil && !errors.Is(err, ErrUnsupported) {
		t.Fatalf("read back: %v", err)
	}
	restore := func() error {
		if had {
			return cl.Register(ctx, mac, serial, prev)
		}
		return cl.Unregister(ctx, mac)
	}
	if err == nil && (!found || got != test) {
		_ = restore()
		t.Fatalf("read back %q (found %v), want the test URL", got, found)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if had {
		if back, _, err := cl.Lookup(ctx, mac); err != nil || back != prev {
			t.Fatalf("after restore the registration is %q (%v), want the previous one", back, err)
		}
	}
}
