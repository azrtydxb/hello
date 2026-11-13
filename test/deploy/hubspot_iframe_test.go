package deploy

import (
	"strings"
	"testing"
)

// TestHubSpotIframeFraming (spec integrations I-50, T-12) fails if the UI's
// nginx template lets any page but the HubSpot calling iframe be framed, or
// lets the iframe be framed by anything but HubSpot's hosts.
func TestHubSpotIframeFraming(t *testing.T) {
	iframe := nginxLocation(t, "= /integrations/hubspot/iframe")
	if iframe == "" {
		t.Fatal("nginx: no location for the HubSpot iframe")
	}
	if !strings.Contains(iframe, `Content-Security-Policy "frame-ancestors https://app.hubspot.com https://*.hubspot.com" always`) {
		t.Error("nginx: the HubSpot iframe is not limited to HubSpot's hosts")
	}
	if strings.Contains(iframe, "X-Frame-Options") {
		t.Error("nginx: X-Frame-Options would stop HubSpot framing the iframe")
	}
	for _, loc := range []string{"/", "= /oauth/consent"} {
		body := nginxLocation(t, loc)
		if !strings.Contains(body, `Content-Security-Policy "frame-ancestors 'none'" always`) || !strings.Contains(body, `X-Frame-Options "DENY" always`) {
			t.Errorf("nginx: location %s may be framed", loc)
		}
	}
}
