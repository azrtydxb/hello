// SIP MESSAGE (RFC 3428) text messaging. A MESSAGE from an authenticated
// device to an extension reaches every registered contact of that extension;
// to a ring or paging group it reaches every member; with no contact
// registered it is stored and delivered when a device registers, until its
// TTL passes. A trunk with messaging turned on may carry SMS over SIP both
// ways. The durable queue and history live in PostgreSQL (MessageQueue); a
// row is claimed by exactly one node, so a message is delivered once
// whatever the number of hello-sip nodes.
//
// The sender of a stored message is always the authenticated identity (the
// device's extension), never the From header, so a phone cannot message as
// another extension. A request is routed only to registered contacts and
// configured trunks, never to a URI the sender chose, so Hello is not a
// relay. Message bodies are never logged.
package sip

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Limits and timings.
const (
	// MaxMessageBytes is the largest message body accepted.
	MaxMessageBytes = 700
	// messageClaimBatch is how many messages one claim takes.
	messageClaimBatch = 100
	// messageDeliverConcurrency bounds the deliveries in flight per node.
	messageDeliverConcurrency = 16
)

// udpMessageLimit is the largest request (before the Via the client adds)
// that goes out as one UDP datagram: the transport refuses a packet within
// 200 bytes of the MTU.
var udpMessageLimit = sip.UDPMTUSize - 200 - 150

// messageDeliverTimeout bounds one MESSAGE exchange (a digest challenge
// included); messageInlineWait is how long the sender's 200 waits for an
// immediate delivery before it settles for 202; messageSweepInterval is how
// often the queue is looked at besides wake-ups; messageMaintainInterval is
// how often expired and old messages are cleaned up. Variables so tests can
// shorten them.
var (
	messageDeliverTimeout   = 8 * time.Second
	messageInlineWait       = 4 * time.Second
	messageSweepInterval    = 15 * time.Second
	messageMaintainInterval = time.Minute
)

// Message targets, scopes, senders and delivery results; they are the
// values the store keeps.
const (
	MsgTargetExtension = "extension"
	MsgTargetRing      = "ring_group"
	MsgTargetPaging    = "paging_group"
	MsgTargetExternal  = "external"
	MsgSenderExt       = "extension"
	MsgSenderExternal  = "external"

	MsgResultDelivered     = "delivered"
	MsgResultNotRegistered = "not_registered"
	MsgResultRetry         = "retry"
	MsgResultFailed        = "failed"
)

// Errors a MessageQueue returns for a refused send.
var (
	ErrMsgTarget    = errors.New("sip: message target not found")
	ErrMsgForbidden = errors.New("sip: sender may not message this target")
	ErrMsgRate      = errors.New("sip: message rate limit exceeded")
	ErrMsgDisabled  = errors.New("sip: messaging is disabled")
	ErrMsgTooLarge  = errors.New("sip: message too large")
	ErrMsgNoTrunk   = errors.New("sip: no trunk has messaging enabled")
)

// MessageSend is one message a node hands to the queue.
type MessageSend struct {
	Sender      string
	SenderKind  string // MsgSenderExt or MsgSenderExternal
	SenderName  string
	TargetKind  string
	Target      string
	ContentType string
	Body        []byte
	TrunkID     int64
	// ClaimBy, for a single recipient, stores the message already claimed
	// by this node so it can deliver at once.
	ClaimBy string
}

// MessageJob is a claimed message to deliver.
type MessageJob struct {
	ID            int64
	Sender        string
	SenderKind    string
	SenderName    string
	Recipient     string
	RecipientKind string // MsgTargetExtension or MsgTargetExternal
	Scope         string // direct, ring_group, paging_group or broadcast
	ScopeRef      string
	ContentType   string
	Body          []byte
	TrunkID       int64
	Attempts      int
	CreatedAt     time.Time
}

// MessageSent is what the queue stored.
type MessageSent struct {
	Recipients int
	// Jobs holds the messages stored already claimed (MessageSend.ClaimBy).
	Jobs []MessageJob
}

