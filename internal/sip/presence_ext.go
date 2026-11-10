// Presence extensions (phone-feature-parity S-35): the reg event package
// (RFC 3680), PUBLISH of presence (RFC 3903, PIDF of RFC 3863) with the event
// state kept in Valkey for both hello-sip nodes, presence SUBSCRIBE with the
// dialog state folded in, and resource lists of dialog subscriptions
// (RFC 4662) for the BLF keys of a provisioned phone.
package sip

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

const (
	// blfListPrefix starts the user part of a phone's resource-list URI:
	// sip:blf-<device username>@<domain>. The provisioning templates name
	// it as .Line.BLFListURI.
	blfListPrefix = "blf-"

	// publishDefaultExpires, publishMinExpires and publishMaxExpires bound a
	// publication's lifetime (RFC 3903 default 3600 s).
	publishDefaultExpires = 3600 * time.Second
	publishMinExpires     = 60 * time.Second
	publishMaxExpires     = 3600 * time.Second
	// publishMaxBody bounds a PIDF document; a presence document is a few
	// hundred bytes.
	publishMaxBody = 8 << 10
	// presenceNoteMax bounds the note kept from a publication.
	presenceNoteMax = 200

	pidfType = "application/pidf+xml"
)

// presencePollInterval is how often reg and presence subscriptions are
// re-read from the shared state: a registration or a PUBLISH handled by the
// other node reaches this node's subscribers within it. Tests shorten it.
var presencePollInterval = 5 * time.Second

// PublishedPresence is the shared event state of PUBLISHed presence (Valkey);
// *livestate.Store satisfies it. Nil makes PUBLISH answer 503.
type PublishedPresence interface {
	PresencePut(ctx context.Context, doc livestate.PresenceDoc, replaces string, ttl time.Duration) (bool, error)
	PresenceDelete(ctx context.Context, ext, etag string) (bool, error)
	PresenceDocs(ctx context.Context, ext string) ([]livestate.PresenceDoc, error)
}

// --- PUBLISH ------------------------------------------------------------------

// pidfDoc is the part of a PIDF document (RFC 3863) Hello keeps.
type pidfDoc struct {
	XMLName xml.Name `xml:"urn:ietf:params:xml:ns:pidf presence"`
	Entity  string   `xml:"entity,attr"`
	Tuples  []struct {
		Status struct {
			Basic string `xml:"basic"`
		} `xml:"status"`
	} `xml:"tuple"`
	Notes []string `xml:"note"`
}

// parsePIDF reads a published document: the basic status (open when any
// tuple is open) and the first note. The entity must name the publisher's
// own extension or device.
func parsePIDF(body []byte, own func(user string) bool) (basic, note string, status int, err error) {
	var d pidfDoc
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = true
	if e := dec.Decode(&d); e != nil {
		return "", "", sip.StatusBadRequest, fmt.Errorf("malformed PIDF")
	}
	if d.Entity != "" {
		var u sip.Uri
		if e := sip.ParseUri(strings.TrimSpace(d.Entity), &u); e != nil || !own(u.User) {
			return "", "", sip.StatusForbidden, fmt.Errorf("entity is not the publisher")
		}
	}
	for _, t := range d.Tuples {
		switch strings.TrimSpace(t.Status.Basic) {
		case "open":
			basic = "open"
		case "closed":
			if basic == "" {
				basic = "closed"
			}
		}
	}
	if basic == "" {
		return "", "", sip.StatusBadRequest, fmt.Errorf("no tuple with a basic status")
	}
	if len(d.Notes) > 0 {
		note = cleanNote(d.Notes[0])
	}
	return basic, note, 0, nil
}

