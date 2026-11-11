package location

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }
func id(v int64) *int64    { return &v }

func tree() map[int64]Node {
	return map[int64]Node{
		1: {ID: 1, Kind: KindSite, Name: "HQ", Country: "NL", State: "Noord-Holland", City: "Amsterdam", Street: "Damrak", HouseNumber: "1", PostalCode: "1012LG", Latitude: f(52.3), Longitude: f(4.9), RadiusM: f(50)},
		2: {ID: 2, Kind: KindFloor, Name: "3", ParentID: id(1)},
		3: {ID: 3, Kind: KindRoom, Name: "Server <room>", ParentID: id(2), Latitude: f(52.31), Longitude: f(4.91)},
	}
}

func TestResolveInheritsAddressAndNarrowsGeo(t *testing.T) {
	p, ok := Resolve(tree(), 3)
	if !ok {
		t.Fatal("room did not resolve")
	}
	if p.City != "Amsterdam" || p.Street != "Damrak" || p.Floor != "3" || p.Room != "Server <room>" {
		t.Fatalf("place = %+v", p)
	}
	if p.Path != "HQ / 3 / Server <room>" {
		t.Fatalf("path = %q", p.Path)
	}
	// The room's own point wins and, with no radius of its own, drops the site's.
	if *p.Latitude != 52.31 || p.RadiusM != nil {
		t.Fatalf("geo = %v %v %v", p.Latitude, p.Longitude, p.RadiusM)
	}
	fl, _ := Resolve(tree(), 2)
	if *fl.Latitude != 52.3 || *fl.RadiusM != 50 || fl.Room != "" {
		t.Fatalf("floor place = %+v", fl)
	}
}

func TestResolveBrokenChains(t *testing.T) {
	if _, ok := Resolve(tree(), 99); ok {
		t.Error("unknown id resolved")
	}
	loop := map[int64]Node{1: {ID: 1, Kind: KindFloor, ParentID: id(2)}, 2: {ID: 2, Kind: KindFloor, ParentID: id(1)}}
	if _, ok := Resolve(loop, 1); ok {
		t.Error("a loop resolved")
	}
	orphan := map[int64]Node{2: {ID: 2, Kind: KindFloor, ParentID: id(7)}}
	if _, ok := Resolve(orphan, 2); ok {
		t.Error("an orphan resolved")
	}
}

func TestPickMostSpecificWins(t *testing.T) {
	for _, c := range []struct {
		phone, ext, group int64
		want              int64
		src               string
	}{
		{5, 6, 7, 5, "phone"}, {0, 6, 7, 6, "extension"}, {0, 0, 7, 7, "group"}, {0, 0, 0, 0, ""},
	} {
		got, src := Pick(c.phone, c.ext, c.group)
		if got != c.want || src != c.src {
			t.Errorf("Pick(%d,%d,%d) = %d %q, want %d %q", c.phone, c.ext, c.group, got, src, c.want, c.src)
		}
	}
}

