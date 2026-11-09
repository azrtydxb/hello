package prov

import (
	"regexp"
	"strings"
)

// Per-model key capabilities and the placement of BLF keys on them.
//
// A phone has a fixed number of programmable keys; a BLF past that number is
// either ignored by the phone or, worse, overwrites another key. PlaceBLF
// puts the keys of a phone on the keys its model has, in order, and drops
// the rest. Hello's own account takes the first key of a Yealink and a Snom
// (the line key), so BLF starts at the second.
//
// Sources, per vendor (the vendor documents are the authority; a count that
// no vendor document gives is marked and kept conservative):
//
//   - Snom: the Snom Service Hub article "Function Key Index Ranges"
//     (service.snom.com), which lists the fkey index range of every desk
//     phone and of its expansion modules. Module 1 of the UXM and of the
//     colour module UXMC start at the same index on a D7, so only module 1's
//     first 18 keys, which both have, are provisioned; modules 2 and 3 differ
//     by module type and Hello cannot tell which is attached.
//   - Fanvil: the datasheets on fanvil.com (service/doc/file): X4U and X5U
//     "6 DSS keys on the extended screen, up to 30 DSS keys via 5 pages",
//     X6U "12 DSS keys on the extended screen, up to 60 DSS keys via 5
//     pages". The page sections of the configuration file are named as in
//     Fanvil's own configuration export.
//   - Yealink: the line key ranges (linekey.X, X up to the count) and the
//     expansion module key ranges of the Yealink configuration templates,
//     cross-checked against a production template set (Nethesis Tancredi,
//     which models T2x to T5x and VP59); the counts are not confirmed in the
//     Yealink Administrator Guide, which could not be fetched. The EXP50
//     (up to 3 per phone) is on the T53, T54, T57 and T58 product pages.
//   - Poly: no verifiable limit. The UC Software Administrator Guide
//     (docs.poly.com, now behind support.hp.com) is a script-rendered page
//     that cannot be fetched, so Poly keeps its unlimited attendant list
//     until a limit can be cited.
//
// A model that is not in a vendor's table is left as it was before the
// table existed: every BLF is written, numbered from the vendor's first
// free key, and the phone ignores keys it does not have.

// keyModel is what a model table says about one phone.
type keyModel struct {
	// keys is the number of programmable keys on the phone body: Yealink
	// linekey.X up to this X, Snom fkey idx 0 to keys-1.
	keys int
	// expModules and expKeys are the Yealink expansion modules the phone
	// takes and the keys of each (expansion_module.X.key.Y, Y up to expKeys).
	expModules, expKeys int
	// expStart is the Snom fkey idx of the first key of expansion module 1
	// (0 for a phone without); expKeys is then the keys of that module.
	expStart int
	// pageSize and pages are the Fanvil DSS key pages.
	pageSize, pages int
}

var (
	yealinkFamilyRE = regexp.MustCompile(`^(T\d\d|VP59)`)
	snomFamilyRE    = regexp.MustCompile(`^D\d{3}`)
	fanvilFamilyRE  = regexp.MustCompile(`^X[456]U`)
)

// yealinkKeys is keyed by the family: the model name without its suffix
// letters (T54W and T54S are the T54). keys counts linekey.1 as well, which
// is the account's own line key.
var yealinkKeys = map[string]keyModel{
	"T19": {keys: 1}, "T21": {keys: 2}, "T23": {keys: 3}, "T27": {keys: 21}, "T29": {keys: 27},
	"T30": {keys: 1}, "T31": {keys: 2}, "T33": {keys: 12}, "T34": {keys: 12},
	"T40": {keys: 3}, "T41": {keys: 15}, "T42": {keys: 15}, "T43": {keys: 21}, "T44": {keys: 21},
	"T46": {keys: 27}, "T48": {keys: 29},
	"T52":  {keys: 21},
	"T53":  {keys: 21, expModules: 3, expKeys: 60},
	"T54":  {keys: 27, expModules: 3, expKeys: 60},
	"T56":  {keys: 27},
	"T57":  {keys: 29, expModules: 3, expKeys: 60},
	"T58":  {keys: 27, expModules: 3, expKeys: 60},
	"VP59": {keys: 27},
}

// snomKeys is the Service Hub's "Function Key Index Ranges" table: keys on
// the phone (all pages) and the index of the first key of expansion module
// 1. A UXM has 18 keys.
var snomKeys = map[string]keyModel{
	"D120": {keys: 2}, "D140": {keys: 2}, "D150": {keys: 2},
	"D315": {keys: 5, expStart: 131, expKeys: 18},
	"D345": {keys: 48, expStart: 174, expKeys: 18},
	"D375": {keys: 12, expStart: 138, expKeys: 18},
	"D385": {keys: 48, expStart: 174, expKeys: 18},
	"D713": {keys: 4},
	"D715": {keys: 5, expStart: 5, expKeys: 18},
	"D717": {keys: 3, expStart: 3, expKeys: 18},
	"D725": {keys: 18, expStart: 18, expKeys: 18},
	"D735": {keys: 32, expStart: 32, expKeys: 18},
	"D745": {keys: 32, expStart: 32, expKeys: 18},
	"D765": {keys: 16, expStart: 16, expKeys: 18},
	"D785": {keys: 24, expStart: 24, expKeys: 18},
	"D862": {keys: 32}, "D865": {keys: 40},
}

