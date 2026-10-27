package oauth

import "strings"

// DialerClientID is Hello's own click-to-call app (spec integrations I-32).
const DialerClientID = "hello-dialer"

// PreregisteredClients are Hello's own first-party apps as public clients
// with PKCE and fixed redirect URIs derived from the deployment's public URL.
// Each phase that ships an app adds its client here (P3 the dialer; the
// browser extension, HubSpot and Salesforce follow). They are Verified, so
// the consent screen names them.
func PreregisteredClients(publicURL string) []Client {
	base := strings.TrimRight(publicURL, "/")
	return []Client{{
		ID: DialerClientID, Kind: ClientPreregistered, Name: "Hello dialer", ClientURI: base,
		RedirectURIs: []string{base + "/integrations/dialer/callback"},
	}}
}