// cleanNote trims a note to presenceNoteMax runes and drops control
// characters.
func cleanNote(n string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(n) {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len() >= presenceNoteMax {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// handlePresencePublish serves PUBLISH Event: presence from a device of the
// extension it publishes for, digest-authenticated; another extension's
// presence is 403. An initial publication gets a SIP-ETag; a refresh,
// modification or removal quotes it in SIP-If-Match (412 when it is gone).
func (s *Server) handlePresencePublish(req *sip.Request, tx sip.ServerTransaction) {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		s.unavailable(tx, req)
		return
	}
	dev, ok := s.verify(req, tx, snap)
	if !ok {
		return
	}
	if eventOf(req) != eventPresence {
		s.respond(tx, req, 489, "Bad Event", sip.NewHeader("Allow-Events", allowEvents))
		return
	}
	ext, ok := ownFeatureExtension(snap, dev, req.Recipient.User)
	if !ok {
		s.respond(tx, req, sip.StatusForbidden, "Forbidden")
		return
	}
	pub := s.deps.Published
	if pub == nil {
		s.unavailable(tx, req)
		return
	}
	exp := publishDefaultExpires
	if h := req.GetHeader("Expires"); h != nil {
		n, err := strconv.Atoi(strings.TrimSpace(h.Value()))
		if err != nil || n < 0 {
			s.respond(tx, req, sip.StatusBadRequest, "Bad Expires")
			return
		}
		exp = time.Duration(n) * time.Second
		if n > 0 {
			exp = min(max(exp, publishMinExpires), publishMaxExpires)
		}
	}
	ifMatch := ""
	if h := req.GetHeader("SIP-If-Match"); h != nil {
		ifMatch = strings.TrimSpace(h.Value())
	}
	body := req.Body()
	if len(body) > publishMaxBody {
		s.respond(tx, req, sip.StatusRequestEntityTooLarge, "Request Entity Too Large")
		return
	}
	ctx, cancel := s.stateCtx()
	defer cancel()

	if exp == 0 { // removal
		if ifMatch == "" {
			s.respond(tx, req, sip.StatusBadRequest, "SIP-If-Match required")
			return
		}
		removed, err := pub.PresenceDelete(ctx, ext, ifMatch)
		if err != nil {
			s.stateDown(tx, req, "presence_delete", err)
			return
		}
		if !removed {
			s.respond(tx, req, 412, "Conditional Request Failed")
			return
		}
		s.respond(tx, req, sip.StatusOK, "OK", sip.NewHeader("SIP-ETag", ifMatch), sip.NewHeader("Expires", "0"))
		s.goBG(func() { s.notifyPresence(ext) })
		return
	}

	doc := livestate.PresenceDoc{Extension: ext, ETag: newID(), Device: dev.Username, UpdatedAt: time.Now()}
	switch {
	case len(body) > 0:
		if ct := req.ContentType(); ct == nil || !strings.EqualFold(strings.TrimSpace(strings.SplitN(ct.Value(), ";", 2)[0]), pidfType) {
			s.respond(tx, req, sip.StatusUnsupportedMediaType, "Unsupported Media Type", sip.NewHeader("Accept", pidfType))
			return
		}
		basic, note, code, err := parsePIDF(body, func(user string) bool {
			_, ok := ownFeatureExtension(snap, dev, user)
			return ok
		})
		if err != nil {
			s.respond(tx, req, code, err.Error())
			return
		}
		doc.Basic, doc.Note = basic, note
	case ifMatch == "":
		s.respond(tx, req, sip.StatusBadRequest, "A publication needs a body or SIP-If-Match")
		return
	default: // a refresh keeps the document it quotes
		docs, err := pub.PresenceDocs(ctx, ext)
		if err != nil {
			s.stateDown(tx, req, "presence_docs", err)
			return
		}
		found := false
		for _, d := range docs {
			if d.ETag == ifMatch {
				doc.Basic, doc.Note, found = d.Basic, d.Note, true
			}
		}
		if !found {
			s.respond(tx, req, 412, "Conditional Request Failed")
			return
		}
	}
	stored, err := pub.PresencePut(ctx, doc, ifMatch, exp)
	if err != nil {
		s.stateDown(tx, req, "presence_put", err)
		return
	}
	if !stored {
		s.respond(tx, req, 412, "Conditional Request Failed")
		return
	}
	s.respond(tx, req, sip.StatusOK, "OK",
		sip.NewHeader("SIP-ETag", doc.ETag), sip.NewHeader("Expires", strconv.Itoa(int(exp/time.Second))))
	s.goBG(func() { s.notifyPresence(ext) })
}

// notifyPresence sends the extension's presence subscribers its current
// document; a subscriber whose document did not change gets nothing.
func (s *Server) notifyPresence(ext string) {
	defer contain(s.log, "presence notify")
	for _, sub := range s.subsOf(ext, eventPresence) {
		s.sendPresenceNotify(sub, int(time.Until(sub.expires)/time.Second), "", false)
	}
}

// subsOf lists the extension's live subscriptions of one event package.
func (s *Server) subsOf(ext, event string) []*subscription {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	var out []*subscription
	for id := range s.byExt[ext] {
		if sub, ok := s.subs[id]; ok && sub.event == event && !sub.list {
			out = append(out, sub)
		}
	}
	return out
}

// --- presence documents -------------------------------------------------------

// presenceBasic is the extension's basic status and note: the published
// documents' (open when any publication is open, the newest note), or, with
// none, open while a device of the extension is registered.
func (s *Server) presenceBasic(ext string) (basic, note string) {
	if pub := s.deps.Published; pub != nil {
		ctx, cancel := s.stateCtx()
		docs, err := pub.PresenceDocs(ctx, ext)
		cancel()
		if err == nil && len(docs) > 0 {
			livestate.SortPresenceDocs(docs)
			basic = "closed"
			for _, d := range docs {
				if d.Basic == "open" {
					basic = "open"
				}
				if note == "" {
					note = d.Note
				}
			}
			return basic, note
		}
	}
	if len(s.bindingsFor(ext)) > 0 {
		return "open", ""
	}
	return "closed", ""
}

// pidfXML renders the presence document of one extension: its basic status
// and note, with the dialog state folded in as an RPID activity (on the
// phone while ringing or in a call, busy under DND).
func pidfXML(ext, domain, basic, note, dlg string) []byte {
	var b bytes.Buffer
	uri := "sip:" + ext + "@" + domain
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\r\n")
	b.WriteString(`<presence xmlns="urn:ietf:params:xml:ns:pidf" xmlns:dm="urn:ietf:params:xml:ns:pidf:data-model" xmlns:rpid="urn:ietf:params:xml:ns:pidf:rpid" entity="` + xmlAttr(uri) + `">` + "\r\n")
	b.WriteString(`<tuple id="t-` + xmlAttr(ext) + `"><status><basic>` + basic + `</basic></status><contact>` + xmlText(uri) + `</contact></tuple>` + "\r\n")
	if note != "" {
		b.WriteString(`<note>` + xmlText(note) + `</note>` + "\r\n")
	}
	var act string
	switch dlg {
	case StateRinging, StateOnCall:
		act = `<rpid:on-the-phone/>`
	case StateDND:
		act = `<rpid:busy/>`
	}
	if act != "" {
		b.WriteString(`<dm:person id="p-` + xmlAttr(ext) + `"><rpid:activities>` + act + `</rpid:activities></dm:person>` + "\r\n")
	}
	b.WriteString(`</presence>` + "\r\n")
	return b.Bytes()
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func xmlAttr(s string) string { return xmlText(s) }

// sendPresenceNotify sends the subscription's NOTIFY with the extension's
// current presence. Unless force is set, an unchanged document is not sent.
func (s *Server) sendPresenceNotify(sub *subscription, expires int, reason string, force bool) {
	defer contain(s.log, "presence NOTIFY")
	basic, note := s.presenceBasic(sub.extension)
	s.subsMu.Lock()
	dlg := sub.lastState
	s.subsMu.Unlock()
	body := pidfXML(sub.extension, s.cfg.Domain, basic, note, dlg)
	s.sendDocNotify(sub, eventPresence, pidfType, body, expires, reason, force, nil)
}

// --- reg event ----------------------------------------------------------------

// regXML renders the reginfo document (RFC 3680) of an extension: one
// registration per extension, a contact per live binding.
func regXML(ext, domain string, version uint64, bs []livestate.Binding, now time.Time) []byte {
	sort.Slice(bs, func(i, j int) bool { return bs[i].ContactURI < bs[j].ContactURI })
	var b bytes.Buffer
	state := "terminated"
	if len(bs) > 0 {
		state = "active"
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\r\n")
	b.WriteString(`<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="` + strconv.FormatUint(version, 10) + `" state="full">` + "\r\n")
	b.WriteString(`<registration aor="` + xmlAttr("sip:"+ext+"@"+domain) + `" id="r-` + xmlAttr(ext) + `" state="` + state + `">` + "\r\n")
	for _, c := range bs {
		sum := sha256.Sum256([]byte(c.ContactURI))
		left := max(int(c.Expires.Sub(now)/time.Second), 0)
		b.WriteString(`<contact id="c-` + hex.EncodeToString(sum[:4]) + `" state="active" event="registered" expires="` + strconv.Itoa(left) + `"><uri>` + xmlText(c.ContactURI) + `</uri></contact>` + "\r\n")
	}
	b.WriteString(`</registration>` + "\r\n" + `</reginfo>` + "\r\n")
	return b.Bytes()
}

// sendRegNotify sends the subscription's NOTIFY with the extension's current
// registrations. Unless force is set, an unchanged set is not sent. The
// expiry seconds are left out of the comparison, they change every second.
func (s *Server) sendRegNotify(sub *subscription, expires int, reason string, force bool) {
	defer contain(s.log, "reg NOTIFY")
	bs := s.bindingsFor(sub.extension)
	key := regKey(bs)
	now := time.Now()
	s.sendDocNotify(sub, eventReg, "application/reginfo+xml", nil, expires, reason, force, &docSpec{
		key: key,
		render: func(ver uint64) ([]byte, []sip.Header) {
			return regXML(sub.extension, s.cfg.Domain, ver, bs, now), nil
		},
	})
}

func regKey(bs []livestate.Binding) string {
	uris := make([]string, 0, len(bs))
	for _, b := range bs {
		uris = append(uris, b.ContactURI)
	}
	sort.Strings(uris)
	return strings.Join(uris, "\n")
}

// regChanged tells the reg subscribers of ext that a binding was added or
// removed on this node. Bindings changed by the other node reach them through
// the poll.
func (s *Server) regChanged(ext string) {
	s.goBG(func() {
		defer contain(s.log, "reg changed")
		for _, sub := range s.subsOf(ext, eventReg) {
			s.sendRegNotify(sub, int(time.Until(sub.expires)/time.Second), "", false)
		}
	})
}

// --- generic NOTIFY -----------------------------------------------------------

// docSpec renders a document that carries a version number; key is what
// decides whether the document changed.
type docSpec struct {
	key    string
	render func(version uint64) ([]byte, []sip.Header)
}

// sendDocNotify sends one in-dialog NOTIFY for the subscription. Either body
// (a fixed document, compared as it is) or spec (a versioned one) gives the
// content. Without force an unchanged document is skipped; a non-empty reason
// ends the subscription. CSeq and version advance under the subscription lock.
func (s *Server) sendDocNotify(sub *subscription, event, ctype string, body []byte, expires int, reason string, force bool, spec *docSpec) {
	key := string(body)
	if spec != nil {
		key = spec.key
	}
	s.subsMu.Lock()
	if reason == "" && !force && sub.lastDoc == key {
		s.subsMu.Unlock()
		return
	}
	sub.cseq++
	sub.version++
	seq, ver := sub.cseq, sub.version
	sub.lastDoc = key
	s.subsMu.Unlock()
	var extra []sip.Header
	if spec != nil {
		body, extra = spec.render(ver)
	}

	req := sip.NewRequest(sip.NOTIFY, *sub.target.Clone())
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: sub.extension, Host: s.cfg.Domain},
		Params:  sip.HeaderParams{{K: "tag", V: sub.localTag}},
	})
	req.AppendHeader(&sip.ToHeader{
		Address: sip.Uri{Scheme: "sip", User: sub.device, Host: s.cfg.Domain},
		Params:  sip.HeaderParams{{K: "tag", V: sub.remoteTag}},
	})
	callID := sip.CallIDHeader(sub.callID)
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: seq, MethodName: sip.NOTIFY})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Event", event))
	if reason != "" {
		req.AppendHeader(sip.NewHeader("Subscription-State", "terminated;reason="+reason))
	} else {
		req.AppendHeader(sip.NewHeader("Subscription-State", "active;expires="+strconv.Itoa(max(expires, 0))))
	}
	for _, h := range extra {
		req.AppendHeader(h)
	}
	req.AppendHeader(sip.NewHeader("Content-Type", ctype))
	req.SetBody(body)
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := s.do(ctx, req)
	if err != nil {
		s.log.Debug("NOTIFY failed", "event", event, "device", sub.device, "error", err)
		return
	}
	s.m.response(res.StatusCode)
}

