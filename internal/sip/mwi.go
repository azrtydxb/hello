// Message waiting indication (RFC 3842, spec S-1): phones SUBSCRIBE to
// Event: message-summary for their own mailbox and get a NOTIFY on every
// change (new, heard, deleted). Contacts holding no subscription get an
// unsolicited NOTIFY instead. Subscriptions live in this node's memory, like
// the dialog (BLF) subscriptions they share a store with; a phone
// re-SUBSCRIBEs after a node restart or failover, and the initial NOTIFY
// carries the current counts.
package sip

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/emiago/sipgo/sip"
)

// hello_mwi_notify_total kinds.
const (
	MWIInitial     = "initial"     // answer to a fresh SUBSCRIBE
	MWIRefresh     = "refresh"     // answer to a refreshing SUBSCRIBE
	MWIChange      = "change"      // new, heard or deleted message
	MWITerminate   = "terminate"   // unsubscribe or expiry
	MWIUnsolicited = "unsolicited" // contact without a subscription
)

// mwiPollInterval is how often subscribed mailboxes are re-read. Changes made
// through the control-plane API reach this process as mailbox events and are
// NOTIFYed at once; the re-read is only the safety net for an event lost
// while the listener reconnects. A variable so tests can shorten it.
var mwiPollInterval = 60 * time.Second

// mailboxChanged handles a mailbox event from the control plane: box 0 means
// anything may have changed (the listener just (re)connected), otherwise the
// owner of that box is NOTIFYed. Counts are read fresh, and a subscription
// whose phone already has them is skipped.
func (s *Server) mailboxChanged(box int64) {
	if box == 0 {
		s.goBG(s.mwiPollOnce)
		return
	}
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	if ext, ok := snap.ExtensionByBox(box); ok {
		s.notifyMWI(ext)
	}
}

