// Feature key synchronization (Broadsoft / ECMA-323 "as-feature-event"):
// the phone's DND and call-forward keys and Hello's per-extension settings
// follow each other.
//
// Phone to Hello: the phone SUBSCRIBEs to Event: as-feature-event; a change
// made on the phone arrives as a SetDoNotDisturb / SetForwarding (or the
// matching ...Event) document in the SUBSCRIBE body and is applied exactly
// like a feature code: queued to the settings sink, which writes the
// extension, audits it and publishes the configuration change cluster-wide.
//
// Hello to phone: every snapshot reload (UI, API, AI and feature-code
// changes all end in one) compares each subscription's last-sent state with
// the extension's current one and NOTIFYs the difference, so the indicator
// follows. Subscriptions live in this node's memory like the BLF and MWI
// ones; each node watches the snapshot itself, so both nodes keep their own
// subscribers current. After a failover the phone's refresh (an in-dialog
// SUBSCRIBE this node has never seen) re-establishes the subscription from
// the request itself and the full state is sent again.
//
// The XML follows ECMA-323 (CSTA) as FreeSWITCH's mod_sofia implements it
// (SetDoNotDisturb / SetForwarding from the phone, DoNotDisturbEvent /
// ForwardingEvent to it), the server these phones are routinely tested
// against. Unlike mod_sofia, which packs the full state into one multipart
// NOTIFY, each document gets its own NOTIFY so it fits a UDP datagram. It has
// not been verified against a phone on Hello itself.
package sip

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

const (
	eventFeature       = "as-feature-event"
	featureContentType = "application/x-as-feature-event+xml"
	ecmaNamespace      = "http://www.ecma-international.org/standards/ecma-323/csta/ed3"

	// featureMinExpires and featureMaxExpires clamp the renewal interval,
	// like MWI's.
	featureMinExpires = 60 * time.Second
	featureMaxExpires = 3600 * time.Second

	// maxFeatureBody bounds the XML a SUBSCRIBE may carry.
	maxFeatureBody = 8 << 10

	// ECMA-323 forwarding types.
	fwdImmediate = "forwardImmediate"
	fwdBusy      = "forwardBusy"
	fwdNoAnswer  = "forwardNoAns"
)

var featureTargetRe = regexp.MustCompile(`^\+?[0-9*#]{2,32}$`)

// errFeatureBody is a malformed or unusable as-feature-event document.
var errFeatureBody = errors.New("bad as-feature-event body")

// featState is the feature state of one extension as a phone shows it.
type featState struct {
	DND                 bool
	Always, Busy, NoAns string
	Rings               int
}

// featStateOf reads an extension's feature state. A ring count of 0 reports
// the system ring timeout as rings.
func (s *Server) featStateOf(e snapshot.Extension) featState {
	rings := e.NoAnswerRings
	if rings == 0 {
		rings = max(int(s.cfg.RingTimeout/time.Second)/secondsPerRing, 1)
	}
	return featState{DND: e.DND, Always: e.ForwardAlways, Busy: e.ForwardBusy, NoAns: e.ForwardNoAnswer, Rings: rings}
}

// apply overlays a queued update on the state, so the NOTIFY that answers a
// phone's change does not flap back to the pre-reload value.
func (f featState) apply(u ExtUpdate) featState {
	if u.DND != nil {
		f.DND = *u.DND
	}
	if u.ForwardAlways != nil {
		f.Always = *u.ForwardAlways
	}
	if u.ForwardBusy != nil {
		f.Busy = *u.ForwardBusy
	}
	if u.ForwardNoAnswer != nil {
		f.NoAns = *u.ForwardNoAnswer
	}
	if u.NoAnswerRings != nil && *u.NoAnswerRings > 0 {
		f.Rings = *u.NoAnswerRings
	}
	return f
}

// featChange is one change a phone asked for.
type featChange struct {
	device string // the <device> the phone named, "" if none
	dnd    *bool  // DND on or off
	fwd    string // fwdImmediate, fwdBusy or fwdNoAnswer
	target *string
	rings  *int
}

