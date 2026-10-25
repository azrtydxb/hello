// Call pickup (phone-feature-parity spec S-50). Dialling the group-pickup
// code answers the oldest call ringing an extension in the dialler's pickup
// scope; the directed code plus an extension answers that extension's call
// if it is in scope; an INVITE with Replaces naming an early dialog does the
// same. The picked-up call's other ringing legs are cancelled, its caller is
// connected to the dialler through the caller's relay, and the two calls are
// linked like a retrieved parked call.
package sip

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// pickupReq hands a pickup to the setup goroutine of the call being picked
// up, the only one that may answer its caller.
type pickupReq struct {
	offer     media.AudioSDP
	taker     *call
	node      string // the picker's node and Call-ID when it is not here
	callID    string
	retriever string
	res       chan pickupRes
}

type pickupRes struct {
	ok     bool
	answer []byte
	code   int
	why    string
	callID string
}

// pickupWait bounds how long a picker waits for the call's setup goroutine.
const pickupWait = 10 * time.Second

// ringingExtensions lists the extensions a call still being set up is
// ringing (one per live phone leg).
func (c *call) ringingExtensions() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.winner != nil || c.setupClosed || c.isCancelled || c.pickedUp || c.page != nil || c.connected {
		return nil
	}
	var out []string
	for _, l := range c.legs {
		if l.trunk != nil || l.uri != nil || l.abandoned || l.stage != c.stageSeq {
			continue
		}
		if e := l.binding.Extension; e != "" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// claimPickup takes the call out of ring setup for a picker: after it, no
// fork can win and no forwarding or voicemail stage starts. False when the
// call is not (or no longer) ringing, is not anchored, or was claimed.
func (c *call) claimPickup() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.winner != nil || c.setupClosed || c.isCancelled || c.pickedUp || c.connected || !c.anchored ||
		c.relay == nil || c.page != nil || len(c.inv.Body()) == 0 {
		return false
	}
	c.setupClosed, c.pickedUp = true, true
	return true
}

// pickupBy runs a pickup of c for req: the claim is atomic, so of several
// pickers racing for the call exactly one gets it.
func (c *call) pickupBy(req *pickupReq) pickupRes {
	if !c.claimPickup() {
		return pickupRes{code: sip.StatusNotFound, why: "the call is no longer ringing"}
	}
	c.pickupCh <- req
	select {
	case r := <-req.res:
		return r
	case <-time.After(pickupWait):
		return pickupRes{code: sip.StatusRequestTimeout, why: "the call did not answer the pickup in time"}
	}
}

// answerPickup connects the caller of the picked-up call to the picker.
func (c *call) answerPickup(req *pickupReq) {
	s := c.s
	c.cancelForks(nil)
	c.mu.Lock()
	cancelled := c.isCancelled || closed(c.aborted)
	relay := c.relay
	c.mu.Unlock()
	fail := func(code int, why string) {
		req.res <- pickupRes{code: code, why: why}
		if cancelled {
			c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
			return
		}
		c.respondA(sip.StatusTemporarilyUnavailable, "Temporarily Unavailable")
		c.end(sip.StatusTemporarilyUnavailable, cdr.SideSystem, why, ResultUnavailable)
	}
	if cancelled {
		fail(sip.StatusRequestTerminated, "the caller hung up")
		return
	}
	off, err := media.ParseAudioSDP(c.inv.Body())
	if err != nil || relay == nil {
		fail(sip.StatusTemporarilyUnavailable, "the call cannot be handed over")
		return
	}
	// The picker's media: the relay's callee leg, in the codec the caller
	// and the picker share.
	relay.SetTarget(legCallee, udpAddr(req.offer.Address, req.offer.Port))
	ans := media.AnswerFor(off, req.offer)
	c.applyNegotiated(ans)
	takerAns := ans.Build(c.anchorHost, relay.LegPort(legCallee), "sendrecv")
	c.mu.Lock()
	c.connected = true
	c.answerTime = time.Now()
	c.link = &callLink{selfSide: cdr.SideCaller, byeSelf: c.byeA, relayOwner: c, relayLeg: legCaller,
		peer: req.taker, node: req.node, callID: req.callID}
	c.mu.Unlock()
	c.addTrace("Call picked up by " + req.retriever)
	c.publish()
	req.res <- pickupRes{ok: true, answer: takerAns, callID: c.callID}
	body := ans.Build(c.anchorHost, relay.LegPort(legCaller), "sendrecv")
	res := sip.NewResponseFromRequest(c.dss.InviteRequest, sip.StatusOK, "OK", body)
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res.AppendHeader(sip.NewHeader("Allow", allow))
	s.m.response(sip.StatusOK)
	if err := c.dss.WriteResponse(res); err != nil {
		s.log.Info("caller did not acknowledge the picked-up call", "correlation_id", c.id, "error", err)
		c.linkedHangup(cdr.SideSystem, "no ACK from caller")
		return
	}
	c.mu.Lock()
	if !c.ended {
		c.maxTimer = time.AfterFunc(s.cfg.MaxCallDuration, c.expire)
	}
	c.mu.Unlock()
}

