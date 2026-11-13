package oauth

import "strings"

// DialerClientID is Hello's own click-to-call app (spec integrations I-32).
const DialerClientID = "hello-dialer"

// ExtensionClientID is Hello's browser extension (spec integrations I-55).
const ExtensionClientID = "hello-extension"

// ExtensionID is the browser extension's id. The extension's manifest carries
// a fixed public key (web/extension/manifest.json) and a Chromium extension id
// is derived from that key alone, so the id, and the redirect address below,
// are the same for every build and every deployment. A test recomputes it from
// the manifest, so the two cannot drift apart.
const ExtensionID = "kengpjalagamnmmkljldjhhgonmbdcph"

// ExtensionRedirectURI is where chrome.identity.launchWebAuthFlow returns the
// authorization code: the address chrome.identity.getRedirectURL() gives.
const ExtensionRedirectURI = "https://" + ExtensionID + ".chromiumapp.org/"

// SalesforceClientID and DynamicsClientID are the apps Hello serves inside
// Salesforce (Open CTI softphone) and Dynamics 365 (CIF widget). Both are
// framed by another site, so they sign in through a popup and receive the
// token by postMessage on their own origin.
const (
	SalesforceClientID = "hello-salesforce"
	DynamicsClientID   = "hello-dynamics"
)

// HubSpotClientID is the OAuth client of the HubSpot calling iframe (spec
// integrations I-50): the iframe signs the user in through a popup and holds
// a calls and events token of its own.
const HubSpotClientID = "hello-hubspot"

// PreregisteredClients are Hello's own first-party apps as public clients
// with PKCE and fixed redirect URIs derived from the deployment's public URL.
// Each phase that ships an app adds its client here (P3 the dialer, P13 Salesforce and
// P19 Dynamics, P5 the browser extension). They are Verified, so
// the consent screen names them.
func PreregisteredClients(publicURL string) []Client {
	base := strings.TrimRight(publicURL, "/")
	return []Client{{
		ID: DialerClientID, Kind: ClientPreregistered, Name: "Hello dialer", ClientURI: base,
		RedirectURIs: []string{base + "/integrations/dialer/callback"},
	}, {
		ID: HubSpotClientID, Kind: ClientPreregistered, Name: "Hello for HubSpot", ClientURI: base,
		RedirectURIs: []string{base + "/integrations/hubspot/callback"},
	}, {
		ID: SalesforceClientID, Kind: ClientPreregistered, Name: "Hello for Salesforce", ClientURI: base,
		RedirectURIs: []string{base + "/integrations/salesforce/callback"},
	}, {
		ID: DynamicsClientID, Kind: ClientPreregistered, Name: "Hello for Dynamics 365", ClientURI: base,
		RedirectURIs: []string{base + "/integrations/dynamics/callback"},
	}, {
		ID: ExtensionClientID, Kind: ClientPreregistered, Name: "Hello browser extension", ClientURI: base,
		RedirectURIs: []string{ExtensionRedirectURI},
	}}
}