// featDoc is the union of the ECMA-323 / Broadsoft documents; element names
// match whatever namespace the phone declares.
type featDoc struct {
	XMLName   xml.Name
	Device    string `xml:"device"`
	DNDOn     *bool  `xml:"doNotDisturbOn"`
	Type      string `xml:"forwardingType"`
	Activate  *bool  `xml:"activateForward"`
	Status    *bool  `xml:"forwardStatus"`
	ForwardDN string `xml:"forwardDN"`
	ForwardTo string `xml:"forwardTo"`
	RingCount *int   `xml:"ringCount"`
}

// parseFeatureBody reads the changes in a SUBSCRIBE body. An empty body has
// none; unknown document roots are ignored; a malformed or incomplete
// document is errFeatureBody.
func parseFeatureBody(body []byte) ([]featChange, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	if len(body) > maxFeatureBody {
		return nil, fmt.Errorf("%w: too large", errFeatureBody)
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	var out []featChange
	for {
		var d featDoc
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errFeatureBody, err)
		}
		c := featChange{device: strings.TrimSpace(d.Device)}
		switch strings.ToLower(d.XMLName.Local) {
		case "setdonotdisturb", "donotdisturbevent":
			if d.DNDOn == nil {
				return nil, fmt.Errorf("%w: doNotDisturbOn missing", errFeatureBody)
			}
			c.dnd = d.DNDOn
		case "setforwarding", "callforwardingevent", "forwardingevent":
			switch strings.ToLower(strings.TrimSpace(d.Type)) {
			case "forwardimmediate":
				c.fwd = fwdImmediate
			case "forwardbusy":
				c.fwd = fwdBusy
			case "forwardnoans", "forwardnoanswer":
				c.fwd = fwdNoAnswer
			default:
				return nil, fmt.Errorf("%w: forwardingType %q", errFeatureBody, d.Type)
			}
			dn := cleanFeatureTarget(d.ForwardDN)
			if dn == "" {
				dn = cleanFeatureTarget(d.ForwardTo)
			}
			active := dn != ""
			switch {
			case d.Activate != nil:
				active = *d.Activate
			case d.Status != nil:
				active = *d.Status
			}
			target := ""
			if active {
				if !featureTargetRe.MatchString(dn) {
					return nil, fmt.Errorf("%w: forward target %q", errFeatureBody, dn)
				}
				target = dn
			}
			c.target = &target
			if d.RingCount != nil && c.fwd == fwdNoAnswer {
				if *d.RingCount < 1 || *d.RingCount > 20 {
					return nil, fmt.Errorf("%w: ringCount %d", errFeatureBody, *d.RingCount)
				}
				c.rings = d.RingCount
			}
		default:
			continue
		}
		out = append(out, c)
	}
}

// cleanFeatureTarget reduces "sip:2000@domain;user=phone" to "2000".
func cleanFeatureTarget(v string) string {
	v = strings.TrimSpace(v)
	for _, p := range []string{"sip:", "sips:", "tel:"} {
		v = strings.TrimPrefix(v, p)
	}
	if i := strings.IndexAny(v, "@;"); i >= 0 {
		v = v[:i]
	}
	return v
}

// ownFeatureExtension resolves the extension a feature-event SUBSCRIBE
// addresses: the authenticated device's own extension, named by number or by
// a device username of that extension. Anything else belongs to someone else.
func ownFeatureExtension(snap *snapshot.Snapshot, dev snapshot.Device, user string) (string, bool) {
	if dev.Extension == "" {
		return "", false
	}
	if user == dev.Extension {
		return dev.Extension, true
	}
	if d, ok := snap.DeviceByUsername(user); ok && d.Extension == dev.Extension {
		return dev.Extension, true
	}
	return "", false
}

// namesExtension reports whether the <device> a phone put in its document is
// the authenticated device's own extension (or one of its usernames).
func namesExtension(snap *snapshot.Snapshot, dev snapshot.Device, named string) bool {
	if named == "" {
		return true
	}
	_, ok := ownFeatureExtension(snap, dev, cleanFeatureTarget(named))
	return ok
}

