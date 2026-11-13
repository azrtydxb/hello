package oauth

import "strings"

// DialerClientID is Hello's own click-to-call app (spec integrations I-32).
const DialerClientID = "hello-dialer"

// HubSpotClientID is the OAuth client of the HubSpot calling iframe (spec
// integrations I-50): the iframe signs the user in through a popup and holds
// a calls and events token of its own.
const HubSpotClientID = "hello-hubspot"

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
	}, {
		ID: HubSpotClientID, Kind: ClientPreregistered, Name: "Hello for HubSpot", ClientURI: base,
		RedirectURIs: []string{base + "/integrations/hubspot/callback"},
	}}
}
