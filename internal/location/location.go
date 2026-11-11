// Package location models where phones are (spec phone-feature-parity S-28
// c): a tree of sites, floors and rooms, resolved to the civic address and
// optional geo point of one phone, and rendered as PIDF-LO (RFC 4119, RFC
// 5491) for the HELD server (RFC 5985) and for the Geolocation header of an
// emergency call (RFC 6442). It is pure: no database, no clock.
package location

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Kinds of a location node. A site carries the civic address; a floor and a
// room only name a place inside it.
const (
	KindSite  = "site"
	KindFloor = "floor"
	KindRoom  = "room"
)

// Limits.
const (
	MaxNameLen  = 100
	MaxFieldLen = 100
	MaxDepth    = 3
)

// Node is one stored location. Floors have a site for parent, rooms a floor
// (or a site). The civic fields are only used by a site.
type Node struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parentId"`

	// Country is an ISO 3166 alpha-2 code (civic element country).
	Country string `json:"country"`
	// State, City, Street, HouseNumber and PostalCode are the civic
	// elements A1, A3, A6, HNO and PC of RFC 4119.
	State       string `json:"state"`
	City        string `json:"city"`
	Street      string `json:"street"`
	HouseNumber string `json:"houseNumber"`
	PostalCode  string `json:"postalCode"`

	// Latitude and Longitude (WGS 84 degrees) and RadiusM (metres) are an
	// optional geo point; a floor or room may carry a more exact one than
	// its site. RadiusM is the uncertainty and may be unset.
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	RadiusM   *float64 `json:"radiusM"`
}

// Place is a resolved location: the civic address of the site with the
// floor and room named, and the most specific geo point on the path.
type Place struct {
	// Path names the nodes from the site down, "HQ / 2 / Server room".
	Path string `json:"path"`

	Country     string `json:"country"`
	State       string `json:"state"`
	City        string `json:"city"`
	Street      string `json:"street"`
	HouseNumber string `json:"houseNumber"`
	PostalCode  string `json:"postalCode"`
	// Floor and Room are the names of the floor and room nodes on the path
	// (civic elements FLR and ROOM).
	Floor string `json:"floor"`
	Room  string `json:"room"`

	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	RadiusM   *float64 `json:"radiusM"`
}

// HasCivic reports whether the place has a civic address to send.
func (p Place) HasCivic() bool {
	return p.Country != "" || p.City != "" || p.Street != "" || p.PostalCode != ""
}

// HasGeo reports whether the place has a geo point.
func (p Place) HasGeo() bool { return p.Latitude != nil && p.Longitude != nil }

// Usable reports whether the place carries anything a LIS can answer with.
func (p Place) Usable() bool { return p.HasCivic() || p.HasGeo() }

// Resolve walks from node id up to its site and returns the place. ok is
// false when id is not in nodes or the parent chain is broken or loops.
func Resolve(nodes map[int64]Node, id int64) (Place, bool) {
	var chain []Node
	seen := map[int64]bool{}
	for cur := id; ; {
		n, ok := nodes[cur]
		if !ok || seen[cur] {
			return Place{}, false
		}
		seen[cur] = true
		chain = append(chain, n)
		if n.ParentID == nil {
			break
		}
		cur = *n.ParentID
	}
	// chain runs from the node up; build the place from the root down.
	var p Place
	var names []string
	for i := len(chain) - 1; i >= 0; i-- {
		n := chain[i]
		names = append(names, n.Name)
		switch n.Kind {
		case KindSite:
			p.Country, p.State, p.City = n.Country, n.State, n.City
			p.Street, p.HouseNumber, p.PostalCode = n.Street, n.HouseNumber, n.PostalCode
		case KindFloor:
			p.Floor = n.Name
		case KindRoom:
			p.Room = n.Name
		}
		if n.Latitude != nil && n.Longitude != nil {
			p.Latitude, p.Longitude, p.RadiusM = n.Latitude, n.Longitude, n.RadiusM
		}
	}
	p.Path = strings.Join(names, " / ")
	return p, true
}

// Pick returns the first assigned location of a phone, then its extension,
// then its phone group (the most specific wins). 0 means none.
func Pick(phone, extension, group int64) (id int64, source string) {
	switch {
	case phone > 0:
		return phone, "phone"
	case extension > 0:
		return extension, "extension"
	case group > 0:
		return group, "group"
	}
	return 0, ""
}