// MessageQueue is the durable message queue (PostgreSQL).
type MessageQueue interface {
	// Watch calls wake for every queued message, and once after every
	// (re)connect, until ctx ends.
	Watch(ctx context.Context, wake func())
	// Send stores a message for the recipients of its target.
	Send(ctx context.Context, in MessageSend) (MessageSent, error)
	// Pending lists the extensions with deliverable undelivered messages
	// and whether any external message waits.
	Pending(ctx context.Context) (extensions []string, external bool, err error)
	// Claim takes pending messages for node: those to the given extensions
	// and, if external, those to external numbers. A message is claimed by
	// one node only.
	Claim(ctx context.Context, node string, extensions []string, external bool, limit int) ([]MessageJob, error)
	// Finish writes a claimed message's result (MsgResult*).
	Finish(ctx context.Context, id int64, result, detail string) error
	// Maintain expires old undelivered messages and purges the history.
	Maintain(ctx context.Context) error
}

var contentTypeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*(\s*;.*)?$`)

// Message request outcomes for hello_sip_messages_total.
const (
	msgAccepted     = "accepted"
	msgDeliveredNow = "delivered"
	msgBadAuth      = "rejected_auth"
	msgForbidden    = "rejected_forbidden"
	msgNotFound     = "rejected_not_found"
	msgTooLarge     = "rejected_too_large"
	msgRateLimited  = "rejected_rate"
	msgInvalid      = "rejected_invalid"
	msgDisabled     = "rejected_disabled"
	msgUnavailable  = "unavailable"
	msgRelayed      = "relayed"
)

// handleMessage is the MESSAGE method handler.
func (s *Server) handleMessage(req *sip.Request, tx sip.ServerTransaction) {
	src := "phone"
	count := func(outcome string) { s.m.MessagesIn.WithLabelValues(src, outcome).Inc() }
	if s.refuseIfNotReady(req, tx) {
		count(msgUnavailable)
		return
	}
	if s.deps.Messages == nil {
		count(msgUnavailable)
		s.unavailable(tx, req)
		return
	}
	if isInDialog(req) {
		// Hello does not relay messages inside a call's dialog.
		count(msgInvalid)
		s.respond(tx, req, sip.StatusMethodNotAllowed, "Method Not Allowed", sip.NewHeader("Allow", allow))
		return
	}
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		count(msgUnavailable)
		s.unavailable(tx, req)
		return
	}
	// The same source and credential checks as an INVITE: a request without
	// credentials is a trunk's only from a trunk's source address; one that
	// claims to be a phone is challenged; anything else is refused.
	var (
		trunk *routing.Trunk
		dev   snapshot.Device
	)
	if req.GetHeader("Authorization") == nil {
		if t, ok := s.trunkSource(req, snap); ok {
			trunk, src = t, "trunk"
		} else if !s.looksLikePhone(req, snap) {
			count(msgBadAuth)
			s.authFailed(tx, req, s.clientIP(req), "MESSAGE from a source that is neither a trunk nor a phone", false)
			return
		}
	}
	if trunk == nil {
		var ok bool
		if dev, ok = s.verify(req, tx, snap); !ok {
			count(msgBadAuth)
			return
		}
	}
	ct, body, code, reason := messageContent(req)
	if code != 0 {
		if code == sip.StatusRequestEntityTooLarge {
			count(msgTooLarge)
		} else {
			count(msgInvalid)
		}
		s.respond(tx, req, code, reason)
		return
	}
	// A typing indication (RFC 3994) is only worth anything now: relayed to
	// the recipient's contacts if registered, never stored.
	typing := strings.HasPrefix(strings.ToLower(ct), "application/im-iscomposing")
	target := req.Recipient.User
	if trunk != nil {
		s.inboundTrunkMessage(req, tx, snap, trunk, ct, body, count)
		return
	}
	// Who is speaking is the authenticated device, not the From header: a
	// From naming anyone else is a spoof attempt.
	if f := req.From(); f != nil && f.Address.User != "" && f.Address.User != dev.Extension && f.Address.User != dev.Username {
		count(msgForbidden)
		s.log.Info("MESSAGE refused: From does not match the authenticated device", "device", dev.Username, "source", s.clientSource(req))
		s.respond(tx, req, sip.StatusForbidden, "Forbidden")
		return
	}
	send := MessageSend{
		Sender: dev.Extension, SenderKind: MsgSenderExt, SenderName: dev.ExtensionName,
		Target: target, ContentType: ct, Body: body,
	}
	switch {
	case snap.HasExtension(target):
		send.TargetKind = MsgTargetExtension
	case isPagingCode(snap, target):
		send.TargetKind = MsgTargetPaging
		if g, ok := snap.PagingGroup(target); ok {
			send.Target = g.Code
		}
	case isRingGroup(snap, target):
		send.TargetKind = MsgTargetRing
	default:
		// An external number: only through a trunk with messaging on.
		dec, ok := s.messagingRoute(snap, dev.Extension, target)
		switch {
		case ok:
		case dec.Kind == routing.KindReject && dec.RejectCode == sip.StatusNotFound:
			// No extension, group or route by that name.
			count(msgNotFound)
			s.respond(tx, req, sip.StatusNotFound, "Not Found")
			return
		default:
			count(msgForbidden)
			s.respond(tx, req, sip.StatusForbidden, "Forbidden")
			return
		}
		send.TargetKind = MsgTargetExternal
	}
	if typing {
		s.relayTyping(req, tx, snap, dev, send, count)
		return
	}
	// A single online recipient is delivered to inline and answered 200;
	// everything else is stored and answered 202.
	if send.TargetKind == MsgTargetExtension && s.isOnline(snap, target) {
		send.ClaimBy = s.cfg.NodeID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	sent, err := s.deps.Messages.Send(ctx, send)
	cancel()
	if err != nil {
		s.messageSendFailed(req, tx, err, count)
		return
	}
	if len(sent.Jobs) == 1 {
		done := make(chan string, 1)
		job := sent.Jobs[0]
		s.goBG(func() { done <- s.deliverMessage(job) })
		select {
		case res := <-done:
			if res == MsgResultDelivered {
				count(msgDeliveredNow)
				s.respond(tx, req, sip.StatusOK, "OK")
				return
			}
		case <-time.After(messageInlineWait):
		}
	}
	count(msgAccepted)
	s.respond(tx, req, sip.StatusAccepted, "Accepted")
}

// messageContent reads the content type and body, or the response to give.
func messageContent(req *sip.Request) (ct string, body []byte, code int, reason string) {
	body = req.Body()
	if len(body) > MaxMessageBytes {
		return "", nil, sip.StatusRequestEntityTooLarge, "Message Too Large"
	}
	if len(body) == 0 {
		return "", nil, sip.StatusBadRequest, "Bad Request"
	}
	h := req.ContentType()
	if h == nil {
		return "", nil, sip.StatusBadRequest, "Content-Type Required"
	}
	ct = strings.TrimSpace(h.Value())
	if len(ct) > 128 || !contentTypeRe.MatchString(ct) {
		return "", nil, sip.StatusBadRequest, "Bad Content-Type"
	}
	return ct, body, 0, ""
}

func isPagingCode(snap *snapshot.Snapshot, target string) bool {
	_, ok := snap.PagingGroup(target)
	return ok
}

func isRingGroup(snap *snapshot.Snapshot, target string) bool {
	_, _, ok := snap.RingGroup(target)
	return ok
}

// messageSendFailed answers a send the queue refused.
func (s *Server) messageSendFailed(req *sip.Request, tx sip.ServerTransaction, err error, count func(string)) {
	switch {
	case errors.Is(err, ErrMsgTarget):
		count(msgNotFound)
		s.respond(tx, req, sip.StatusNotFound, "Not Found")
	case errors.Is(err, ErrMsgForbidden), errors.Is(err, ErrMsgNoTrunk):
		count(msgForbidden)
		s.respond(tx, req, sip.StatusForbidden, "Forbidden")
	case errors.Is(err, ErrMsgRate):
		count(msgRateLimited)
		s.respond(tx, req, 429, "Too Many Requests", sip.NewHeader("Retry-After", "30"))
	case errors.Is(err, ErrMsgDisabled):
		count(msgDisabled)
		s.respond(tx, req, sip.StatusServiceUnavailable, "Messaging Disabled")
	case errors.Is(err, ErrMsgTooLarge):
		count(msgTooLarge)
		s.respond(tx, req, sip.StatusRequestEntityTooLarge, "Message Too Large")
	default:
		s.log.Warn("message queue unavailable", "error", err)
		count(msgUnavailable)
		s.unavailable(tx, req)
	}
}

// inboundTrunkMessage handles a MESSAGE from a trunk's source address: SMS
// over SIP, accepted only from a trunk with messaging on, and only for an
// extension an inbound route (by DID) names.
func (s *Server) inboundTrunkMessage(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot,
	t *routing.Trunk, ct string, body []byte, count func(string),
) {
	if !t.Messaging {
		count(msgForbidden)
		s.respond(tx, req, sip.StatusForbidden, "Forbidden")
		return
	}
	from := req.From().Address.User
	// A carrier cannot present itself as an internal extension, or as the
	// system: the phone would show the message as an internal one.
	if from == "" || snap.HasExtension(from) || !validSenderNumber(from) {
		count(msgForbidden)
		s.log.Info("trunk MESSAGE refused: sender is not a usable external number", "trunk", t.Name)
		s.respond(tx, req, sip.StatusForbidden, "Forbidden")
		return
	}
	dec := s.decide(snap.Routing(), routing.Call{
		FromTrunk: t.ID, Number: req.Recipient.User, CallerID: from,
		SIPDomain: req.Recipient.Host, Header: headerOf(req), At: time.Now(),
	})
	if dec.Kind != routing.KindInbound || dec.Extension == "" || !snap.HasExtension(dec.Extension) {
		count(msgNotFound)
		s.respond(tx, req, sip.StatusNotFound, "Not Found")
		return
	}
	if strings.HasPrefix(strings.ToLower(ct), "application/im-iscomposing") {
		// A carrier's typing indication is dropped: nothing to relay.
		count(msgRelayed)
		s.respond(tx, req, sip.StatusOK, "OK")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := s.deps.Messages.Send(ctx, MessageSend{
		Sender: from, SenderKind: MsgSenderExternal, SenderName: strings.TrimSpace(req.From().DisplayName),
		TargetKind: MsgTargetExtension, Target: dec.Extension, ContentType: ct, Body: body, TrunkID: t.ID,
	})
	cancel()
	if err != nil {
		s.messageSendFailed(req, tx, err, count)
		return
	}
	count(msgAccepted)
	s.respond(tx, req, sip.StatusAccepted, "Accepted")
}

var senderNumberRe = regexp.MustCompile(`^\+?[0-9*#]{2,32}$`)

func validSenderNumber(n string) bool { return senderNumberRe.MatchString(n) }

// relayTyping delivers a typing indication to the target extension's
// registered contacts, without storing it.
func (s *Server) relayTyping(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, dev snapshot.Device,
	send MessageSend, count func(string),
) {
	if send.TargetKind == MsgTargetExtension && snap.HasExtension(send.Target) {
		job := MessageJob{
			Sender: send.Sender, SenderKind: send.SenderKind, SenderName: send.SenderName,
			Recipient: send.Target, RecipientKind: MsgTargetExtension, Scope: "direct",
			ContentType: send.ContentType, Body: send.Body, CreatedAt: time.Now(),
		}
		s.goBG(func() {
			ctx, cancel := context.WithTimeout(context.Background(), messageDeliverTimeout)
			defer cancel()
			_, _, _ = s.sendToExtension(ctx, snap, job)
		})
	}
	count(msgRelayed)
	s.respond(tx, req, sip.StatusOK, "OK")
}

// messagingRoute decides whether extension ext may text the external number
// and through which trunk: its outbound route must reach a trunk with
// messaging turned on.
func (s *Server) messagingRoute(snap *snapshot.Snapshot, ext, number string) (routing.Decision, bool) {
	if !senderNumberRe.MatchString(number) {
		return routing.Decision{}, false
	}
	dec := s.decide(snap.Routing(), routing.Call{
		FromExtension: ext, Number: number, CallerID: ext, At: time.Now(),
	})
	if dec.Kind != routing.KindOutbound || dec.Emergency {
		return dec, false
	}
	for _, c := range dec.Candidates {
		if c.Trunk != nil && c.Trunk.Messaging {
			return dec, true
		}
	}
	return dec, false
}

// isOnline reports whether any device of the extension has a binding.
func (s *Server) isOnline(snap *snapshot.Snapshot, ext string) bool {
	for _, d := range snap.DevicesForExtension(ext) {
		ctx, cancel := s.stateCtx()
		bs, err := s.deps.State.Bindings(ctx, s.aor(d.Username))
		cancel()
		if err == nil && len(bs) > 0 {
			return true
		}
	}
	return false
}

// kickMessages asks the delivery loop to deliver what is waiting for ext, a
// device of which just registered.
func (s *Server) kickMessages(ext string) {
	if s.deps.Messages == nil || ext == "" {
		return
	}
	s.msgMu.Lock()
	s.msgKick[ext] = struct{}{}
	s.msgMu.Unlock()
	select {
	case s.msgWake <- struct{}{}:
	default:
	}
}

// messageLoop delivers queued messages: when one is queued (LISTEN), when a
// device registers, and on a slow sweep, so a message is never stranded.
func (s *Server) messageLoop(ctx context.Context) {
	wake := func() {
		select {
		case s.msgWake <- struct{}{}:
		default:
		}
	}
	s.bg.Go(func() {
		s.deps.Messages.Watch(ctx, func() { s.msgMu.Lock(); s.msgSweep = true; s.msgMu.Unlock(); wake() })
	})
	sweep := time.NewTicker(messageSweepInterval)
	defer sweep.Stop()
	maintain := time.NewTicker(messageMaintainInterval)
	defer maintain.Stop()
	sem := make(chan struct{}, messageDeliverConcurrency)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-sweep.C:
			s.msgMu.Lock()
			s.msgSweep = true
			s.msgMu.Unlock()
		case <-maintain.C:
			mctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := s.deps.Messages.Maintain(mctx); err != nil && ctx.Err() == nil {
				s.log.Warn("message maintenance failed", "error", err)
			}
			cancel()
			continue
		case <-s.msgWake:
		}
		s.msgMu.Lock()
		kicked := make([]string, 0, len(s.msgKick))
		for e := range s.msgKick {
			kicked = append(kicked, e)
		}
		clear(s.msgKick)
		sweepNow := s.msgSweep
		s.msgSweep = false
		s.msgMu.Unlock()
		s.runMessageClaim(ctx, kicked, sweepNow, sem)
	}
}

