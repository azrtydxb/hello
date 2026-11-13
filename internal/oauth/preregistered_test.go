package oauth

import "testing"

// TestPreregisteredDialer fails if the dialer or the HubSpot calling iframe is
// not a preregistered client whose only redirect URI is its callback under the
// public URL.
func TestPreregisteredDialer(t *testing.T) {
	cs := PreregisteredClients("https://hello.example/")
	if len(cs) != 2 {
		t.Fatalf("clients = %+v", cs)
	}
	c := cs[0]
	if c.ID != "hello-dialer" || c.Kind != ClientPreregistered ||
		len(c.RedirectURIs) != 1 || c.RedirectURIs[0] != "https://hello.example/integrations/dialer/callback" {
		t.Fatalf("dialer = %+v", c)
	}
	h := cs[1]
	if h.ID != HubSpotClientID || h.Kind != ClientPreregistered ||
		len(h.RedirectURIs) != 1 || h.RedirectURIs[0] != "https://hello.example/integrations/hubspot/callback" {
		t.Fatalf("hubspot = %+v", h)
	}
}
