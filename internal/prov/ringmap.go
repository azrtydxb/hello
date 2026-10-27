package prov

import (
	"slices"
	"strconv"
	"strings"
)

// The ring map (phone-feature-parity S-42 and S-44): a tone per ring tag and
// vendor, written into the phone's configuration as the vendor's
// distinctive-ring rules, so a phone that gets Alert-Info
// <http://127.0.0.1/Bellcore-drN>;info=hello-<tag> plays the tone the admin
// chose for the tag. Every key and value is from the vendor's administrator
// guide and unverified on hardware (spec "Vendor reference: distinctive
// ringing", verify).

// RingSilent is the tone id that means no ring (visual only), offered by
// the vendors whose ringer list has it.
const RingSilent = "silent"

// RingTone is one entry of a vendor's built-in ringer list.
type RingTone struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Silent bool   `json:"silent"`
}

// ringIDs are the built-in ringer ids per vendor, in display order. The id
// is what the ring map stores; nativeTone turns it into the value the
// vendor's key takes.
var ringTones = func() map[Vendor][]RingTone {
	seq := func(prefix, label string, from, to int) []RingTone {
		var out []RingTone
		for i := from; i <= to; i++ {
			n := strconv.Itoa(i)
			out = append(out, RingTone{ID: prefix + n, Label: label + " " + n})
		}
		return out
	}
	silent := RingTone{ID: RingSilent, Label: "Silent (visual only)", Silent: true}
	return map[Vendor][]RingTone{
		// Yealink ring types 1 to 8 and the five Bellcore ringers.
		Yealink: append(seq("", "Ring", 1, 8), seq("bellcore", "Bellcore ring", 1, 5)...),
		Poly:    append(seq("ringer", "Ringer", 1, 24), silent),
		// Grandstream: the system ring tone and the three custom ring tones
		// the phone holds. Hello produces no Grandstream tone file yet, so a
		// custom one is whatever the phone already has.
		Grandstream: {{ID: "system", Label: "System ring tone"}, {ID: "1", Label: "Custom ring tone 1"},
			{ID: "2", Label: "Custom ring tone 2"}, {ID: "3", Label: "Custom ring tone 3"}},
		Snom:   append(seq("Ringer", "Ringer", 1, 10), RingTone{ID: "Silent", Label: "Silent (visual only)", Silent: true}),
		Fanvil: seq("", "Type", 1, 9),
	}
}()

// RingVendors lists the vendors that have a ring map, in display order.
var RingVendors = []Vendor{Yealink, Poly, Grandstream, Snom, Fanvil}

// RingTones returns a vendor's built-in ringer list (a copy), nil for an
// unknown vendor.
func RingTones(v Vendor) []RingTone { return slices.Clone(ringTones[v]) }

// ValidRingTone reports whether id is in the vendor's ringer list.
func ValidRingTone(v Vendor, id string) bool {
	return slices.ContainsFunc(ringTones[v], func(t RingTone) bool { return t.ID == id })
}

// RingSlots is how many tags a phone of the vendor can map to a tone, 0 for
// a model whose key is not known (a Grandstream WP856 or desk phone).
func RingSlots(v Vendor, model string) int {
	switch v {
	case Yealink, Fanvil:
		return 10
	case Poly:
		return 23 // 24 classes, one used by the auto-answer rule
	case Snom:
		return 3 // internal, external and group
	case Grandstream:
		switch GrandstreamFamily(model) {
		case FamilyWP8x6:
			return 5 // the five Bellcore slots
		case FamilyWP8xx:
			return 10
		}
	}
	return 0
}

// DefaultRingTone is the tone a vendor plays for a tag whose Bellcore slot
// is slot when the admin set none: the vendor's ringers in order, so
// nothing has to be configured. Grandstream plays its system tone for every
// tag, because a custom tone exists only if the phone holds one.
func DefaultRingTone(v Vendor, slot int) string {
	if slot < 1 || slot > 5 {
		slot = 1
	}
	n := strconv.Itoa(slot)
	switch v {
	case Yealink, Fanvil:
		return n
	case Poly:
		return "ringer" + strconv.Itoa(slot+1) // ringer2 is the phone's own default
	case Snom:
		return "Ringer" + n
	}
	return "system"
}

// nativeTone is the value the vendor's tone key takes for a tone id.
func nativeTone(v Vendor, id string) string {
	switch v {
	case Yealink:
		if n, ok := strings.CutPrefix(id, "bellcore"); ok {
			k, _ := strconv.Atoi(n)
			return strconv.Itoa(8 + k) // the Bellcore ringers follow Ring8 (verify)
		}
	case Grandstream:
		if id == "system" {
			return "0"
		}
	}
	return id
}