// runMessageClaim claims what can be delivered now and hands each message to
// a delivery goroutine. kicked extensions have just registered; with sweep,
// every extension that has waiting messages and a registered device is
// tried too.
func (s *Server) runMessageClaim(ctx context.Context, kicked []string, sweep bool, sem chan struct{}) {
	defer contain(s.log, "message claim")
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	exts := map[string]bool{}
	for _, e := range kicked {
		exts[e] = true
	}
	external := false
	if sweep {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		pend, ext, err := s.deps.Messages.Pending(cctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("list pending messages", "error", err)
			}
			return
		}
		external = ext
		for _, e := range pend {
			if !exts[e] && s.isOnline(snap, e) {
				exts[e] = true
			}
		}
	}
	list := make([]string, 0, len(exts))
	for e := range exts {
		list = append(list, e)
	}
	for {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		jobs, err := s.deps.Messages.Claim(cctx, s.cfg.NodeID, list, external, messageClaimBatch)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("claim messages", "error", err)
			}
			return
		}
		// One goroutine per recipient delivers that recipient's messages in
		// order; different recipients go in parallel.
		var order []string
		byRecipient := map[string][]MessageJob{}
		for _, j := range jobs {
			if _, ok := byRecipient[j.Recipient]; !ok {
				order = append(order, j.Recipient)
			}
			byRecipient[j.Recipient] = append(byRecipient[j.Recipient], j)
		}
		for _, rcpt := range order {
			batch := byRecipient[rcpt]
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			s.goBG(func() {
				defer func() { <-sem }()
				for _, j := range batch {
					s.deliverMessage(j)
				}
			})
		}
		if len(jobs) < messageClaimBatch {
			return
		}
	}
}

