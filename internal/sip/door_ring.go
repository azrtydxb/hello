package sip

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// The doorbell webhook (spec phone-feature-parity S-48). hello-control has
// already checked the signature and the rate; a trigger reaches a SIP node
// through the queue. The ring_door action rings the members of the Door group
// with the doorbell tag from a virtual caller named after the webhook; the
// first to answer hears the chime (the webhook's announcement or the built-in
// one) and the call ends after it, or after doorChimeMax. The chime_page
// action plays the chime one way to every member of a paging group that
// answers the auto-answer page. Both write a CDR with kind door.

// DoorTriggers is the queue of doorbell webhook triggers; *livestate.Store
// satisfies it.
type DoorTriggers interface {
	TakeDoorTrigger(ctx context.Context) (livestate.DoorTrigger, bool, error)
}

const (
	doorTriggerPoll = 500 * time.Millisecond
	// doorChimeMax ends a chime call that is still playing.
	doorChimeMax = 30 * time.Second
	// doorHookCaller is the user part of the virtual caller.
	doorHookCaller = "door-webhook"
)

// doorTriggerLoop takes queued triggers while this node is READY.
func (s *Server) doorTriggerLoop(ctx context.Context) {
	defer contain(s.log, "door trigger loop")
	t := time.NewTicker(doorTriggerPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if st, _ := s.state(); st != cluster.Ready {
			continue
		}
		for {
			sctx, cancel := s.stateCtx()
			tr, ok, err := s.deps.DoorTriggers.TakeDoorTrigger(sctx)
			cancel()
			if err != nil {
				s.log.Warn("door trigger queue unavailable", "error", err)
				break
			}
			if !ok {
				break
			}
			s.bg.Go(func() {
				defer contain(s.log, "door trigger")
				s.runDoorTrigger(ctx, tr)
			})
		}
	}
}

// chimeContact is one registered contact being rung or paged.
type chimeContact struct {
	ext string
	b   livestate.Binding
	l   *leg
	res string
}