// pickupCandidate is one ringing call a picker may take, local or remote.
type pickupCandidate struct {
	local *call
	live  livestate.Call
	start time.Time
	exts  []string
}

// pickupCandidates lists the calls ringing an extension in scope, oldest
// first. directed limits them to one extension.
func (s *Server) pickupCandidates(snap *snapshot.Snapshot, picker, directed string) []pickupCandidate {
	scope := snap.PickupScope(picker)
	allowed := func(exts []string) []string {
		var out []string
		for _, e := range exts {
			if slices.Contains(scope, e) && (directed == "" || e == directed) {
				out = append(out, e)
			}
		}
		return out
	}
	var out []pickupCandidate
	seen := map[string]bool{}
	s.mu.Lock()
	calls := make([]*call, 0, len(s.calls))
	for c := range s.calls {
		calls = append(calls, c)
	}
	s.mu.Unlock()
	for _, c := range calls {
		if c.callerNum == picker {
			continue
		}
		if exts := allowed(c.ringingExtensions()); len(exts) > 0 {
			out = append(out, pickupCandidate{local: c, start: c.start, exts: exts})
			seen[c.id] = true
		}
	}
	if s.deps.Park != nil {
		ctx, cancel := s.stateCtx()
		live, err := s.deps.State.Calls(ctx)
		cancel()
		if err == nil {
			for _, lc := range live {
				if lc.State != "ringing" || lc.Node == s.cfg.NodeID || seen[lc.ID] || lc.From == picker {
					continue
				}
				target := []string{lc.To}
				if _, members, ok := snap.RingGroup(lc.To); ok {
					target = target[:0]
					for _, m := range members {
						target = append(target, m.Extension)
					}
				}
				if exts := allowed(target); len(exts) > 0 {
					out = append(out, pickupCandidate{live: lc, start: lc.StartedAt, exts: exts})
				}
			}
		}
	}
	slices.SortStableFunc(out, func(a, b pickupCandidate) int { return a.start.Compare(b.start) })
	return out
}

// handlePickup serves the group and directed pickup codes dialled as a call.
func (c *call) handlePickup(tx sip.ServerTransaction, snap *snapshot.Snapshot, fc snapshot.FeatureCode, arg string) {
	directed := ""
	if fc.Action == ActionPickupDirected {
		if arg == "" {
			c.pickupFail(tx, sip.StatusNotFound, "directed pickup needs an extension")
			return
		}
		directed = arg
		if !snap.CanPickUp(c.callerNum, directed) {
			c.pickupFail(tx, sip.StatusForbidden, "extension "+directed+" is not in your pickup scope")
			return
		}
	}
	c.pickupFrom(tx, snap, c.s.pickupCandidates(snap, c.callerNum, directed))
}

func (c *call) pickupFail(tx sip.ServerTransaction, code int, why string) {
	c.s.respond(tx, c.inv, code, statusText(code))
	c.record(code, cdr.SideSystem, why, ResultNotFound)
	c.s.parkMetric("pickup", why)
}

