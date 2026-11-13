package location

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// XML namespaces of PIDF-LO and HELD.
const (
	NSHeld    = "urn:ietf:params:xml:ns:geopriv:held"
	NSPidf    = "urn:ietf:params:xml:ns:pidf"
	NSGeopriv = "urn:ietf:params:xml:ns:pidf:geopriv10"
	NSCivic   = "urn:ietf:params:xml:ns:pidf:geopriv10:civicAddr"
	NSGML     = "http://www.opengis.net/gml"
	NSGS      = "http://www.opengis.net/pidflo/1.0"
)

// ContentTypeHELD is the media type of HELD documents; ContentTypePIDF the
// one of a PIDF-LO body in a SIP message.
const (
	ContentTypeHELD = "application/held+xml"
	ContentTypePIDF = "application/pidf+xml"
)

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// presence writes the PIDF-LO presence element for p. entity is the URI the
// location is about ("pres:101@pbx.example"); it is escaped. Only the
// namespaces the document uses are declared and a zero at leaves out the
// timestamp, so the document stays as small as it can: a SIP request over UDP
// carries it.
func presence(p Place, entity string, at time.Time) string {
	var b strings.Builder
	civic := p.HasCivic() || p.Floor != "" || p.Room != ""
	b.WriteString(`<presence xmlns="` + NSPidf + `" xmlns:gp="` + NSGeopriv + `"`)
	if civic {
		b.WriteString(` xmlns:ca="` + NSCivic + `"`)
	}
	if p.HasGeo() {
		b.WriteString(` xmlns:gml="` + NSGML + `"`)
		if p.RadiusM != nil {
			b.WriteString(` xmlns:gs="` + NSGS + `"`)
		}
	}
	fmt.Fprintf(&b, ` entity="%s"><tuple id="h"><status><gp:geopriv><gp:location-info>`, esc(entity))
	if civic {
		b.WriteString(`<ca:civicAddress>`)
		el := func(name, v string) {
			if v != "" {
				fmt.Fprintf(&b, "<ca:%s>%s</ca:%s>", name, esc(v), name)
			}
		}
		el("country", p.Country)
		el("A1", p.State)
		el("A3", p.City)
		el("A6", p.Street)
		el("HNO", p.HouseNumber)
		el("FLR", p.Floor)
		el("ROOM", p.Room)
		el("PC", p.PostalCode)
		b.WriteString(`</ca:civicAddress>`)
	}
	if p.HasGeo() {
		pos := num(*p.Latitude) + " " + num(*p.Longitude)
		if p.RadiusM != nil {
			fmt.Fprintf(&b, `<gs:Circle srsName="urn:ogc:def:crs:EPSG::4326"><gml:pos>%s</gml:pos><gs:radius uom="urn:ogc:def:uom:EPSG::9001">%s</gs:radius></gs:Circle>`, pos, num(*p.RadiusM))
		} else {
			fmt.Fprintf(&b, `<gml:Point srsName="urn:ogc:def:crs:EPSG::4326"><gml:pos>%s</gml:pos></gml:Point>`, pos)
		}
	}
	b.WriteString(`</gp:location-info><gp:usage-rules><gp:retransmission-allowed>yes</gp:retransmission-allowed></gp:usage-rules><gp:method>Manual</gp:method></gp:geopriv></status>`)
	if !at.IsZero() {
		fmt.Fprintf(&b, `<timestamp>%s</timestamp>`, at.UTC().Format(time.RFC3339))
	}
	b.WriteString(`</tuple></presence>`)
	return b.String()
}

// PIDFLO is the PIDF-LO document (RFC 4119) of p, for the body of a SIP
// request; a zero at leaves out the timestamp.
func PIDFLO(p Place, entity string, at time.Time) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + presence(p, entity, at))
}

// HELDResponse is the locationResponse document (RFC 5985 section 4.3) with
// the PIDF-LO of p.
func HELDResponse(p Place, entity string, at time.Time) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<locationResponse xmlns="%s">`, NSHeld)
	b.WriteString(presence(p, entity, at))
	b.WriteString(`</locationResponse>`)
	return []byte(b.String())
}

// HELD error codes (RFC 5985 section 4.3.2 and 6.3).
const (
	ErrRequest             = "requestError"
	ErrLocationUnavailable = "locationUnavailable"
	ErrUnsupportedType     = "unsupportedMessage"
)

// HELDError is an error document.
func HELDError(code, message string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		fmt.Sprintf(`<error xmlns="%s" code="%s"><message xml:lang="en">%s</message></error>`, NSHeld, esc(code), esc(message)))
}

// Request is what Hello reads of a locationRequest.
type Request struct {
	// Types are the requested location types (civic, geodetic,
	// locationURI); empty means any. Exact is the locationType exact flag.
	Types []string
	Exact bool
}

// ParseRequest reads a HELD locationRequest (RFC 5985 section 4.1). An
// empty body is a request for any location. Anything that is not a
// locationRequest document is an error.
func ParseRequest(body []byte) (Request, error) {
	var r Request
	if len(bytes.TrimSpace(body)) == 0 {
		return r, nil
	}
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict = true
	d.Entity = nil
	root := true
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) && !root {
			break
		}
		if err != nil {
			return Request{}, fmt.Errorf("not a well-formed XML document")
		}
		switch t := tok.(type) {
		case xml.Directive:
			return Request{}, fmt.Errorf("DOCTYPE and entity declarations are not accepted")
		case xml.StartElement:
			if root {
				if t.Name.Space != NSHeld || t.Name.Local != "locationRequest" {
					return Request{}, fmt.Errorf("the root element is not a HELD locationRequest")
				}
				root = false
				continue
			}
			if t.Name.Space == NSHeld && t.Name.Local == "locationType" {
				for _, a := range t.Attr {
					if a.Name.Local == "exact" {
						r.Exact = a.Value == "true" || a.Value == "1"
					}
				}
				var txt string
				if err := d.DecodeElement(&txt, &t); err != nil {
					return Request{}, fmt.Errorf("bad locationType")
				}
				r.Types = append(r.Types, strings.Fields(txt)...)
			}
		}
	}
	return r, nil
}

// Answerable reports whether p satisfies the request: with exact set, one of
// the requested types must be available (civic or geodetic); without it,
// anything usable does.
func (r Request) Answerable(p Place) bool {
	if !p.Usable() {
		return false
	}
	if !r.Exact || len(r.Types) == 0 {
		return true
	}
	for _, t := range r.Types {
		switch t {
		case "civic":
			if p.HasCivic() {
				return true
			}
		case "geodetic":
			if p.HasGeo() {
				return true
			}
		}
	}
	return false
}