// runDoorTrigger rings or pages for one trigger and writes its CDR.
func (s *Server) runDoorTrigger(ctx context.Context, t livestate.DoorTrigger) {
	start := time.Now()
	rec := cdr.Record{
		CorrelationID: newID(), SIPCallID: newID(), Source: t.Name, StartTime: start, SIPNode: s.cfg.NodeID,
		MediaMode: "anchored", Direction: cdr.DirectionInternal, Kind: cdr.KindDoor, TerminationSide: cdr.SideSystem,
	}
	var tr routing.Trace
	finish := func(status int, why string) {
		rec.EndTime = time.Now()
		rec.DurationMs = rec.EndTime.Sub(start).Milliseconds()
		rec.FinalStatus, rec.FailureReason, rec.Trace = status, why, tr
		if !rec.AnswerTime.IsZero() {
			rec.BillableMs = rec.EndTime.Sub(rec.AnswerTime).Milliseconds()
		}
		s.deps.CDRs.Enqueue(rec)
	}
	label := ""
	if t.Label != "" {
		label = " (" + t.Label + ")"
	}
	tr.Add("Doorbell webhook " + t.Name + label)

	snap := s.deps.Snapshots.Current()
	if snap == nil {
		tr.Add("The configuration is not loaded on this node")
		finish(sip.StatusServiceUnavailable, "configuration not loaded")
		return
	}
	var (
		exts      []string
		ringWait  = 30 * time.Second
		ignoreDND bool
		firstWins bool
	)
	switch t.Action {
	case "ring_door":
		names := []string{t.RingGroup}
		if t.RingGroup == "" {
			names = snap.DoorGroups()
		}
		for _, name := range names {
			g, members, ok := snap.RingGroup(name)
			if !ok || !g.IsDoor {
				tr.Add("Door group " + name + " is not configured on this node")
				continue
			}
			tr.Add("Ringing Door group " + name)
			for _, m := range members {
				if !slices.Contains(exts, m.Extension) {
					exts = append(exts, m.Extension)
				}
			}
			if g.RingTimeout > 0 {
				ringWait = max(ringWait, time.Duration(g.RingTimeout)*time.Second)
			}
			ignoreDND = ignoreDND || g.IgnoreDND
		}
		rec.Destination = strings.Join(names, ",")
		if t.RingGroup == "" && len(names) == 0 {
			rec.Destination = "door"
		}
		rec.RingTag, rec.RingRule = "doorbell", "kind:door"
		firstWins = true
	case "chime_page":
		g, ok := snap.PagingGroup(t.PagingCode)
		rec.Destination = t.PagingCode
		if !ok || !g.Enabled {
			tr.Add("Paging group " + t.PagingCode + " is not available on this node")
			finish(sip.StatusNotFound, "paging group not available")
			return
		}
		tr.Add("Chiming paging group " + g.Name)
		exts = g.Members
		ringWait = s.cfg.PageAnswerTimeout
		ignoreDND = g.Priority == snapshot.PagingEmergency
	default:
		tr.Add("Unknown action " + t.Action)
		finish(sip.StatusInternalServerError, "unknown action")
		return
	}

	busy := s.busyExtensions()
	var cs []*chimeContact
	for _, ext := range exts {
		if e, _ := snap.Extension(ext); (e.DND && !ignoreDND) || e.Door {
			tr.Add("Skipped " + ext + ": do not disturb")
			continue
		}
		if busy[ext] {
			tr.Add("Skipped " + ext + ": busy")
			continue
		}
		for _, d := range snap.DevicesForExtension(ext) {
			sctx, cancel := s.stateCtx()
			bs, err := s.deps.State.Bindings(sctx, s.aor(d.Username))
			cancel()
			if err != nil {
				tr.Add("Live state unavailable")
				finish(sip.StatusServiceUnavailable, "live state unavailable")
				return
			}
			for _, b := range bs {
				cs = append(cs, &chimeContact{ext: ext, b: b})
			}
		}
	}
	if len(cs) == 0 {
		tr.Add("No registered phone to ring")
		finish(sip.StatusTemporarilyUnavailable, "no registered phone")
		return
	}

	fan, err := media.NewFanout(s.cfg.RTPPortMin, s.cfg.RTPPortMax, 0)
	if err != nil {
		tr.Add("No media port")
		finish(sip.StatusServiceUnavailable, "no media port")
		return
	}
	defer fan.Close()
	offer := media.BuildAudioSDPDir(s.anchorHostOr(s.advHost), fan.MemberPort(), 0, 0, 0, "sendonly")
	tbl := snap.RingTags()
	rec.RingTime = time.Now()

	ringCtx, cancelRing := context.WithTimeout(ctx, ringWait)
	defer cancelRing()
	var (
		mu      sync.Mutex
		winners []*chimeContact
		won     atomic.Bool
		wg      sync.WaitGroup
	)
	for _, c := range cs {
		wg.Go(func() {
			defer contain(s.log, "door contact")
			req, err := s.chimeInvite(c, t, offer, tbl.Header("doorbell", c.b.UserAgent))
			if err != nil {
				c.res = "failed"
				return
			}
			dcs, err := s.uac.WriteInvite(ringCtx, req)
			if err != nil {
				c.res = "failed"
				return
			}
			l := &leg{c: &call{s: s}, binding: c.b, dcs: dcs}
			err = dcs.WaitAnswer(ringCtx, sipgo.AnswerOptions{})
			if res := dcs.InviteResponse; err == nil || (res != nil && res.IsSuccess()) {
				ans, perr := media.ParseAudioSDP(res.Body())
				addr := udpAddr(ans.Address, ans.Port)
				if perr != nil || addr == nil || ans.PayloadType != 0 {
					l.bye()
					c.res = "failed"
					return
				}
				// Ringing a group: the first answer wins and the rest give up.
				if firstWins && !won.CompareAndSwap(false, true) {
					l.bye()
					c.res = "answered elsewhere"
					return
				}
				l.ack(nil, "")
				fan.AddTarget(c.b.ContactURI, addr)
				c.l, c.res = l, PageLegAnswered
				mu.Lock()
				winners = append(winners, c)
				mu.Unlock()
				if firstWins {
					cancelRing() // CANCEL the others
				}
				return
			}
			var de *sipgo.ErrDialogResponse
			switch {
			case errors.As(err, &de) && isBusy(de.Res.StatusCode):
				c.res = PageLegBusy
			case errors.As(err, &de):
				c.res = "failed"
			case firstWins && won.Load():
				c.res = "answered elsewhere"
			default:
				c.res = PageLegNoAnswer
			}
		})
	}
	wg.Wait()
	for _, c := range cs {
		tr.Add(fmt.Sprintf("%s (%s): %s", c.ext, c.b.Device, c.res))
	}
	if len(winners) == 0 {
		finish(sip.StatusTemporarilyUnavailable, "not answered")
		return
	}
	rec.AnswerTime = time.Now()
	names := make([]string, len(winners))
	for i, w := range winners {
		names[i] = w.ext
	}
	tr.Add("Answered by " + strings.Join(names, ", "))

	pcm := s.chimeAudio(&tr, snap, t.Announcement)
	pctx, pcancel := context.WithTimeout(ctx, doorChimeMax)
	err = fan.PlayMembers(pctx, pcm)
	pcancel()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		tr.Add("Chime stopped early")
	} else {
		tr.Add("Chime played")
	}
	var bw sync.WaitGroup
	for _, w := range winners {
		bw.Go(func() {
			defer contain(s.log, "door chime BYE")
			w.l.bye()
		})
	}
	bw.Wait()
	finish(sip.StatusOK, "")
}