// fanvilKeys covers the X-U series, whose DSS keys are on the extended
// screen in pages; their four or five line keys on the main screen are not
// DSS keys of this list.
var fanvilKeys = map[string]keyModel{
	"X4U": {pageSize: 6, pages: 5},
	"X5U": {pageSize: 6, pages: 5},
	"X6U": {pageSize: 12, pages: 5},
}

// fanvilSection is the section name of a Fanvil DSS key page: the file
// carries "--Dsskey Config1--:", "--Dsskey Config2--:" and so on.
const fanvilSection = "Dsskey"

// keyModelName upper-cases a model and drops spaces, dashes and the vendor
// prefix phones add ("SIP-T54W", "snomD785").
func keyModelName(v Vendor, model string) string {
	m := strings.ToUpper(strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_', '.':
			return -1
		}
		return r
	}, model))
	switch v {
	case Yealink:
		m = strings.TrimPrefix(m, "SIP")
	case Snom:
		m = strings.TrimPrefix(m, "SNOM")
	case Fanvil:
		m = strings.TrimPrefix(m, "FANVIL")
	}
	return m
}

// KeyCapacity reports how many BLF keys a phone can take, expansion modules
// included, and whether the model is in the capability table (false: the
// count is meaningless and every BLF is written). Poly has no table.
func KeyCapacity(v Vendor, model string) (n int, known bool) {
	slots, bounded := blfSlots(v, model)
	return len(slots), bounded
}

// blfSlot is one key a BLF can sit on.
type blfSlot struct {
	slot, module, page int
}

// blfSlots lists the keys a BLF can take on the model, in the order they are
// filled. bounded is false for a model not in the table.
func blfSlots(v Vendor, model string) (slots []blfSlot, bounded bool) {
	m := keyModelName(v, model)
	switch v {
	case Yealink:
		km, ok := yealinkKeys[yealinkFamilyRE.FindString(m)]
		if !ok {
			return nil, false
		}
		for k := 2; k <= km.keys; k++ { // linekey.1 is the account
			slots = append(slots, blfSlot{slot: k})
		}
		for mod := 1; mod <= km.expModules; mod++ {
			for k := 1; k <= km.expKeys; k++ {
				slots = append(slots, blfSlot{slot: k, module: mod})
			}
		}
		return slots, true
	case Snom:
		km, ok := snomKeys[snomFamilyRE.FindString(m)]
		if !ok {
			return nil, false
		}
		for idx := 1; idx < km.keys; idx++ { // fkey 0 is the account's line key
			slots = append(slots, blfSlot{slot: idx})
		}
		if km.expStart > 0 {
			for k := range km.expKeys {
				slots = append(slots, blfSlot{slot: km.expStart + k})
			}
		}
		return slots, true
	case Fanvil:
		km, ok := fanvilKeys[fanvilFamilyRE.FindString(m)]
		if !ok {
			return nil, false
		}
		for p := 1; p <= km.pages; p++ {
			for k := 1; k <= km.pageSize; k++ {
				slots = append(slots, blfSlot{slot: k, page: p})
			}
		}
		return slots, true
	}
	return nil, false
}

// unboundedSlot is key i (0-based) of a model with no table: where the
// vendor's first free key is.
func unboundedSlot(v Vendor, i int) blfSlot {
	switch v {
	case Yealink:
		return blfSlot{slot: i + 2}
	case Fanvil:
		return blfSlot{slot: i + 1, page: 1}
	}
	return blfSlot{slot: i + 1}
}

// PlaceBLF returns keys with their placement filled in for the model: each
// key on the next key the model has, keys past the last dropped, and the
// directed-pickup code set on the Yealink keys of the phone body. The input
// is not changed.
func PlaceBLF(v Vendor, model string, keys []BLFKey, pickup string) []BLFKey {
	if len(keys) == 0 {
		return keys
	}
	slots, bounded := blfSlots(v, model)
	out := make([]BLFKey, 0, len(keys))
	prevPage := 0
	for i, k := range keys {
		var s blfSlot
		switch {
		case bounded && i < len(slots):
			s = slots[i]
		case bounded:
			return out
		default:
			s = unboundedSlot(v, i)
		}
		k.Slot, k.Module, k.Page = s.slot, s.module, s.page
		if v == Fanvil {
			k.Section = fanvilSection
			k.PageStart = s.page != prevPage
			prevPage = s.page
		}
		if v == Yealink && s.module == 0 {
			k.Pickup = pickup
		}
		out = append(out, k)
	}
	return out
}

