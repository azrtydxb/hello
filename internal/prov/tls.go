package prov

import "strings"

// TLSVendor reports whether the vendor's built-in template carries a SIP
// transport key (Grandstream P130, Yealink transport_type, Poly
// reg.1.server.1.transport, Fanvil SIP1 Transport). Snom's key is
// unconfirmed (verify), so a Snom phone always registers over UDP.
func TLSVendor(v Vendor) bool {
	switch v {
	case Grandstream, Yealink, Poly, Fanvil:
		return true
	}
	return false
}

// TLSModelMatch reports whether a phone of vendor and model is selected by
// a HELLO_PROV_SIP_TLS_MODELS list: entries are "vendor" (every model of
// it) or "vendor:prefix" (models whose name starts with prefix, ignoring
// case), comma separated.
func TLSModelMatch(list []string, v Vendor, model string) bool {
	if !TLSVendor(v) {
		return false
	}
	for _, e := range list {
		name, prefix, hasPrefix := strings.Cut(strings.TrimSpace(e), ":")
		if !strings.EqualFold(name, string(v)) {
			continue
		}
		if !hasPrefix || strings.HasPrefix(strings.ToLower(model), strings.ToLower(strings.TrimSpace(prefix))) {
			return true
		}
	}
	return false
}

// GrandstreamDTLSKey reports whether a Grandstream model has the SRTP Key
// Exchange setting (P60078: 0 SDES, 1 DTLS) in the vendor's config templates:
// the WP8x6 family (wp8x6_config 1.0.3.35 and later, wp856_config 1.0.3.16).
func GrandstreamDTLSKey(v Vendor, model string) bool {
	return v == Grandstream && modelHasPrefix(model, "WP826", "WP836", "WP856")
}

// GrandstreamICEKey reports whether a Grandstream model has the Media NAT
// Traversal setting (P95002: 0 No, 1 Auto, 2 STUN, 3 TURN, 4 ICE) in the
// vendor's config templates: WP826 and WP836 (wp8x6_config 1.0.3.35 and
// later). The WP856 template (1.0.3.16) does not list it.
func GrandstreamICEKey(v Vendor, model string) bool {
	return v == Grandstream && modelHasPrefix(model, "WP826", "WP836")
}

func modelHasPrefix(model string, prefixes ...string) bool {
	m := strings.ToUpper(strings.TrimSpace(model))
	for _, p := range prefixes {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}