// mwiBody renders the application/simple-message-summary document for one
// mailbox (RFC 3842 §5.2).
func mwiBody(ext, domain string, unread, total int) string {
	return fmt.Sprintf("Messages-Waiting: %s\r\nMessage-Account: sip:%s@%s\r\nVoice-Message: %d/%d (%d/%d)\r\n",
		yesNo(unread > 0), ext, domain, unread, total, unread, total)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// mwiCounts reads the extension's mailbox counts; no box or no store means
// an empty mailbox, which still answers a subscription.
func (s *Server) mwiCounts(ext string) (unread, total int, err error) {
	box := s.boxIDFor(ext)
	if s.deps.Voicemails == nil || box == 0 {
		return 0, 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*s.cfg.StateTimeout)
	defer cancel()
	return s.deps.Voicemails.MessageCounts(ctx, box)
}

// notifyMWI reacts to a mailbox change: every subscription of the extension
// gets an in-dialog NOTIFY, every registered contact without one an
// unsolicited NOTIFY. It never blocks the caller.
func (s *Server) notifyMWI(ext string) {
	if ext == "" {
		return
	}
	s.goBG(func() {
		defer contain(s.log, "MWI")
		subs, subscribed := s.mwiSubs(ext)
		for _, sub := range subs {
			s.sendMWINotify(sub, int(time.Until(sub.expires)/time.Second), "", MWIChange)
		}
		unread, total, err := s.mwiCounts(ext)
		if err != nil {
			s.log.Warn("MWI counts unavailable", "error", err)
			s.m.MWINotify.WithLabelValues(MWIUnsolicited, "failed").Inc()
			return
		}
		body := mwiBody(ext, s.cfg.Domain, unread, total)
		for _, b := range s.bindingsFor(ext) {
			if subscribed[b.Device] {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
			err := s.sendUnsolicitedMWI(ctx, ext, b, body)
			cancel()
			result := "ok"
			if err != nil {
				result = "failed"
				s.log.Debug("MWI NOTIFY failed", "device", b.Device, "error", err)
			}
			s.m.MWINotify.WithLabelValues(MWIUnsolicited, result).Inc()
		}
	})
}

// mwiSubs lists the extension's live message-summary subscriptions and the
// devices that hold one.
func (s *Server) mwiSubs(ext string) ([]*subscription, map[string]bool) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	devs := map[string]bool{}
	var out []*subscription
	for id := range s.byExt[ext] {
		if sub, ok := s.subs[id]; ok && sub.event == eventMessageSummary {
			out = append(out, sub)
			devs[sub.device] = true
		}
	}
	return out, devs
}

// sendMWINotify sends one in-dialog message-summary NOTIFY. It reads the
// current counts (state, not a delta), so a NOTIFY racing a change is
// still right. A non-empty reason ends the subscription.
func (s *Server) sendMWINotify(sub *subscription, expires int, reason, kind string) {
	defer contain(s.log, "MWI notify")
	unread, total, err := s.mwiCounts(sub.extension)
	if err != nil {
		s.log.Warn("MWI counts unavailable", "error", err)
		s.m.MWINotify.WithLabelValues(kind, "failed").Inc()
		return
	}
	body := mwiBody(sub.extension, s.cfg.Domain, unread, total)
	s.subsMu.Lock()
	if reason == "" && kind == MWIChange && sub.lastMWI == body {
		s.subsMu.Unlock() // a poll or a racing change already sent these counts
		return
	}
	sub.cseq++
	seq := sub.cseq
	sub.lastMWI = body
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
	req.AppendHeader(sip.NewHeader("Event", eventMessageSummary))
	if reason != "" {
		req.AppendHeader(sip.NewHeader("Subscription-State", "terminated;reason="+reason))
	} else {
		req.AppendHeader(sip.NewHeader("Subscription-State", "active;expires="+strconv.Itoa(max(expires, 0))))
	}
	req.AppendHeader(sip.NewHeader("Content-Type", "application/simple-message-summary"))
	req.SetBody([]byte(body))
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := s.do(ctx, req)
	if err != nil {
		s.log.Debug("MWI NOTIFY failed", "device", sub.device, "error", err)
		s.m.MWINotify.WithLabelValues(kind, "failed").Inc()
		return
	}
	s.m.response(res.StatusCode)
	s.m.MWINotify.WithLabelValues(kind, "ok").Inc()
}

// sendUnsolicitedMWI sends the out-of-dialog message-summary NOTIFY (no
// Subscription-State, RFC 3842 §3 practice) to one registered contact.
func (s *Server) sendUnsolicitedMWI(ctx context.Context, ext string, b livestate.Binding, body string) error {
	var target sip.Uri
	raw := strings.TrimSuffix(strings.TrimPrefix(b.ContactURI, "<"), ">")
	if err := sip.ParseUri(raw, &target); err != nil {
		return fmt.Errorf("binding contact %q: %w", b.ContactURI, err)
	}
	req := sip.NewRequest(sip.NOTIFY, target)
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: ext, Host: s.cfg.Domain},
		Params:  sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}},
	})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: b.Device, Host: s.cfg.Domain}})
	callID := sip.CallIDHeader(newID())
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.NOTIFY})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Event", eventMessageSummary))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/simple-message-summary"))
	req.SetBody([]byte(body))
	req.SetTransport("UDP")
	if b.Source != "" {
		req.SetDestination(b.Source)
	}
	res, err := s.do(ctx, req)
	if err != nil {
		return err
	}
	s.m.response(res.StatusCode)
	return nil
}

// mwiPollLoop re-reads the mailboxes that have subscribers and NOTIFYs the
// ones whose counts changed since the last NOTIFY: that is how a message
// heard or deleted through the control-plane API reaches the phone, on any
// node holding the subscription.
func (s *Server) mwiPollLoop(ctx context.Context) {
	t := time.NewTicker(mwiPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-t.C:
		}
		s.mwiPollOnce()
	}
}

func (s *Server) mwiPollOnce() {
	defer contain(s.log, "MWI poll")
	s.subsMu.Lock()
	exts := map[string]bool{}
	for _, sub := range s.subs {
		if sub.event == eventMessageSummary {
			exts[sub.extension] = true
		}
	}
	s.subsMu.Unlock()
	for ext := range exts {
		unread, total, err := s.mwiCounts(ext)
		if err != nil {
			continue
		}
		body := mwiBody(ext, s.cfg.Domain, unread, total)
		subs, _ := s.mwiSubs(ext)
		for _, sub := range subs {
			s.subsMu.Lock()
			same := sub.lastMWI == body
			s.subsMu.Unlock()
			if !same {
				s.sendMWINotify(sub, int(time.Until(sub.expires)/time.Second), "", MWIChange)
			}
		}
	}
}