// pickupFrom tries the candidates in order until one hands the call over.
func (c *call) pickupFrom(tx sip.ServerTransaction, snap *snapshot.Snapshot, cands []pickupCandidate) {
	s := c.s
	offer, err := media.ParseAudioSDP(c.inv.Body())
	if err != nil {
		c.pickupFail(tx, sip.StatusNotAcceptableHere, "pickup needs an SDP offer")
		return
	}
	lk := &callLink{selfSide: cdr.SideCaller, byeSelf: c.byeCallerRaw}
	for _, cand := range cands {
		var res pickupRes
		if cand.local != nil {
			res = cand.local.pickupBy(&pickupReq{offer: offer, taker: c, retriever: c.callerNum, res: make(chan pickupRes, 1)})
			if res.ok {
				lk.peer = cand.local
			}
		} else {
			rep, ok := s.parkCommand(cand.live.Node, livestate.ParkCommand{Op: livestate.ParkOpPickup,
				Correlation: cand.live.ID, Offer: string(c.inv.Body()), PeerCallID: c.callID, Retriever: c.callerNum})
			res = pickupRes{ok: ok, answer: []byte(rep.Answer), code: rep.Code, why: rep.Reason, callID: rep.CallID}
			if ok {
				lk.node, lk.callID = cand.live.Node, rep.CallID
			}
		}
		if !res.ok {
			continue
		}
		if !c.begin(c.inv, tx) {
			s.notifyPeer(c, lk)
			return
		}
		lk.answer = res.answer
		c.mu.Lock()
		c.link = lk
		c.mu.Unlock()
		c.addTrace(fmt.Sprintf("Picked up a call ringing %s", strings.Join(cand.exts, ", ")))
		s.parkMetric("pickup", "ok")
		c.answerSelf(res.answer)
		return
	}
	c.pickupFail(tx, sip.StatusNotFound, "nothing to pick up")
}

// handleReplacesPickup serves an INVITE with Replaces naming an early dialog
// (the pickup form some phones use): in scope, it takes the call.
func (c *call) handleReplacesPickup(tx sip.ServerTransaction, snap *snapshot.Snapshot, replaces string) {
	callID := replaces
	if i := strings.IndexByte(callID, ';'); i >= 0 {
		callID = callID[:i]
	}
	callID, _ = url.QueryUnescape(callID)
	ref, ok := c.s.lookup(callID)
	if !ok {
		c.pickupFail(tx, sip.StatusCallTransactionDoesNotExists, "the Replaces dialog does not exist")
		return
	}
	var cands []pickupCandidate
	for _, cand := range c.s.pickupCandidates(snap, c.callerNum, "") {
		if cand.local == ref.c {
			cands = append(cands, cand)
		}
	}
	if len(cands) == 0 {
		c.pickupFail(tx, sip.StatusForbidden, "the Replaces dialog is not in your pickup scope")
		return
	}
	c.pickupFrom(tx, snap, cands)
}

// --- cross-node commands and slot presence ---------------------------------

// parkCommand sends cmd to node and waits for its reply.
func (s *Server) parkCommand(node string, cmd livestate.ParkCommand) (livestate.ParkReply, bool) {
	cmd.ID, cmd.From = newID(), s.cfg.NodeID
	ctx, cancel := s.stateCtx()
	err := s.deps.Park.ParkSend(ctx, node, cmd)
	cancel()
	if err != nil {
		return livestate.ParkReply{Code: sip.StatusServiceUnavailable, Reason: "park store unavailable"}, false
	}
	deadline := time.Now().Add(parkCommandWait)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		rctx, rcancel := s.stateCtx()
		rep, ok, err := s.deps.Park.ParkGetReply(rctx, cmd.ID)
		rcancel()
		if err == nil && ok {
			return rep, rep.OK
		}
	}
	return livestate.ParkReply{}, false
}

// parkLoop serves the commands other nodes send this one and keeps the BLF
// subscriptions of park slots current when another node changes a slot.
func (s *Server) parkLoop(ctx context.Context) {
	defer contain(s.log, "park loop")
	poll := time.NewTicker(parkCommandPoll)
	defer poll.Stop()
	lastSync := time.Now()
	known := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
		if st, _ := s.state(); st != cluster.Ready {
			continue
		}
		for {
			sctx, cancel := s.stateCtx()
			cmd, ok, err := s.deps.Park.ParkTake(sctx, s.cfg.NodeID)
			cancel()
			if err != nil || !ok {
				break
			}
			s.bg.Go(func() {
				defer contain(s.log, "park command")
				s.runParkCommand(cmd)
			})
		}
		if time.Since(lastSync) >= parkSlotSyncEvery {
			lastSync = time.Now()
			s.syncSlots(known)
		}
	}
}

