package oauth

import "testing"

// TestPreregisteredDialer fails if the dialer or the HubSpot calling iframe is
// not a preregistered client whose only redirect URI is its callback under the
// public URL.
func TestPreregisteredDialer(t *testing.T) {
	cs := PreregisteredClients("https://hello.example/")
	if len(cs) != 4 {
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

// TestPreregisteredEmbeddedApps fails if the Salesforce or Dynamics app is
// not a verified public client with exactly one redirect URI, its own
// callback page under the public URL.
func TestPreregisteredEmbeddedApps(t *testing.T) {
	want := map[string]string{
		"hello-salesforce": "https://hello.example/integrations/salesforce/callback",
		"hello-dynamics":   "https://hello.example/integrations/dynamics/callback",
	}
	for _, c := range PreregisteredClients("https://hello.example/") {
		w, ok := want[c.ID]
		if !ok {
			continue
		}
		delete(want, c.ID)
		if c.Kind != ClientPreregistered || len(c.RedirectURIs) != 1 || c.RedirectURIs[0] != w {
			t.Errorf("%s = %+v", c.ID, c)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing clients %v", want)
	}
}