// presencePollLoop re-reads what other nodes change: a registration handled
// elsewhere reaches reg subscribers, a PUBLISH handled elsewhere reaches
// presence subscribers.
func (s *Server) presencePollLoop(ctx context.Context) {
	t := time.NewTicker(presencePollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-t.C:
		}
		s.presencePollOnce()
	}
}

func (s *Server) presencePollOnce() {
	defer contain(s.log, "presence poll")
	s.subsMu.Lock()
	var regs, pres []*subscription
	for _, sub := range s.subs {
		switch sub.event {
		case eventReg:
			regs = append(regs, sub)
		case eventPresence:
			pres = append(pres, sub)
		}
	}
	s.subsMu.Unlock()
	for _, sub := range regs {
		s.sendRegNotify(sub, int(time.Until(sub.expires)/time.Second), "", false)
	}
	for _, sub := range pres {
		s.sendPresenceNotify(sub, int(time.Until(sub.expires)/time.Second), "", false)
	}
}

// --- resource lists -----------------------------------------------------------

// blfListUser reports whether user names a resource list, and the device
// username the list belongs to. A real extension keeps its number.
func blfListUser(snap *snapshot.Snapshot, user string) (string, bool) {
	if !strings.HasPrefix(user, blfListPrefix) || snap.HasExtension(user) {
		return "", false
	}
	return strings.TrimPrefix(user, blfListPrefix), true
}