// runParkCommand does what another node asked of a call held here.
func (s *Server) runParkCommand(cmd livestate.ParkCommand) {
	rep := livestate.ParkReply{ID: cmd.ID}
	switch cmd.Op {
	case livestate.ParkOpBye:
		if ref, ok := s.lookup(cmd.CallID); ok {
			ref.c.linkedHangup(cdr.SideSystem, "the other party hung up")
		}
		return
	case livestate.ParkOpRetrieve:
		offer, err := media.ParseAudioSDP([]byte(cmd.Offer))
		ref, ok := s.lookup(cmd.CallID)
		switch {
		case err != nil:
			rep.Code, rep.Reason = sip.StatusNotAcceptableHere, "bad offer"
		case !ok:
			rep.Code, rep.Reason = sip.StatusGone, "the parked call is gone"
		default:
			ans, code, why, ok := ref.c.parkRetrieved(offer, cmd.Retriever, cmd.From, cmd.PeerCallID, nil)
			rep.OK, rep.Answer, rep.Code, rep.Reason, rep.CallID = ok, string(ans), code, why, ref.c.callID
		}
	case livestate.ParkOpPickup:
		offer, err := media.ParseAudioSDP([]byte(cmd.Offer))
		var target *call
		s.mu.Lock()
		for c := range s.calls {
			if c.id == cmd.Correlation {
				target = c
			}
		}
		s.mu.Unlock()
		switch {
		case err != nil:
			rep.Code, rep.Reason = sip.StatusNotAcceptableHere, "bad offer"
		case target == nil:
			rep.Code, rep.Reason = sip.StatusNotFound, "the call is no longer ringing"
		default:
			r := target.pickupBy(&pickupReq{offer: offer, node: cmd.From, callID: cmd.PeerCallID, retriever: cmd.Retriever,
				res: make(chan pickupRes, 1)})
			rep.OK, rep.Answer, rep.Code, rep.Reason, rep.CallID = r.ok, string(r.answer), r.code, r.why, r.callID
		}
	default:
		return
	}
	ctx, cancel := s.stateCtx()
	defer cancel()
	if err := s.deps.Park.ParkPutReply(ctx, rep); err != nil {
		s.log.Warn("could not post a park reply", "error", err)
	}
}

// slotChanged notifies the BLF subscribers of a park slot (this node's) that
// its occupancy changed.
func (s *Server) slotChanged(slot string) {
	s.goBG(func() {
		defer contain(s.log, "slot presence")
		ctx, cancel := s.stateCtx()
		defer cancel()
		_, occupied, err := s.deps.Park.ParkGet(ctx, slot)
		if err != nil {
			return
		}
		s.notifyExtension(slot, slotState(occupied))
	})
}

func slotState(occupied bool) string {
	if occupied {
		return StateOnCall
	}
	return StateIdle
}

// syncSlots compares the cluster's occupied slots with what this node last
// told its subscribers and notifies the differences, so a slot parked or
// retrieved on another node reaches the phones subscribed here.
func (s *Server) syncSlots(known map[string]bool) {
	ctx, cancel := s.stateCtx()
	list, err := s.deps.Park.ParkList(ctx)
	cancel()
	if err != nil {
		return
	}
	now := map[string]bool{}
	for _, p := range list {
		now[p.Slot] = true
	}
	for slot := range now {
		if !known[slot] {
			s.notifyExtension(slot, StateOnCall)
		}
	}
	for slot := range known {
		if !now[slot] {
			s.notifyExtension(slot, StateIdle)
		}
	}
	clear(known)
	for slot := range now {
		known[slot] = true
	}
}

// slotOccupied reports a slot's occupancy for a BLF subscription's first
// NOTIFY.
func (s *Server) slotOccupied(slot string) bool {
	if s.deps.Park == nil {
		return false
	}
	ctx, cancel := s.stateCtx()
	defer cancel()
	_, ok, _ := s.deps.Park.ParkGet(ctx, slot)
	return ok
}