// lineTone is the value of the vendor's line ring-tone key for a tone id,
// "" where the key has no value for it. Yealink's takes the sound file
// (Ring1.wav), which the Bellcore ringers do not have a known name for.
func lineTone(v Vendor, id string) string {
	if v == Yealink {
		if strings.HasPrefix(id, "bellcore") {
			return ""
		}
		return "Ring" + id + ".wav"
	}
	return nativeTone(v, id)
}

// RingTag is a ring tag as the ring map sees it.
type RingTag struct {
	Name string
	Slot int // Bellcore slot, 1 to 5
}

// RingRule is one distinctive-ring rule of a phone's configuration.
type RingRule struct {
	// N is the vendor's own rule number (Poly's first rule is 2, because 1
	// is the auto-answer rule).
	N    int
	Tag  string
	Slot int
	// Text is what the rule matches in the Alert-Info header, URL the
	// Bellcore URL of the tag's slot. Class is Snom's class (internal,
	// external or group) or Poly's ring class (custom<n>).
	Text, URL, Class string
	// Tone is the vendor's native value for the tone; Silent is set for no ring.
	Tone   string
	Silent bool
}

// RingInfo is the ring map a built-in template renders for one phone.
type RingInfo struct {
	// Rules are the distinctive-ring rules, longest match text first so a
	// tag that is the start of another (door, doorbell) cannot shadow it.
	Rules []RingRule
	// Default is the tone of the line when no rule matches: the internal
	// tag's tone. Empty where the model has no key for it.
	Default string
	// Bellcore is the five tones of Bellcore-dr1 to dr5, comma separated
	// (Grandstream WP8x6); empty for other phones.
	Bellcore string
}

// NewRingInfo resolves the ring map of one phone. tags are in priority order
// (built-ins first); tones holds the admin's choices for this vendor, already
// merged group over global, by tag. A tag without a valid choice gets the
// default for its Bellcore slot. Tags beyond the model's slot count are
// dropped.
func NewRingInfo(v Vendor, model string, tags []RingTag, tones map[string]string) RingInfo {
	var info RingInfo
	slots := RingSlots(v, model)
	if slots == 0 {
		return info
	}
	toneOf := func(t RingTag) string {
		if id, ok := tones[t.Name]; ok && ValidRingTone(v, id) {
			return id
		}
		return DefaultRingTone(v, t.Slot)
	}
	silent := func(id string) bool {
		return slices.ContainsFunc(ringTones[v], func(t RingTone) bool { return t.ID == id && t.Silent })
	}
	url := func(slot int) string { return "http://127.0.0.1/Bellcore-dr" + strconv.Itoa(slot) }
	family := ""
	if v == Grandstream {
		family = GrandstreamFamily(model)
	}

	for _, t := range tags {
		if t.Name == "internal" {
			if v != Grandstream || family == FamilyWP8xx {
				info.Default = lineTone(v, toneOf(t))
			}
			break
		}
	}

	switch {
	case v == Snom:
		for _, c := range []struct{ tag, class, text string }{
			{"internal", "internal", "alert-internal"},
			{"external", "external", "alert-external"},
			{"ringgroup", "group", "alert-group"},
		} {
			i := slices.IndexFunc(tags, func(t RingTag) bool { return t.Name == c.tag })
			if i < 0 {
				continue
			}
			id := toneOf(tags[i])
			info.Rules = append(info.Rules, RingRule{N: len(info.Rules) + 1, Tag: c.tag, Slot: tags[i].Slot, Text: c.text,
				URL: url(tags[i].Slot), Class: c.class, Tone: nativeTone(v, id), Silent: silent(id)})
		}
	case v == Grandstream && family == FamilyWP8x6:
		// One tone per Bellcore slot; the first tag of a slot decides it.
		var list []string
		for slot := 1; slot <= 5; slot++ {
			id := DefaultRingTone(v, slot)
			if i := slices.IndexFunc(tags, func(t RingTag) bool { return t.Slot == slot }); i >= 0 {
				id = toneOf(tags[i])
			}
			list = append(list, nativeTone(v, id))
		}
		info.Bellcore = strings.Join(list, ",")
	default:
		first := 1
		if v == Poly {
			first = 2
		}
		for _, t := range tags {
			if len(info.Rules) >= slots {
				break
			}
			id := toneOf(t)
			n := first + len(info.Rules)
			r := RingRule{N: n, Tag: t.Name, Slot: t.Slot, Text: "hello-" + t.Name, URL: url(t.Slot),
				Tone: nativeTone(v, id), Silent: silent(id)}
			if v == Poly {
				r.Class = "custom" + strconv.Itoa(n-1)
			}
			info.Rules = append(info.Rules, r)
		}
		// Number the rules after ordering them, longest text first.
		slices.SortStableFunc(info.Rules, func(a, b RingRule) int { return len(b.Text) - len(a.Text) })
		for i := range info.Rules {
			info.Rules[i].N = first + i
			if v == Poly {
				info.Rules[i].Class = "custom" + strconv.Itoa(i+1)
			}
		}
	}
	return info
}