var countryRE = regexp.MustCompile(`^[A-Z]{2}$`)

// Problem is one invalid field.
type Problem struct{ Path, Msg string }

// Validate checks a node on its own; the parent rules (a floor under a site,
// a room under a floor or a site, depth) are checked against the stored tree
// by ValidateParent.
func Validate(n Node) []Problem {
	var ps []Problem
	add := func(path, msg string) { ps = append(ps, Problem{path, msg}) }
	switch n.Kind {
	case KindSite, KindFloor, KindRoom:
	default:
		add("kind", "must be site, floor or room")
	}
	if l := len([]rune(strings.TrimSpace(n.Name))); l < 1 || l > MaxNameLen {
		add("name", fmt.Sprintf("must be 1 to %d characters", MaxNameLen))
	}
	if strings.ContainsAny(n.Name, "<>&\"'\x00\r\n") {
		add("name", "must not contain <, >, &, quotes or control characters")
	}
	civic := map[string]string{"state": n.State, "city": n.City, "street": n.Street, "houseNumber": n.HouseNumber, "postalCode": n.PostalCode}
	for k, v := range civic {
		if len([]rune(v)) > MaxFieldLen {
			add(k, fmt.Sprintf("must be at most %d characters", MaxFieldLen))
		}
		if strings.ContainsAny(v, "\x00\r\n") {
			add(k, "must not contain control characters")
		}
		if n.Kind != KindSite && v != "" {
			add(k, "only a site has a civic address")
		}
	}
	if n.Country != "" && !countryRE.MatchString(n.Country) {
		add("country", "must be a two-letter ISO 3166 code in capitals")
	}
	if n.Kind != KindSite && n.Country != "" {
		add("country", "only a site has a civic address")
	}
	if (n.Latitude == nil) != (n.Longitude == nil) {
		add("latitude", "latitude and longitude are set together")
	}
	if n.Latitude != nil && (math.IsNaN(*n.Latitude) || *n.Latitude < -90 || *n.Latitude > 90) {
		add("latitude", "must be between -90 and 90")
	}
	if n.Longitude != nil && (math.IsNaN(*n.Longitude) || *n.Longitude < -180 || *n.Longitude > 180) {
		add("longitude", "must be between -180 and 180")
	}
	if n.RadiusM != nil {
		if n.Latitude == nil {
			add("radiusM", "needs a latitude and longitude")
		} else if math.IsNaN(*n.RadiusM) || *n.RadiusM <= 0 || *n.RadiusM > 1e6 {
			add("radiusM", "must be above 0 and at most 1000000 metres")
		}
	}
	if n.Kind == KindSite {
		if n.ParentID != nil {
			add("parentId", "a site has no parent")
		}
		if n.Country == "" && n.City == "" && n.Street == "" && n.PostalCode == "" && n.Latitude == nil {
			add("country", "a site needs a civic address or a geo point")
		}
	}
	return ps
}

// ValidateParent checks the parent of n against the tree: a floor sits under
// a site, a room under a floor or a site, and a node is not its own
// ancestor. nodes is the stored tree (without the node being changed).
func ValidateParent(nodes map[int64]Node, n Node) []Problem {
	if n.Kind == KindSite {
		return nil
	}
	if n.ParentID == nil {
		return []Problem{{"parentId", "a " + n.Kind + " needs a parent"}}
	}
	p, ok := nodes[*n.ParentID]
	if !ok {
		return []Problem{{"parentId", "no such location"}}
	}
	switch {
	case n.Kind == KindFloor && p.Kind != KindSite:
		return []Problem{{"parentId", "a floor belongs to a site"}}
	case n.Kind == KindRoom && p.Kind != KindFloor && p.Kind != KindSite:
		return []Problem{{"parentId", "a room belongs to a floor or a site"}}
	}
	if n.ID != 0 {
		for cur, i := p, 0; ; i++ {
			if cur.ID == n.ID || i > MaxDepth {
				return []Problem{{"parentId", "a location cannot be inside itself"}}
			}
			if cur.ParentID == nil {
				break
			}
			next, ok := nodes[*cur.ParentID]
			if !ok {
				break
			}
			cur = next
		}
	}
	return nil
}