// vmKeyLabel is what the transfer-to-voicemail key shows on the phone.
const vmKeyLabel = "To voicemail"

// SupportsVMKey reports whether Hello can provision a transfer-to-voicemail
// key for the model: a phone that has programmable keys (or, on Poly,
// softkeys) Hello writes. Yealink, Snom and Fanvil only on a model in the
// capability table (an unknown model might have no key to spare and Hello
// cannot tell, as it cannot with BLF); Poly on
// every model (a softkey). Grandstream has none: a WP has no key block in its
// templates, and the P323 block of the other models is unverified and its
// numbers (three to a key) run into the phonebook keys P330 to P333 past two
// BLF keys, so no key is added there. Whether the model has a key left after the BLF
// keys is PlaceVMKey's question.
func SupportsVMKey(v Vendor, model string) bool {
	switch v {
	case Yealink, Snom, Fanvil:
		_, bounded := blfSlots(v, model)
		return bounded
	case Poly:
		return true
	}
	return false
}

// PlaceVMKey returns the transfer-to-voicemail key of a phone whose own
// extension is ext, or nil when there is none: no voicemail code or extension,
// a model without support (SupportsVMKey), or (Yealink, Snom, Fanvil) no key
// left. The key takes the key after the nBLF BLF keys that PlaceBLF placed,
// so it never displaces a BLF; a phone whose keys the BLF keys fill gets
// none. domain is the SIP domain, for the vendors whose key takes a URI.
func PlaceVMKey(v Vendor, model string, nBLF int, code, ext, domain string) *VMKey {
	if code == "" || ext == "" || !SupportsVMKey(v, model) {
		return nil
	}
	k := &VMKey{Number: code + ext, Label: vmKeyLabel}
	k.URI = "sip:" + k.Number + "@" + domain
	switch v {
	case Yealink, Snom, Fanvil:
		slots, _ := blfSlots(v, model)
		if nBLF >= len(slots) {
			return nil
		}
		s := slots[nBLF]
		k.Slot, k.Module, k.Page = s.slot, s.module, s.page
		if v == Fanvil {
			k.Section = fanvilSection
			k.PageStart = nBLF == 0 || slots[nBLF-1].page != s.page
		}
	}
	return k
}

// FeatureCodes are the star codes a phone's own DND and forward keys dial
// (Snom, Fanvil): the PBX's feature_codes by action, empty where the PBX has
// none. Forward codes are dialled followed by the target number.
type FeatureCodes struct {
	DNDOn, DNDOff                         string
	ForwardOn, ForwardOff                 string // unconditional
	ForwardBusyOn, ForwardBusyOff         string
	ForwardNoAnswerOn, ForwardNoAnswerOff string
	// PickupDirected is the directed-pickup code, dialled followed by the
	// ringing extension's number.
	PickupDirected string
}

// FeatureCodeRow is one feature_codes row.
type FeatureCodeRow struct {
	Code, Action, Argument string
}

// NewFeatureCodes picks the codes out of the PBX's rows. When two rows share
// an action the lower code wins, so the result does not depend on row order.
func NewFeatureCodes(rows []FeatureCodeRow) FeatureCodes {
	var c FeatureCodes
	set := func(dst *string, code string) {
		if *dst == "" || code < *dst {
			*dst = code
		}
	}
	for _, r := range rows {
		on := r.Argument == "set"
		switch r.Action {
		case "dnd_on":
			set(&c.DNDOn, r.Code)
		case "dnd_off":
			set(&c.DNDOff, r.Code)
		case "forward_always":
			if on {
				set(&c.ForwardOn, r.Code)
			} else if r.Argument == "clear" {
				set(&c.ForwardOff, r.Code)
			}
		case "forward_busy":
			if on {
				set(&c.ForwardBusyOn, r.Code)
			} else if r.Argument == "clear" {
				set(&c.ForwardBusyOff, r.Code)
			}
		case "forward_no_answer":
			if on {
				set(&c.ForwardNoAnswerOn, r.Code)
			} else if r.Argument == "clear" {
				set(&c.ForwardNoAnswerOff, r.Code)
			}
		case "pickup_directed":
			set(&c.PickupDirected, r.Code)
		}
	}
	return c
}

// pemBase64 is a PEM certificate's base64 text without the BEGIN and END
// lines and without line breaks; empty for an empty or unarmoured input.
func pemBase64(pem string) string {
	var b strings.Builder
	in := false
	for line := range strings.SplitSeq(pem, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "-----BEGIN"):
			in = true
		case strings.HasPrefix(line, "-----END"):
			return b.String()
		case in:
			b.WriteString(line)
		}
	}
	return ""
}