// deliverMessage delivers one claimed message and writes its result; it
// returns the result.
func (s *Server) deliverMessage(j MessageJob) string {
	defer contain(s.log, "message delivery")
	snap := s.deps.Snapshots.Current()
	ctx, cancel := context.WithTimeout(context.Background(), messageDeliverTimeout)
	defer cancel()
	var (
		res, detail string
		via         = "phone"
	)
	switch {
	case snap == nil:
		res, detail = MsgResultRetry, "no configuration loaded"
	case j.RecipientKind == MsgTargetExternal:
		via = "trunk"
		res, detail = s.sendExternal(ctx, snap, j)
	default:
		res, detail, _ = s.sendToExtension(ctx, snap, j)
	}
	s.m.MessageDeliveries.WithLabelValues(via, res).Inc()
	fctx, fcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer fcancel()
	if err := s.deps.Messages.Finish(fctx, j.ID, res, detail); err != nil {
		s.log.Warn("record message result", "message", j.ID, "error", err)
	}
	return res
}

// sendToExtension sends the message to every registered contact of every
// device of the recipient extension.
func (s *Server) sendToExtension(ctx context.Context, snap *snapshot.Snapshot, j MessageJob) (result, detail string, contacts int) {
	type target struct {
		dev snapshot.Device
		b   livestate.Binding
	}
	var targets []target
	for _, d := range snap.DevicesForExtension(j.Recipient) {
		sctx, cancel := s.stateCtx()
		bs, err := s.deps.State.Bindings(sctx, s.aor(d.Username))
		cancel()
		if err != nil {
			return MsgResultRetry, "registrations unavailable", 0
		}
		for _, b := range bs {
			targets = append(targets, target{d, b})
		}
	}
	if len(targets) == 0 {
		return MsgResultNotRegistered, "not registered", 0
	}
	codes := make([]int, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			defer contain(s.log, "message contact")
			codes[i] = s.messageToContact(ctx, snap, j, t.dev, t.b)
		})
	}
	wg.Wait()
	delivered, retry := 0, 0
	worst := 0
	for _, c := range codes {
		switch {
		case c/100 == 2:
			delivered++
		case permanentMessageFailure(c):
			worst = c
		default:
			retry++
			worst = c
		}
	}
	switch {
	case delivered > 0:
		return MsgResultDelivered, fmt.Sprintf("delivered to %d of %d contacts", delivered, len(targets)), len(targets)
	case retry > 0:
		return MsgResultRetry, "phone answered " + codeText(worst), len(targets)
	}
	return MsgResultFailed, "phone refused: " + codeText(worst), len(targets)
}

