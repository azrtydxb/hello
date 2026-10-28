package api

import (
	"net/http"
	"strconv"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/store"
)

// The ring map (migration 00051, spec phone-feature-parity S-42 and S-44): a
// tone per ring tag and vendor, written into the phones' provisioned
// configuration. Reads are open to a viewer; changes need the write scope.

// ringVendor is one vendor's ringer list and how many tags its phones map.
type ringVendor struct {
	Vendor string          `json:"vendor"`
	Label  string          `json:"label"`
	Slots  int             `json:"slots"`
	Tones  []prov.RingTone `json:"tones"`
}

// ringMapView is what GET /api/v1/ring-map returns: the vendors' ringer
// lists, the default tone of every tag and vendor, and the explicit choices.
type ringMapView struct {
	Vendors  []ringVendor         `json:"vendors"`
	Defaults []store.RingMapEntry `json:"defaults"`
	store.RingMap
}

var ringVendorLabels = map[prov.Vendor]string{
	prov.Yealink: "Yealink", prov.Poly: "Poly", prov.Grandstream: "Grandstream", prov.Snom: "Snom", prov.Fanvil: "Fanvil",
}

// ringVendorSlots is the most tags a vendor's phones map; a Grandstream WP8x6
// maps the five Bellcore slots only and a model with no known key none.
var ringVendorSlots = map[prov.Vendor]int{
	prov.Yealink: 10, prov.Poly: 23, prov.Grandstream: 10, prov.Snom: 3, prov.Fanvil: 10,
}

func (s *server) getRingMap(w http.ResponseWriter, r *http.Request) {
	tags, err := s.Store.ListRingTags(r.Context())
	if err != nil {
		s.internal(w, "list ring tags", err)
		return
	}
	m, err := s.Store.GetRingMap(r.Context())
	if err != nil {
		s.internal(w, "read ring map", err)
		return
	}
	view := ringMapView{RingMap: m, Vendors: []ringVendor{}, Defaults: []store.RingMapEntry{}}
	for _, v := range prov.RingVendors {
		view.Vendors = append(view.Vendors, ringVendor{Vendor: string(v), Label: ringVendorLabels[v], Slots: ringVendorSlots[v], Tones: prov.RingTones(v)})
		for _, t := range tags {
			view.Defaults = append(view.Defaults, store.RingMapEntry{Tag: t.Name, Vendor: string(v), Tone: prov.DefaultRingTone(v, t.BellcoreSlot)})
		}
	}
	writeJSON(w, http.StatusOK, view)
}

type ringMapBody struct {
	Entries *[]store.RingMapEntry `json:"entries"`
}

// ringMapEntries validates a body: every entry names a tag, a vendor and a
// tone of that vendor's list, and no tag and vendor appears twice.
func ringMapEntries(w http.ResponseWriter, r *http.Request) ([]store.RingMapEntry, bool) {
	var b ringMapBody
	if !decode(w, r, &b) {
		return nil, false
	}
	if b.Entries == nil {
		badRequest(w, "entries is required")
		return nil, false
	}
	var f fieldErrs
	seen := map[[2]string]bool{}
	for i, e := range *b.Entries {
		p := "entries[" + strconv.Itoa(i) + "]"
		v := prov.Vendor(e.Vendor)
		switch {
		case prov.RingTones(v) == nil:
			f.add(p+".vendor", "must be yealink, poly, grandstream, snom or fanvil")
		case !prov.ValidRingTone(v, e.Tone):
			f.add(p+".tone", "is not a ringer of %s", e.Vendor)
		}
		if e.Tag == "" {
			f.add(p+".tag", "is required")
		}
		if seen[[2]string{e.Tag, e.Vendor}] {
			f.add(p, "names the same tag and vendor as an earlier entry")
		}
		seen[[2]string{e.Tag, e.Vendor}] = true
	}
	if len(f) > 0 {
		writeFields(w, f)
		return nil, false
	}
	return *b.Entries, true
}

// putRingMap replaces the global map's choices.
func (s *server) putRingMap(w http.ResponseWriter, r *http.Request) {
	in, ok := ringMapEntries(w, r)
	if !ok {
		return
	}
	m, err := s.Store.ReplaceRingMap(r.Context(), actor(r).String(), nil, in)
	if err != nil {
		s.configError(w, "ring map", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// putRingMapGroup replaces one phone group's overrides; an empty list removes them.
func (s *server) putRingMapGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	in, ok := ringMapEntries(w, r)
	if !ok {
		return
	}
	m, err := s.Store.ReplaceRingMap(r.Context(), actor(r).String(), &id, in)
	if err != nil {
		s.configError(w, "ring map", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}