// isListSub reports whether an in-dialog request is to a resource list.
func isListSub(req *sip.Request) bool { return strings.HasPrefix(req.Recipient.User, blfListPrefix) }

// supportsEventlist reports whether the SUBSCRIBE lists the eventlist option
// tag in Supported.
func supportsEventlist(req *sip.Request) bool {
	for _, h := range req.GetHeaders("Supported") {
		for _, t := range strings.Split(h.Value(), ",") {
			if strings.EqualFold(strings.TrimSpace(t), "eventlist") {
				return true
			}
		}
	}
	return false
}

// handleListSubscribe serves a dialog SUBSCRIBE to a phone's resource list
// (RFC 4662): only the phone's own device may subscribe (403), the phone must
// say it supports eventlist (421), and it must have BLF keys (404). Each
// NOTIFY carries the whole list as multipart/related with an RLMI part.
func (s *Server) handleListSubscribe(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, dev snapshot.Device, listUser string) {
	if dev.Extension == "" || listUser != dev.Username {
		s.respond(tx, req, sip.StatusForbidden, "Forbidden")
		return
	}
	numbers, ok := snap.BLFList(dev.Username)
	if !ok {
		s.respond(tx, req, sip.StatusNotFound, "Not Found")
		return
	}
	if !supportsEventlist(req) {
		s.respond(tx, req, 421, "Extension Required", sip.NewHeader("Require", "eventlist"))
		return
	}
	var members []string
	seen := map[string]bool{}
	for _, n := range numbers {
		if !seen[n] && blfTargetKnown(snap, n) {
			seen[n] = true
			members = append(members, n)
		}
	}
	exp := s.subscribeExpires(req)
	if exp < 0 {
		s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
		return
	}
	if exp == 0 {
		s.dropSub(req.CallID().Value(), true)
		s.respond(tx, req, sip.StatusOK, "OK", sip.NewHeader("Expires", "0"))
		return
	}
	exp = min(max(exp, mwiMinExpires), mwiMaxExpires)
	sub, fresh := s.upsertSub(req, eventDialog, dev.Extension, exp)
	if sub == nil {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	s.subsMu.Lock()
	sub.list = true
	sub.listURI = "sip:" + blfListPrefix + dev.Username + "@" + s.cfg.Domain
	sub.members = members
	old := sub.memberState
	sub.memberState = map[string]string{}
	s.subsMu.Unlock()
	states := map[string]string{}
	for _, n := range members {
		if st, ok := old[n]; ok && !fresh {
			states[n] = st
		} else {
			states[n] = s.initialDialogState(snap, n)
		}
	}
	s.subsMu.Lock()
	sub.memberState = states
	s.subsMu.Unlock()
	s.respond(tx, req, sip.StatusOK, "OK",
		sip.NewHeader("Expires", strconv.Itoa(int(exp/time.Second))),
		sip.NewHeader("Event", eventDialog),
		sip.NewHeader("Require", "eventlist"),
		&sip.ToHeader{Address: *req.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: sub.localTag}}})
	s.m.PresenceSubscriptions.Set(float64(s.subCount()))
	s.goBG(func() { s.sendListNotify(sub, int(exp/time.Second), "", nil) })
}