func codeText(c int) string {
	if c <= 0 {
		return "no answer"
	}
	if c == sip.StatusRequestEntityTooLarge {
		return "413 (too large for one UDP packet)"
	}
	return strconv.Itoa(c)
}

// permanentMessageFailure is a final answer that asking again will not
// change.
func permanentMessageFailure(code int) bool {
	switch code {
	case 400, 403, 405, 413, 415, 488, 606:
		return true
	}
	return false
}

// messageToContact sends the MESSAGE to one binding, answering the phone's
// digest challenge once from the device's stored HA1, and returns the final
// status (0: no answer or an error).
func (s *Server) messageToContact(ctx context.Context, snap *snapshot.Snapshot, j MessageJob, dev snapshot.Device, b livestate.Binding) int {
	var target sip.Uri
	raw := strings.TrimSuffix(strings.TrimPrefix(b.ContactURI, "<"), ">")
	if err := sip.ParseUri(raw, &target); err != nil {
		return 0
	}
	req := sip.NewRequest(sip.MESSAGE, target)
	from := sip.Uri{Scheme: "sip", User: fromUser(j), Host: s.cfg.Domain}
	req.AppendHeader(&sip.FromHeader{
		Address: from, DisplayName: messageDisplayName(j),
		Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}},
	})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: j.Recipient, Host: s.cfg.Domain}})
	callID := sip.CallIDHeader(newID())
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.MESSAGE})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Date", j.CreatedAt.UTC().Format(time.RFC1123)))
	req.AppendHeader(sip.NewHeader("Content-Type", j.ContentType))
	req.SetBody(j.Body)
	req.SetTransport("UDP")
	if err := s.routeToBinding(req, b); err != nil {
		return 0
	}
	if len(req.String()) > udpMessageLimit {
		return sip.StatusRequestEntityTooLarge
	}
	res, err := s.do(ctx, req)
	if err != nil {
		return 0
	}
	s.m.response(res.StatusCode)
	if edgeRefused(res) {
		return edgeRefusedCode
	}
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		hdr, authHdr := "WWW-Authenticate", "Authorization"
		if res.StatusCode == sip.StatusProxyAuthRequired {
			hdr, authHdr = "Proxy-Authenticate", "Proxy-Authorization"
		}
		cred, err := answerChallenge(res.GetHeaders(hdr), req.Method.String(), req.Recipient.String(), dev.Username, dev.Realm, dev.HA1MD5, dev.HA1SHA256)
		if err != nil {
			return res.StatusCode
		}
		req.RemoveHeader(authHdr)
		req.AppendHeader(sip.NewHeader(authHdr, cred.String()))
		req.CSeq().SeqNo++
		req.RemoveHeader("Via")
		if res, err = s.do(ctx, req); err != nil {
			return 0
		}
		s.m.response(res.StatusCode)
		if edgeRefused(res) {
			return edgeRefusedCode
		}
	}
	return res.StatusCode
}

