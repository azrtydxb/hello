package prov

// 802.1X and the SCEP settings a phone's configuration carries
// (phone-feature-parity S-25). An 802.1X profile is a reusable EAP identity:
// a phone group names one for the wired port, and the Wi-Fi profiles of S-23
// name one each for an enterprise network. The store resolves every profile
// a phone may use into RenderData.Dot1X; the template writes the vendor keys.
//
// Vendor keys, all from Grandstream's published configuration templates
// (config-template-10212026.zip), not yet confirmed on a phone (verify):
//
//   - SCEP, wp8x6_config_1.0.3.39 "Maintenance/Security Settings/SCEP" and
//     grp260x_config_1.0.7.74 (the same keys): P8647 enable, P8648 server
//     URL, P8652 CA fingerprint, P8653 challenge password, P8654 key length
//     (1 = 2048 bit), P8655 applicant name. The WP856 (1.0.3.16), WP8xx
//     (1.0.11.83) and GRP261x/26xx (1.0.13.127, 1.0.15.23) templates have
//     no SCEP keys.
//   - Wired 802.1X, grp26xx_config_1.0.15.23, grp261x_2x_3x_5x_7x_config_
//     1.0.13.127 and grp260x_config_1.0.7.74 "Network/Advanced Settings":
//     P7901 mode (0 off, 1 EAP-MD5, 2 EAP-TLS, 3 EAP-PEAPv0/MSCHAPv2), P7902
//     identity (64 characters at most), P7903 password, P8439 CA
//     certificate, P8440 client certificate. The WP handsets have no wired
//     port.
//   - Wi-Fi EAP (for S-23), wp8x6_config_1.0.3.39 "Available Wi-Fi": P83002
//     security mode (5 WPA-802.1x, 12 WPA3-802.1x), P83004 EAP method (1
//     TLS, 2 PEAP), P83016 CA certificate type (1 system certificates, 3
//     uploaded), P83018/P83019 domain validation, P83013 phase 2 (0
//     MSCHAPv2), P83011 CA certificate, P83012 anonymous identity, P83014
//     identity, P83015 password (PEAP); P83007 CA certificate, P83008 client
//     certificate, P83009 identity, P83010 key password (TLS). Those are the
//     first network's keys; wifi.go repeats them per network slot.

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"regexp"
	"strings"
)

// The 802.1X methods a profile may use.
const (
	Dot1XEAPTLS = "eap-tls" // certificate; the certificate comes from SCEP
	Dot1XPEAP   = "peap"    // PEAPv0 with MSCHAPv2, identity and password
	Dot1XEAPMD5 = "eap-md5" // identity and password; wired ports only
)

// Dot1XMethods lists the methods in the order the API documents them.
var Dot1XMethods = []string{Dot1XEAPTLS, Dot1XPEAP, Dot1XEAPMD5}

// Dot1XMaxIdentity is the longest identity after expansion (P7902).
const Dot1XMaxIdentity = 64

// SCEPInfo is a phone's SCEP client settings: the server URL (its own
// provisioning URL, so it carries the token; masked in previews), the
// one-time challenge password (masked in previews) and the CA's SHA-256
// fingerprint. Nil in RenderData unless SCEP is on for the phone's group,
// a CA exists, HELLO_SCEP is on and the phone's model has SCEP keys.
type SCEPInfo struct {
	URL       string
	Challenge string
	// CAFingerprint is the CA certificate's SHA-256, colon-separated hex.
	// The built-in template does not write it (P8652's expected format is
	// not documented; verify on a phone) and the phone takes the CA from
	// GetCACert over the HTTPS connection it already trusts.
	CAFingerprint string
	// KeyBits is the size of the key the phone generates (2048).
	KeyBits int
}

// Dot1XProfile is one 802.1X profile as a phone sees it: the identity with
// its placeholders filled in and the password opened (masked in previews).
type Dot1XProfile struct {
	ID                int64
	Name              string
	Method            string // eap-tls, peap or eap-md5
	Identity          string
	AnonymousIdentity string
	Password          string
	// CACertPEM is the CA the RADIUS server's certificate chains to, empty
	// when the profile names none.
	CACertPEM string
	// ServerDomain is the RADIUS server name the phone checks, empty for
	// none.
	ServerDomain string
	// Usable is false for an EAP-TLS profile on a phone that has no SCEP
	// certificate source (SCEP off for it): the template then leaves the
	// port alone instead of switching it to a method that cannot succeed.
	Usable bool
	// GSWiredMode is the Grandstream P7901 value: 1 EAP-MD5, 2 EAP-TLS, 3
	// PEAPv0/MSCHAPv2.
	GSWiredMode int
	// GSWifiEAP is the Grandstream WP8x6 P83004 value: 1 EAP-TLS, 2 PEAP;
	// 0 for EAP-MD5, which Wi-Fi does not offer.
	GSWifiEAP int
	// GSWifiCAType is the WP8x6 P83016 value: 3 (uploaded certificate)
	// with a CA, else 1 (the phone's system certificates). Hello never
	// writes 0 (skip verification).
	GSWifiCAType int
}