func TestValidate(t *testing.T) {
	good := Node{Kind: KindSite, Name: "HQ", Country: "NL", City: "Amsterdam"}
	if ps := Validate(good); len(ps) != 0 {
		t.Fatalf("good site: %v", ps)
	}
	for name, n := range map[string]Node{
		"empty name":      {Kind: KindSite, Name: " ", Country: "NL"},
		"markup in name":  {Kind: KindSite, Name: "a<b", Country: "NL"},
		"bad kind":        {Kind: "wing", Name: "x"},
		"lower country":   {Kind: KindSite, Name: "x", Country: "nl"},
		"site no address": {Kind: KindSite, Name: "x"},
		"lat only":        {Kind: KindSite, Name: "x", Country: "NL", Latitude: f(1)},
		"lat range":       {Kind: KindSite, Name: "x", Latitude: f(91), Longitude: f(0)},
		"lon range":       {Kind: KindSite, Name: "x", Latitude: f(0), Longitude: f(181)},
		"radius no point": {Kind: KindSite, Name: "x", Country: "NL", RadiusM: f(5)},
		"radius zero":     {Kind: KindSite, Name: "x", Latitude: f(1), Longitude: f(1), RadiusM: f(0)},
		"floor address":   {Kind: KindFloor, Name: "2", ParentID: id(1), City: "x"},
		"site parent":     {Kind: KindSite, Name: "x", Country: "NL", ParentID: id(1)},
		"long street":     {Kind: KindSite, Name: "x", Country: "NL", Street: strings.Repeat("a", 101)},
	} {
		if ps := Validate(n); len(ps) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateParent(t *testing.T) {
	nodes := tree()
	for name, c := range map[string]struct {
		n  Node
		ok bool
	}{
		"floor under site":      {Node{Kind: KindFloor, ParentID: id(1)}, true},
		"room under floor":      {Node{Kind: KindRoom, ParentID: id(2)}, true},
		"room under site":       {Node{Kind: KindRoom, ParentID: id(1)}, true},
		"floor without parent":  {Node{Kind: KindFloor}, false},
		"floor under floor":     {Node{Kind: KindFloor, ParentID: id(2)}, false},
		"room under room":       {Node{Kind: KindRoom, ParentID: id(3)}, false},
		"unknown parent":        {Node{Kind: KindFloor, ParentID: id(42)}, false},
		"site needs no parent":  {Node{Kind: KindSite}, true},
		"floor moved into room": {Node{ID: 2, Kind: KindFloor, ParentID: id(3)}, false},
	} {
		if got := len(ValidateParent(nodes, c.n)) == 0; got != c.ok {
			t.Errorf("%s: ok = %v, want %v", name, got, c.ok)
		}
	}
}

func TestPIDFLODocument(t *testing.T) {
	p, _ := Resolve(tree(), 2)
	doc := PIDFLO(p, "pres:101@pbx.example", time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC))
	var v struct {
		Entity string `xml:"entity,attr"`
		Civic  struct {
			Country string `xml:"country"`
			A3      string `xml:"A3"`
			A6      string `xml:"A6"`
			HNO     string `xml:"HNO"`
			FLR     string `xml:"FLR"`
			PC      string `xml:"PC"`
		} `xml:"tuple>status>geopriv>location-info>civicAddress"`
		Radius string `xml:"tuple>status>geopriv>location-info>Circle>radius"`
		Pos    string `xml:"tuple>status>geopriv>location-info>Circle>pos"`
	}
	if err := xml.Unmarshal(doc, &v); err != nil {
		t.Fatalf("not well-formed: %v\n%s", err, doc)
	}
	if v.Entity != "pres:101@pbx.example" || v.Civic.Country != "NL" || v.Civic.A3 != "Amsterdam" ||
		v.Civic.A6 != "Damrak" || v.Civic.HNO != "1" || v.Civic.FLR != "3" || v.Civic.PC != "1012LG" {
		t.Fatalf("civic = %+v entity %q", v.Civic, v.Entity)
	}
	if v.Pos != "52.3 4.9" || v.Radius != "50" {
		t.Fatalf("geo = %q %q", v.Pos, v.Radius)
	}
	if !strings.Contains(string(doc), "<timestamp>2026-11-09T08:00:00Z</timestamp>") {
		t.Fatalf("no timestamp:\n%s", doc)
	}
}

func TestPIDFLOEscapesAndPointWithoutRadius(t *testing.T) {
	p, _ := Resolve(tree(), 3)
	doc := string(PIDFLO(p, `pres:"a"&b`, time.Now()))
	if strings.Contains(doc, "<room>") || !strings.Contains(doc, "Server &lt;room&gt;") {
		t.Fatalf("room not escaped:\n%s", doc)
	}
	if strings.Contains(doc, `entity="pres:"a"`) || !strings.Contains(doc, "&amp;b") {
		t.Fatalf("entity not escaped:\n%s", doc)
	}
	if !strings.Contains(doc, "<gml:Point") || strings.Contains(doc, "Circle") {
		t.Fatalf("expected a point without radius:\n%s", doc)
	}
}

func TestHELDResponseAndError(t *testing.T) {
	p, _ := Resolve(tree(), 1)
	var v struct {
		XMLName xml.Name
		City    string `xml:"presence>tuple>status>geopriv>location-info>civicAddress>A3"`
	}
	if err := xml.Unmarshal(HELDResponse(p, "pres:x@y", time.Now()), &v); err != nil {
		t.Fatal(err)
	}
	if v.XMLName.Space != NSHeld || v.XMLName.Local != "locationResponse" || v.City != "Amsterdam" {
		t.Fatalf("response = %+v", v)
	}
	var e struct {
		XMLName xml.Name
		Code    string `xml:"code,attr"`
		Msg     string `xml:"message"`
	}
	if err := xml.Unmarshal(HELDError(ErrLocationUnavailable, "no <location>"), &e); err != nil {
		t.Fatal(err)
	}
	if e.XMLName.Local != "error" || e.Code != "locationUnavailable" || e.Msg != "no <location>" {
		t.Fatalf("error = %+v", e)
	}
}

func TestParseRequest(t *testing.T) {
	const hdr = `<?xml version="1.0"?>`
	r, err := ParseRequest([]byte(hdr + `<locationRequest xmlns="` + NSHeld + `"><locationType exact="true">civic geodetic</locationType></locationRequest>`))
	if err != nil || !r.Exact || len(r.Types) != 2 || r.Types[0] != "civic" {
		t.Fatalf("request = %+v, %v", r, err)
	}
	if r, err := ParseRequest(nil); err != nil || len(r.Types) != 0 {
		t.Fatalf("empty body: %+v %v", r, err)
	}
	for name, body := range map[string]string{
		"wrong root":  hdr + `<foo/>`,
		"wrong ns":    hdr + `<locationRequest xmlns="urn:other"/>`,
		"not xml":     `hello`,
		"doctype":     `<!DOCTYPE x [<!ENTITY a "b">]><locationRequest xmlns="` + NSHeld + `"/>`,
		"unclosed":    `<locationRequest xmlns="` + NSHeld + `">`,
		"entity bomb": `<!DOCTYPE lolz [<!ENTITY a "aaaa">]><locationRequest xmlns="` + NSHeld + `">&a;</locationRequest>`,
	} {
		if _, err := ParseRequest([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAnswerable(t *testing.T) {
	civicOnly := Place{City: "Amsterdam"}
	geoOnly := Place{Latitude: f(1), Longitude: f(2)}
	if !(Request{}).Answerable(civicOnly) || (Request{}).Answerable(Place{}) {
		t.Error("an unconstrained request is answerable iff the place is usable")
	}
	exactGeo := Request{Types: []string{"geodetic"}, Exact: true}
	if exactGeo.Answerable(civicOnly) || !exactGeo.Answerable(geoOnly) {
		t.Error("exact geodetic needs a geo point")
	}
	loose := Request{Types: []string{"geodetic"}}
	if !loose.Answerable(civicOnly) {
		t.Error("a non-exact request takes what exists")
	}
}