// edgeRefusedCode is what messageToContact reports when the edge proxy of
// the node the phone registered on refused the MESSAGE: it never reached the
// phone, so it is not a permanent failure and delivering it again cannot
// duplicate it.
const edgeRefusedCode = sip.StatusServiceUnavailable

// edgeRefused reports whether res is an edge proxy's refusal (see
// edgeRefusedHeader) rather than the phone's own answer.
func edgeRefused(res *sip.Response) bool {
	return res.StatusCode == sip.StatusForbidden && res.GetHeader(edgeRefusedHeader) != nil
}

// fromUser is the user part a recipient sees: the sender's extension or
// number, so a reply goes back to it. The system sender is "hello".
func fromUser(j MessageJob) string {
	if j.SenderKind == "system" || j.Sender == "" {
		return "hello"
	}
	return j.Sender
}

// messageDisplayName is the From display name: the sender's name and, for a
// group message, the group it was sent to. Quotes and control characters
// are removed so it cannot break out of the header.
func messageDisplayName(j MessageJob) string {
	name := strings.TrimSpace(j.SenderName)
	if name == "" && j.SenderKind == "system" {
		name = "Announcement"
	}
	switch j.Scope {
	case "ring_group", "paging_group":
		name += " (" + j.ScopeRef + ")"
	case "broadcast":
		name += " (everyone)"
	}
	if j.SenderKind == MsgSenderExternal {
		if name == "" {
			name = j.Sender
		}
		name = "SMS " + name
	}
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == 0x7f {
			return -1
		}
		return r
	}, name)
	clean = strings.TrimSpace(clean)
	if len(clean) > 64 {
		clean = clean[:64]
	}
	return clean
}

