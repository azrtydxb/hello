package prov

// SupportsSessionTimers reports whether Hello requests RFC 4028 session
// timers (Session-Expires, Supported: timer) on a call leg to a phone of the
// vendor and model (phone-feature-parity S-9). Every first-class vendor's
// current desk and Wi-Fi handsets implement RFC 4028 (Grandstream WP8x6 and
// WP822 per their administration guides, the others by their published SIP
// feature lists); none is confirmed on hardware yet (lab item 8). A phone
// Hello cannot identify (Generic, or a User-Agent that names no vendor)
// gets no request, so an unusual handset never sees a header it may reject;
// it still gets Hello's refresh when it answers a refresh-less call, because
// Hello refreshes as the refresher whenever the far end does not.
func SupportsSessionTimers(v Vendor, _ string) bool {
	switch v {
	case Grandstream:
		// A GXP or GRP desk phone has the feature too; the WP families are
		// the ones the lab covers, so an unknown Grandstream model counts.
		return true
	case Yealink, Poly, Snom, Fanvil:
		return true
	}
	return false
}
