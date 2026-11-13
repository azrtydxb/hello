package oauth

import "strings"

// DialerClientID is Hello's own click-to-call app (spec integrations I-32).
const DialerClientID = "hello-dialer"

// SalesforceClientID and DynamicsClientID are the apps Hello serves inside
// Salesforce (Open CTI softphone) and Dynamics 365 (CIF widget). Both are
// framed by another site, so they sign in through a popup and receive the
// token by postMessage on their own origin.
const (
	SalesforceClientID = "hello-salesforce"
	DynamicsClientID   = "hello-dynamics"
)

// PreregisteredClients are Hello's own first-party apps as public clients
// with PKCE and fixed redirect URIs derived from the deployment's public URL.
// Each phase that ships an app adds its client here (P3 the dialer, P13 Salesforce and
// P19 Dynamics; the browser extension and HubSpot follow). They are Verified, so
// the consent screen names them.
func PreregisteredClients(publicURL string) []Client {
	base := strings.TrimRight(publicURL, "/")
	return []Client{{
		ID: DialerClientID, Kind: ClientPreregistered, Name: "Hello dialer", ClientURI: base,
		RedirectURIs: []string{base + "/integrations/dialer/callback"},
	}, {
		ID: SalesforceClientID, Kind: ClientPreregistered, Name: "Hello for Salesforce", ClientURI: base,
		RedirectURIs: []string{base + "/integrations/salesforce/callback"},
	}, {
		ID: DynamicsClientID, Kind: ClientPreregistered, Name: "Hello for Dynamics 365", ClientURI: base,
		RedirectURIs: []string{base + "/integrations/dynamics/callback"},
	}}
}