// sendExternal sends the message to an external number through a trunk with
// messaging on: the sender's outbound route picks the trunks (a system
// message takes the first usable messaging trunk).
func (s *Server) sendExternal(ctx context.Context, snap *snapshot.Snapshot, j MessageJob) (result, detail string) {
	type attempt struct {
		t       routing.Trunk
		dsts    []routing.Destination
		number  string
		callerD string
	}
	var attempts []attempt
	rs := snap.Routing()
	if j.SenderKind == MsgSenderExt {
		dec := s.decide(rs, routing.Call{FromExtension: j.Sender, Number: j.Recipient, CallerID: j.Sender, At: time.Now()})
		if dec.Kind != routing.KindOutbound || dec.Emergency {
			return MsgResultFailed, "no outbound route for the number"
		}
		for _, c := range dec.Candidates {
			if c.Trunk != nil && c.Trunk.Messaging {
				attempts = append(attempts, attempt{*c.Trunk, c.Destinations, dec.Number, dec.CallerID})
			}
		}
	} else {
		for _, t := range rs.Config.Trunks {
			if !t.Enabled || !t.Messaging {
				continue
			}
			if _, bad := rs.Misconfigured[t.ID]; bad {
				continue
			}
			attempts = append(attempts, attempt{t, orderedDestinations(t), j.Recipient, t.DefaultCallerID})
		}
	}
	if len(attempts) == 0 {
		return MsgResultFailed, "no trunk with messaging enabled"
	}
	last := 0
	for _, a := range attempts {
		for _, d := range a.dsts {
			code := s.messageViaTrunk(ctx, a.t, d, j, a.number, a.callerD)
			if code/100 == 2 {
				return MsgResultDelivered, "accepted by trunk " + a.t.Name
			}
			if permanentMessageFailure(code) || code == 404 {
				return MsgResultFailed, fmt.Sprintf("trunk %s refused: %s", a.t.Name, codeText(code))
			}
			last = code
		}
	}
	return MsgResultRetry, "trunk answered " + codeText(last)
}

