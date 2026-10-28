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