// chimeAudio is the PCM (8 kHz mono) of the chime: the named announcement, or
// the built-in chime when none is named or it cannot be read.
func (s *Server) chimeAudio(tr *routing.Trace, snap *snapshot.Snapshot, announcement string) []byte {
	if announcement == "" {
		return media.ChimePCM()
	}
	obj, ok := snap.Announcement(announcement)
	if !ok || s.deps.Objects == nil {
		tr.Add("Announcement " + announcement + " is missing: built-in chime")
		return media.ChimePCM()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*s.cfg.StateTimeout)
	defer cancel()
	raw, err := s.deps.Objects.GetAnnouncement(ctx, obj)
	if err != nil {
		tr.Add("Announcement " + announcement + " is unavailable: built-in chime")
		return media.ChimePCM()
	}
	pcm, rate, err := media.DecodeWAV(raw)
	if err != nil {
		tr.Add("Announcement " + announcement + " is not a WAV: built-in chime")
		return media.ChimePCM()
	}
	if rate != 8000 {
		if pcm, err = media.Resample(pcm, rate, 8000); err != nil {
			tr.Add("Announcement " + announcement + " could not be resampled: built-in chime")
			return media.ChimePCM()
		}
	}
	return pcm
}

// chimeInvite builds the INVITE of one contact: a sendonly offer from the
// virtual caller named after the webhook. A ring carries the doorbell
// Alert-Info (alert), a page the vendor's auto-answer headers.
func (s *Server) chimeInvite(c *chimeContact, t livestate.DoorTrigger, offer []byte, alert string) (*sip.Request, error) {
	var ruri sip.Uri
	if err := sip.ParseUri(c.b.ContactURI, &ruri); err != nil {
		return nil, err
	}
	name := strings.NewReplacer(`"`, ``, `\`, ``, "\r", ``, "\n", ``).Replace(t.Name)
	req := sip.NewRequest(sip.INVITE, ruri)
	callID := sip.CallIDHeader(newID())
	maxFwd := sip.MaxForwardsHeader(70)
	req.AppendHeader(&sip.FromHeader{
		DisplayName: name,
		Address:     sip.Uri{Scheme: "sip", User: doorHookCaller, Host: s.cfg.Domain},
		Params:      sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(16)}},
	})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: c.ext, Host: s.cfg.Domain}})
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE})
	req.AppendHeader(&maxFwd)
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Allow", allow))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(offer)
	if t.Action == "chime_page" {
		for _, h := range pagingHeaders(s.cfg.Domain, c.b.UserAgent, name, doorHookCaller) {
			req.AppendHeader(h)
		}
	} else {
		req.AppendHeader(sip.NewHeader("Alert-Info", alert))
	}
	req.SetTransport("UDP")
	if err := s.routeToBinding(req, c.b); err != nil {
		return nil, fmt.Errorf("route to contact: %w", err)
	}
	return req, nil
}
