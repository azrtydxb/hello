package sip

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"regexp"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/location"
	"github.com/emiago/sipgo/sip"
)

// Geolocation on emergency calls (spec phone-feature-parity S-28 e, RFC
// 6442). A phone that sends its own location with an emergency call has it
// forwarded to a trunk flagged geolocation; otherwise the location stored
// for the phone is added. A trunk that is not flagged gets neither: the
// carrier never sees a location the administrator did not agree to share.

// geoDatagramLimit is the largest UDP datagram Hello sends. sipgo refuses
// anything over 1300 bytes (RFC 3261 18.1.1), which an INVITE with a PIDF-LO
// body does not fit in; the limit is raised once at start, before any server
// exists, so it is not written while another goroutine reads it. A trunk is
// only sent such a request when the administrator flagged it geolocation.
const geoDatagramLimit = 4000

func init() {
	if sip.UDPMTUSize < geoDatagramLimit {
		sip.UDPMTUSize = geoDatagramLimit
	}
}

// Limits on what is taken from a phone: a PIDF-LO document is a few hundred
// bytes, so anything larger is not forwarded.
const (
	maxPIDFBytes   = 16 << 10
	maxGeoHeader   = 1024
	maxEmergInfo   = 512
	geoContentType = "multipart/mixed"
)

// geoInfo is what the caller's INVITE carried for its location.
type geoInfo struct {
	// header is the Geolocation header value, routing the Geolocation-Routing
	// one and emergencyInfo the P-Emergency-Info one, as sent.
	header, routing, emergencyInfo string
	// pidf is the PIDF-LO part of a multipart body, only when it is
	// well-formed XML with a presence root.
	pidf []byte
}

var geoEntryRE = regexp.MustCompile(`<[^<>]*>[^,]*`)

// takeGeolocation reads the location a phone sent with its INVITE and leaves
// the INVITE with a plain SDP body, so the media code never meets a
// multipart one. A body that is not a well-formed multipart is left alone.
func (c *call) takeGeolocation() {
	req := c.inv
	if req == nil {
		return
	}
	var g geoInfo
	g.header = clipHeader(req.GetHeader("Geolocation"), maxGeoHeader)
	g.routing = clipHeader(req.GetHeader("Geolocation-Routing"), 16)
	g.emergencyInfo = clipHeader(req.GetHeader("P-Emergency-Info"), maxEmergInfo)
	if ct := req.ContentType(); ct != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct.Value())), "multipart/") {
		if sdp, pidf, ok := splitMultipart(ct.Value(), req.Body()); ok {
			req.RemoveHeader("Content-Type")
			if len(sdp) > 0 {
				req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
			}
			req.SetBody(sdp)
			if wellFormedPIDF(pidf) {
				g.pidf = pidf
			}
		}
	}
	c.mu.Lock()
	c.geo = g
	c.mu.Unlock()
}

func clipHeader(h sip.Header, max int) string {
	if h == nil {
		return ""
	}
	v := strings.TrimSpace(h.Value())
	if len(v) > max || strings.ContainsAny(v, "\r\n\x00") {
		return ""
	}
	return v
}

// splitMultipart returns the SDP and PIDF-LO parts of a multipart body.
func splitMultipart(contentType string, body []byte) (sdp, pidf []byte, ok bool) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mt, "multipart/") || params["boundary"] == "" {
		return nil, nil, false
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for i := 0; i < 8; i++ {
		part, err := mr.NextRawPart()
		if err != nil {
			break
		}
		pt, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		data, err := io.ReadAll(io.LimitReader(part, maxPIDFBytes+1))
		if err != nil {
			break
		}
		switch pt {
		case "application/sdp":
			if sdp == nil {
				sdp = data
			}
		case location.ContentTypePIDF:
			if pidf == nil && len(data) <= maxPIDFBytes {
				pidf = data
			}
		}
	}
	return sdp, pidf, sdp != nil || pidf != nil
}

// wellFormedPIDF reports whether doc is well-formed XML whose root is a
// presence element and that declares no entities.
func wellFormedPIDF(doc []byte) bool {
	if len(doc) == 0 {
		return false
	}
	d := xml.NewDecoder(bytes.NewReader(doc))
	root := true
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return !root
		}
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case xml.Directive:
			return false
		case xml.StartElement:
			if root {
				if t.Name.Local != "presence" {
					return false
				}
				root = false
			}
		}
	}
}

// addGeolocation adds the caller's location to the INVITE of a leg to a
// trunk flagged geolocation, when the call is an emergency call.
func (l *leg) addGeolocation(req *sip.Request) {
	if l.trunk == nil || !l.trunk.Geolocation {
		return
	}
	c, s := l.c, l.c.s
	c.mu.Lock()
	em, g, dev, num := c.emergency, c.geo, c.callerDevice, c.callerNum
	c.mu.Unlock()
	if !em {
		return
	}
	if g.emergencyInfo != "" {
		req.AppendHeader(sip.NewHeader("P-Emergency-Info", g.emergencyInfo))
	}
	pidf, source := g.pidf, "the phone's own location"
	if len(pidf) == 0 {
		pidf = nil
		if snap := c.snapOrFail(); snap != nil {
			if p, ok := snap.DevicePlace(dev); ok && p.Usable() {
				pidf = location.PIDFLO(p, "pres:"+num+"@"+s.cfg.Domain, time.Time{})
				source = "the stored location " + p.Path
			}
		}
	}
	// Location by reference (a URI in the phone's header) is forwarded as is.
	var refs []string
	for _, e := range geoEntryRE.FindAllString(g.header, -1) {
		if !strings.HasPrefix(strings.ToLower(e), "<cid:") {
			refs = append(refs, strings.TrimSpace(e))
		}
	}
	if len(pidf) == 0 && len(refs) == 0 {
		c.addTrace(fmt.Sprintf("Emergency call to trunk %s: no location to send", l.trunk.Name))
		s.m.Emergency.WithLabelValues("emergency", "no_location").Inc()
		return
	}
	header := strings.Join(refs, ", ")
	if len(pidf) > 0 {
		cid := newCID() + "@" + s.cfg.Domain
		header = strings.Join(append([]string{"<cid:" + cid + ">"}, refs...), ", ")
		sdp := req.Body()
		boundary := "hello-geo-" + newCID()
		var b bytes.Buffer
		if len(sdp) > 0 {
			fmt.Fprintf(&b, "--%s\r\nContent-Type: application/sdp\r\n\r\n", boundary)
			b.Write(sdp)
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s\r\nContent-ID: <%s>\r\nContent-Disposition: render;handling=optional\r\n\r\n", boundary, location.ContentTypePIDF, cid)
		b.Write(pidf)
		fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
		req.RemoveHeader("Content-Type")
		req.AppendHeader(sip.NewHeader("Content-Type", geoContentType+";boundary="+boundary))
		req.SetBody(b.Bytes())
	}
	req.RemoveHeader("Geolocation")
	req.AppendHeader(sip.NewHeader("Geolocation", header))
	if g.routing != "" {
		req.AppendHeader(sip.NewHeader("Geolocation-Routing", g.routing))
	}
	c.addTrace(fmt.Sprintf("Emergency call to trunk %s: sent Geolocation (%s)", l.trunk.Name, source))
	s.m.Emergency.WithLabelValues("emergency", "geolocation").Inc()
}

func newCID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