// updateOf folds a phone's changes into one queued update.
func updateOf(ext string, changes []featChange) ExtUpdate {
	u := ExtUpdate{Extension: ext}
	for _, c := range changes {
		if c.dnd != nil {
			v := *c.dnd
			u.DND = &v
		}
		if c.target != nil {
			v := *c.target
			switch c.fwd {
			case fwdImmediate:
				u.ForwardAlways = &v
			case fwdBusy:
				u.ForwardBusy = &v
			case fwdNoAnswer:
				u.ForwardNoAnswer = &v
			}
		}
		if c.rings != nil {
			v := *c.rings
			u.NoAnswerRings = &v
		}
	}
	return u
}

// featureEvents renders the documents that bring a phone from sent (nil:
// nothing sent yet, send all) to cur, one per feature.
func featureEvents(ext string, sent *featState, cur featState) [][]byte {
	var out [][]byte
	if sent == nil || sent.DND != cur.DND {
		out = append(out, featureXML("DoNotDisturbEvent", ext,
			"<doNotDisturbOn>"+strconv.FormatBool(cur.DND)+"</doNotDisturbOn>"))
	}
	fwd := func(kind, oldT, newT string, rings int) {
		if sent != nil && oldT == newT && (kind != fwdNoAnswer || sent.Rings == cur.Rings) {
			return
		}
		inner := "<forwardingType>" + kind + "</forwardingType>" +
			"<forwardStatus>" + strconv.FormatBool(newT != "") + "</forwardStatus>" +
			"<forwardTo>" + xmlEscape(newT) + "</forwardTo>"
		if kind == fwdNoAnswer {
			inner += "<ringCount>" + strconv.Itoa(rings) + "</ringCount>"
		}
		out = append(out, featureXML("ForwardingEvent", ext, inner))
	}
	var old featState
	if sent != nil {
		old = *sent
	}
	fwd(fwdImmediate, old.Always, cur.Always, 0)
	fwd(fwdBusy, old.Busy, cur.Busy, 0)
	fwd(fwdNoAnswer, old.NoAns, cur.NoAns, cur.Rings)
	return out
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func featureXML(root, ext, inner string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\r\n" +
		"<" + root + ` xmlns="` + ecmaNamespace + `"><device>` + xmlEscape(ext) + "</device>" + inner +
		"</" + root + ">\r\n")
}

// featureChanges reads and authorises the changes in a feature-event
// SUBSCRIBE: 400 for a bad document, 403 when it names another extension.
// ok=false means the response was sent.
func (s *Server) featureChanges(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, dev snapshot.Device, ext string) ([]featChange, bool) {
	changes, err := parseFeatureBody(req.Body())
	if err != nil {
		s.log.Debug("as-feature-event body rejected", "device", dev.Username, "error", err)
		s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
		return nil, false
	}
	for _, c := range changes {
		if !namesExtension(snap, dev, c.device) {
			s.log.Warn("as-feature-event change for another extension refused", "device", dev.Username, "extension", dev.Extension, "named", c.device)
			s.respond(tx, req, sip.StatusForbidden, "Forbidden")
			return nil, false
		}
	}
	return changes, true
}

// handleFeatureSubscribe finishes a feature-event SUBSCRIBE after the
// common authentication and the extension check.
func (s *Server) handleFeatureSubscribe(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, ext string, changes []featChange, exp time.Duration) {
	e, _ := snap.Extension(ext)
	state := s.featStateOf(e)
	if len(changes) > 0 {
		upd := updateOf(ext, changes)
		if s.deps.Settings == nil || !s.deps.Settings.Apply(upd) {
			s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable")
			return
		}
		state = state.apply(upd)
	}
	sub, _ := s.upsertSub(req, eventFeature, ext, exp)
	if sub == nil {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	s.respond(tx, req, sip.StatusOK, "OK",
		sip.NewHeader("Expires", strconv.Itoa(int(exp/time.Second))),
		sip.NewHeader("Event", eventFeature),
		&sip.ToHeader{Address: *req.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: sub.localTag}}})
	s.m.PresenceSubscriptions.Set(float64(s.subCount()))
	// RFC 6665: every SUBSCRIBE, refresh included, is answered with a NOTIFY
	// carrying the full state.
	s.goBG(func() { s.sendFeatureNotifies(sub, state, int(exp/time.Second), true) })
}