// listNotifyBudget is the body size one list NOTIFY may have: sipgo refuses a
// UDP datagram over 1300 bytes (RFC 3261 section 18.1.1) and the NOTIFY's own
// headers take about 520 of them and a Route header about 100 more. A list therefore travels as one full-state
// NOTIFY with as many resources as fit, then partial ones (RFC 4662
// fullState="false") for the rest, and a state change sends only the resource
// that changed.
const listNotifyBudget = 600

// sendListNotify sends the resource list's NOTIFYs. With changed empty it
// sends the whole list (a new or refreshed subscription); otherwise only the
// named members. A non-empty reason ends the subscription.
func (s *Server) sendListNotify(sub *subscription, expires int, reason string, changed []string) {
	defer contain(s.log, "list NOTIFY")
	s.subsMu.Lock()
	members := append([]string(nil), sub.members...)
	if len(changed) > 0 {
		members = changed
	}
	states := make(map[string]string, len(members))
	for _, m := range members {
		states[m] = sub.memberState[m]
	}
	uri := sub.listURI
	boundary := "rl" + sub.localTag
	s.subsMu.Unlock()
	rlmiCID := "r" + sub.localTag + "@rl"
	ctype := `multipart/related;type="application/rlmi+xml";start="<` + rlmiCID + `>";boundary="` + boundary + `"`
	send := func(chunk []string, full bool, reason string) {
		s.sendDocNotify(sub, eventDialog, ctype, nil, expires, reason, true, &docSpec{
			render: func(ver uint64) ([]byte, []sip.Header) {
				return listBody(s.cfg.Domain, uri, boundary, rlmiCID, ver, full, chunk, states), []sip.Header{sip.NewHeader("Require", "eventlist")}
			},
		})
	}
	if reason != "" {
		send(nil, true, reason)
		return
	}
	full := len(changed) == 0
	for len(members) > 0 {
		n := 1
		for n < len(members) && len(listBody(s.cfg.Domain, uri, boundary, rlmiCID, 0, full, members[:n+1], states)) <= listNotifyBudget {
			n++
		}
		send(members[:n], full, "")
		members = members[n:]
		full = false
	}
	if len(sub.members) == 0 && len(changed) == 0 {
		send(nil, true, "")
	}
}

