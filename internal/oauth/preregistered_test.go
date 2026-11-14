package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

// TestPreregisteredDialer fails if the dialer or the HubSpot calling iframe is
// not a preregistered client whose only redirect URI is its callback under the
// public URL.
func TestPreregisteredDialer(t *testing.T) {
	cs := PreregisteredClients("https://hello.example/")
	if len(cs) != 5 {
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

// TestPreregisteredExtension fails if the browser extension is not a
// preregistered public client whose only redirect URI is the chromiumapp.org
// address of its fixed id.
func TestPreregisteredExtension(t *testing.T) {
	var c Client
	for _, x := range PreregisteredClients("https://hello.example") {
		if x.ID == ExtensionClientID {
			c = x
		}
	}
	if c.ID != "hello-extension" || c.Kind != ClientPreregistered || len(c.RedirectURIs) != 1 ||
		c.RedirectURIs[0] != "https://kengpjalagamnmmkljldjhhgonmbdcph.chromiumapp.org/" {
		t.Fatalf("extension = %+v", c)
	}
	if !redirectMatches(c.RedirectURIs, ExtensionRedirectURI) || redirectMatches(c.RedirectURIs, "https://evil.example/") {
		t.Fatal("the extension redirect is not matched exactly")
	}
}

// TestExtensionIDMatchesManifest fails if ExtensionID is not the id that the
// public key in web/extension/manifest.json gives (first 16 bytes of the
// SHA-256 of the DER key, written with the letters a to p). The two drifting
// apart would break every extension sign-in.
func TestExtensionIDMatchesManifest(t *testing.T) {
	raw, err := os.ReadFile("../../web/extension/manifest.json")
	if err != nil {
		t.Skipf("no extension manifest: %v", err)
	}
	var m struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Key == "" {
		t.Fatalf("manifest key: %v", err)
	}
	der, err := base64.StdEncoding.DecodeString(m.Key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	id := make([]byte, 0, 32)
	for _, b := range sum[:16] {
		id = append(id, 'a'+b>>4, 'a'+b&15)
	}
	if string(id) != ExtensionID {
		t.Fatalf("manifest key gives extension id %s, ExtensionID is %s", id, ExtensionID)
	}
}