func orderedDestinations(t routing.Trunk) []routing.Destination {
	out := append([]routing.Destination(nil), t.Destinations...)
	for i := 1; i < len(out); i++ {
		for k := i; k > 0 && out[k].Priority < out[k-1].Priority; k-- {
			out[k], out[k-1] = out[k-1], out[k]
		}
	}
	return out
}

// messageViaTrunk sends the MESSAGE to one destination of a trunk, answering
// the carrier's digest challenge with the trunk credentials.
func (s *Server) messageViaTrunk(ctx context.Context, t routing.Trunk, d routing.Destination, j MessageJob, number, callerID string) int {
	domain := trunkDomain(t)
	if domain == "" {
		return 0
	}
	if callerID == "" {
		callerID = t.Username
	}
	if callerID == "" {
		callerID = j.Sender
	}
	to := sip.Uri{Scheme: "sip", User: number, Host: domain}
	req := sip.NewRequest(sip.MESSAGE, to)
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: callerID, Host: domain},
		Params:  sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}},
	})
	req.AppendHeader(&sip.ToHeader{Address: to})
	callID := sip.CallIDHeader(newID())
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.MESSAGE})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Content-Type", j.ContentType))
	req.SetBody(j.Body)
	req.SetTransport("UDP")
	req.SetDestination(s.trunks.destAddr(d))
	res, err := s.do(ctx, req)
	if err != nil {
		return 0
	}
	s.m.response(res.StatusCode)
	if (res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired) && t.Password != "" {
		res, err = s.doDigest(ctx, req, res, sipgo.DigestAuth{Username: t.Username, Password: t.Password})
		if err != nil {
			return 0
		}
		s.m.response(res.StatusCode)
	}
	return res.StatusCode
}