// listBody builds the multipart/related body of a list NOTIFY: the RLMI
// document and one dialog-info part per listed member.
func listBody(domain, listURI, boundary, rlmiCID string, ver uint64, full bool, members []string, states map[string]string) []byte {
	var b bytes.Buffer
	b.WriteString("--" + boundary + "\r\nContent-Type: application/rlmi+xml\r\nContent-ID: <" + rlmiCID + ">\r\n\r\n")
	b.WriteString(`<list xmlns="urn:ietf:params:xml:ns:rlmi" uri="` + xmlAttr(listURI) + `" version="` + strconv.FormatUint(ver, 10) + `" fullState="` + strconv.FormatBool(full) + `">`)
	for _, m := range members {
		// No <name>: the phone labels its own keys, and a long name would push
		// a single resource past the datagram.
		b.WriteString(`<resource uri="` + xmlAttr("sip:"+m+"@"+domain) + `">`)
		b.WriteString(`<instance id="1" state="active" cid="` + xmlAttr(listPartCID(m, domain)) + `"/></resource>`)
	}
	b.WriteString("</list>\r\n")
	for _, m := range members {
		b.WriteString("--" + boundary + "\r\nContent-Type: application/dialog-info+xml\r\nContent-ID: <" + listPartCID(m, domain) + ">\r\n\r\n")
		b.WriteString(dialogCompact(m, domain, ver, states[m]))
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}

// listPartCID is the Content-ID a list part is referenced by.
func listPartCID(member, _ string) string { return "i" + member + "@rl" }

// dialogCompact is dialogXML without the declaration and line breaks, for the
// parts of a list NOTIFY, where every byte counts against the datagram.
func dialogCompact(ext, domain string, version uint64, state string) string {
	full := string(dialogXML(ext, domain, version, state))
	full = strings.TrimPrefix(full, `<?xml version="1.0"?>`+"\r\n")
	return strings.ReplaceAll(full, "\r\n", "") + "\r\n"
}