// featureSubs lists the live feature-event subscriptions, optionally of one
// extension.
func (s *Server) featureSubs(ext string) []*subscription {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	var out []*subscription
	for _, sub := range s.subs {
		if sub.event == eventFeature && (ext == "" || sub.extension == ext) {
			out = append(out, sub)
		}
	}
	return out
}

// notifyFeatureChanges runs after every snapshot reload: each feature
// subscription whose extension no longer matches what its phone was last
// told gets the difference.
func (s *Server) notifyFeatureChanges(cur *snapshot.Snapshot) {
	for _, sub := range s.featureSubs("") {
		e, _ := cur.Extension(sub.extension)
		state := s.featStateOf(e)
		s.goBG(func() {
			s.subsMu.Lock()
			left := int(time.Until(sub.expires) / time.Second)
			s.subsMu.Unlock()
			s.sendFeatureNotifies(sub, state, left, false)
		})
	}
}

// sendFeatureNotifies sends the events that bring sub's phone to state: all
// of them when full, else only what differs from what was last sent. Sends
// of one subscription are serialised, so CSeq and content stay ordered.
func (s *Server) sendFeatureNotifies(sub *subscription, state featState, expires int, full bool) {
	defer contain(s.log, "feature notify")
	sub.notifyMu.Lock()
	defer sub.notifyMu.Unlock()
	var sent *featState
	if !full {
		s.subsMu.Lock()
		sent = sub.feat
		s.subsMu.Unlock()
	}
	// One NOTIFY per document: all four in one multipart body would not fit
	// a UDP datagram.
	for _, doc := range featureEvents(sub.extension, sent, state) {
		if !s.sendFeatureNotify(sub, featureContentType, doc, expires, "") {
			break // an unreachable phone does not time out four times over
		}
	}
	s.subsMu.Lock()
	sub.feat = &state
	s.subsMu.Unlock()
}

// sendFeatureTerminate ends a feature subscription with the terminal NOTIFY.
func (s *Server) sendFeatureTerminate(sub *subscription, reason string) {
	defer contain(s.log, "feature notify")
	sub.notifyMu.Lock()
	defer sub.notifyMu.Unlock()
	state := featState{}
	s.subsMu.Lock()
	if sub.feat != nil {
		state = *sub.feat
	}
	s.subsMu.Unlock()
	_ = s.sendFeatureNotify(sub, featureContentType, featureXML("DoNotDisturbEvent", sub.extension,
		"<doNotDisturbOn>"+strconv.FormatBool(state.DND)+"</doNotDisturbOn>"), 0, reason)
}

// sendFeatureNotify sends one in-dialog NOTIFY and reports whether the phone
// answered.
func (s *Server) sendFeatureNotify(sub *subscription, contentType string, body []byte, expires int, reason string) bool {
	s.subsMu.Lock()
	sub.cseq++
	seq := sub.cseq
	s.subsMu.Unlock()
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
	req.AppendHeader(sip.NewHeader("Event", eventFeature))
	if reason != "" {
		req.AppendHeader(sip.NewHeader("Subscription-State", "terminated;reason="+reason))
	} else {
		req.AppendHeader(sip.NewHeader("Subscription-State", "active;expires="+strconv.Itoa(max(expires, 0))))
	}
	req.AppendHeader(sip.NewHeader("Content-Type", contentType))
	req.SetBody(body)
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := s.do(ctx, req)
	if err != nil {
		s.log.Debug("feature NOTIFY failed", "device", sub.device, "error", err)
		return false
	}
	s.m.response(res.StatusCode)
	return true
}
