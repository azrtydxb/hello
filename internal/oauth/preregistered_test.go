package oauth

import "testing"

// TestPreregisteredDialer fails if the dialer is not a preregistered client
// whose only redirect URI is its callback under the public URL.
func TestPreregisteredDialer(t *testing.T) {
	cs := PreregisteredClients("https://hello.example/")
	if len(cs) != 1 {
		t.Fatalf("clients = %+v", cs)
	}
	c := cs[0]
	if c.ID != "hello-dialer" || c.Kind != ClientPreregistered ||
		len(c.RedirectURIs) != 1 || c.RedirectURIs[0] != "https://hello.example/integrations/dialer/callback" {
		t.Fatalf("dialer = %+v", c)
	}
}