// Dot1XInfo is RenderData.Dot1X: the wired profile and every profile, for
// the Wi-Fi profiles to look up by ID ({{range .Dot1X.Profiles}}{{if eq .ID
// $wifi.Dot1XID}}...; render data holds no maps).
type Dot1XInfo struct {
	// Wired is the phone group's wired profile, nil when it names none or
	// the model has no wired 802.1X keys (SupportsWired8021X).
	Wired *Dot1XProfile
	// Profiles holds every 802.1X profile, in ID order.
	Profiles []Dot1XProfile
}

// SupportsSCEP reports whether the built-in template writes SCEP keys for
// the model: a Grandstream WP816/826/836 (WP8x6 family) or a GRP2601/2602/
// 2603/2604 (GRP260x), the two templates that define them.
func SupportsSCEP(v Vendor, model string) bool {
	if v != Grandstream {
		return false
	}
	return GrandstreamFamily(model) == FamilyWP8x6 || grp260xRE.MatchString(normModel(model))
}

// SupportsWired8021X reports whether the built-in template writes wired
// 802.1X keys for the model: the Grandstream GRP26xx desk phones, whose
// three templates define P7901 to P7903.
func SupportsWired8021X(v Vendor, model string) bool {
	return v == Grandstream && grp26RE.MatchString(normModel(model))
}

var (
	grp260xRE = regexp.MustCompile(`^GRP260[1-4]`)
	grp26RE   = regexp.MustCompile(`^GRP26`)
)

// ExpandIdentity fills an identity's placeholders: {mac} the phone's MAC in
// lowercase hex, {MAC} in uppercase.
func ExpandIdentity(identity, mac string) string {
	return strings.NewReplacer("{mac}", strings.ToLower(mac), "{MAC}", strings.ToUpper(mac)).Replace(identity)
}

// identityRE is what an identity may hold: printable ASCII without spaces,
// so it survives every vendor format; placeholders included.
var identityRE = regexp.MustCompile(`^[\x21-\x7e]{1,64}$`)

// NewDot1XProfile resolves a stored profile for a phone: identity expanded,
// vendor values filled, Usable set (scep says whether the phone gets a
// certificate from SCEP, which EAP-TLS needs).
func NewDot1XProfile(id int64, name, method, identity, anonymous, password, caPEM, domain, mac string, scep bool) Dot1XProfile {
	p := Dot1XProfile{ID: id, Name: name, Method: method, Identity: ExpandIdentity(identity, mac),
		AnonymousIdentity: ExpandIdentity(anonymous, mac), Password: password, CACertPEM: strings.TrimSpace(caPEM),
		ServerDomain: domain, Usable: true, GSWifiCAType: 1}
	switch method {
	case Dot1XEAPTLS:
		p.GSWiredMode, p.GSWifiEAP, p.Usable = 2, 1, scep
	case Dot1XPEAP:
		p.GSWiredMode, p.GSWifiEAP = 3, 2
	case Dot1XEAPMD5:
		p.GSWiredMode = 1
	}
	if p.CACertPEM != "" {
		p.GSWifiCAType = 3
	}
	return p
}

// Dot1XField is one validation failure of a profile.
type Dot1XField struct {
	Path    string
	Message string
}

// ValidateDot1X checks a profile's fields. hasPassword says whether the
// profile will hold a password after the change (a new one or the stored
// one kept): PEAP and EAP-MD5 need one, EAP-TLS takes none.
func ValidateDot1X(name, method, identity, anonymous, caPEM, domain string, hasPassword, newPassword bool) []Dot1XField {
	var f []Dot1XField
	add := func(p, m string) { f = append(f, Dot1XField{p, m}) }
	if n := len([]rune(strings.TrimSpace(name))); n < 1 || n > 64 {
		add("name", "must be 1 to 64 characters")
	}
	switch method {
	case Dot1XEAPTLS:
		if newPassword {
			add("password", "EAP-TLS uses the phone's certificate, not a password")
		}
	case Dot1XPEAP, Dot1XEAPMD5:
		if !hasPassword {
			add("password", "is required for "+method)
		}
	default:
		add("method", "must be eap-tls, peap or eap-md5")
	}
	if !identityRE.MatchString(identity) {
		add("identity", "must be 1 to 64 printable characters without spaces ({mac} and {MAC} are filled in)")
	} else if len(ExpandIdentity(identity, "000000000000")) > Dot1XMaxIdentity {
		add("identity", "is longer than 64 characters once {mac} is filled in")
	}
	if anonymous != "" && !identityRE.MatchString(anonymous) {
		add("anonymousIdentity", "must be at most 64 printable characters without spaces")
	}
	if caPEM != "" {
		if err := checkCAPEM(caPEM); err != nil {
			add("caCertPem", err.Error())
		}
	}
	if len(domain) > 253 || strings.ContainsAny(domain, " \t\r\n<>&\"'") {
		add("serverDomain", "must be a host name")
	}
	return f
}

// checkCAPEM accepts one or more PEM certificates and nothing else.
func checkCAPEM(s string) error {
	rest := []byte(strings.TrimSpace(s))
	n := 0
	for len(rest) > 0 {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil || b.Type != "CERTIFICATE" {
			return errors.New("must be PEM certificates only")
		}
		if _, err := x509.ParseCertificate(b.Bytes); err != nil {
			return errors.New("holds a certificate that does not parse")
		}
		n++
		rest = []byte(strings.TrimSpace(string(rest)))
	}
	if n == 0 || n > 8 {
		return errors.New("must hold 1 to 8 PEM certificates")
	}
	return nil
}
