// The paging-group test (phonebooks-ptt spec P-18): hello-control queues a
// request in Valkey (POST /api/v1/paging-groups/{id}/test) and one READY SIP
// node pages the one named member, exactly as a real page would reach it
// (same auto-answer headers, same sendonly offer), plays the page tone for
// a moment and hangs up. The result lists every contact with the headers
// its INVITE carried, so an administrator can check a phone's auto-answer
// without a second person. A test page has no caller, no CDR and never
// preempts anything: a member in a call or on DND (unless the group is an
// emergency group) is reported, not rung.
package sip

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// PageTests is the queue of test-page requests; *livestate.Store satisfies
// it.
type PageTests interface {
	TakePageTest(ctx context.Context) (livestate.PageTestRequest, bool, error)
	PutPageTestResult(ctx context.Context, r livestate.PageTestResult) error
}

const (
	pageTestPoll = time.Second
	// pageTestTone is how long an answered test contact hears the tone.
	pageTestTone = 3 * time.Second
)

// pageTestLoop takes queued test requests while this node is READY.
func (s *Server) pageTestLoop(ctx context.Context) {
	defer contain(s.log, "page test loop")
	t := time.NewTicker(pageTestPoll)
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
			req, ok, err := s.deps.PageTests.TakePageTest(sctx)
			cancel()
			if err != nil {
				s.log.Warn("page test queue unavailable", "error", err)
				break
			}
			if !ok {
				break
			}
			s.bg.Go(func() {
				defer contain(s.log, "page test")
				res := s.testPage(ctx, req)
				pctx, cancel := s.stateCtx()
				defer cancel()
				if err := s.deps.PageTests.PutPageTestResult(pctx, res); err != nil {
					s.log.Warn("could not post a page test result", "id", req.ID, "error", err)
				}
			})
		}
	}
}

// testContact is one contact's INVITE state during a test page.
type testContact struct {
	b       livestate.Binding
	headers []string
	res     livestate.PageTestContact
	l       *leg
}

// testPage pages one member of a group and reports what happened.
func (s *Server) testPage(ctx context.Context, rq livestate.PageTestRequest) livestate.PageTestResult {
	res := livestate.PageTestResult{ID: rq.ID, Extension: rq.Extension, Node: s.cfg.NodeID, Contacts: []livestate.PageTestContact{}}
	fail := func(why string) livestate.PageTestResult {
		res.Result, res.Detail = "failed", why
		return res
	}
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return fail("the configuration is not loaded on this node")
	}
	g, ok := snap.PagingGroup(rq.GroupCode)
	if !ok {
		return fail("no paging group with code " + rq.GroupCode + " on this node (configuration may still be propagating)")
	}
	if !slices.Contains(g.Members, rq.Extension) {
		return fail("extension " + rq.Extension + " is not a member of " + g.Name)
	}
	e, _ := snap.Extension(rq.Extension)
	if e.DND && g.Priority != snapshot.PagingEmergency {
		res.Result = PageLegDND
		return res
	}
	if s.busyExtensions()[rq.Extension] {
		res.Result = PageLegBusy
		return res
	}
	var cs []*testContact
	for _, d := range snap.DevicesForExtension(rq.Extension) {
		sctx, cancel := s.stateCtx()
		bs, err := s.deps.State.Bindings(sctx, s.aor(d.Username))
		cancel()
		if err != nil {
			return fail("live state unavailable: " + err.Error())
		}
		for _, b := range bs {
			cs = append(cs, &testContact{b: b, res: livestate.PageTestContact{Device: d.Username, UserAgent: b.UserAgent, Headers: []string{}}})
		}
	}
	if len(cs) == 0 {
		res.Result = PageLegNoRegistration
		return res
	}
	fan, err := media.NewFanout(s.cfg.RTPPortMin, s.cfg.RTPPortMax, 0)
	if err != nil {
		return fail("no media port: " + err.Error())
	}
	defer fan.Close()
	offer := media.BuildAudioSDPDir(s.anchorHostOr(s.advHost), fan.MemberPort(), 0, 0, 0, "sendonly")

	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Go(func() {
			defer contain(s.log, "page test contact")
			s.testContact(ctx, c, rq.Extension, offer, fan)
		})
	}
	wg.Wait()

	var answered []*testContact
	for _, c := range cs {
		res.Contacts = append(res.Contacts, c.res)
		if c.res.Result == PageLegAnswered {
			answered = append(answered, c)
		}
	}
	if len(answered) > 0 {
		tctx, cancel := context.WithTimeout(ctx, pageTestTone)
		_ = fan.PlayMembers(tctx, media.PageTonePCM())
		cancel()
		var bw sync.WaitGroup
		for _, c := range answered {
			bw.Go(func() {
				defer contain(s.log, "page test BYE")
				c.l.bye()
			})
		}
		bw.Wait()
	}
	res.Result = summarizeTest(res.Contacts)
	return res
}

// summarizeTest is the overall result: answered if any contact answered,
// else busy if all were busy, else no_answer if any rang out, else failed.
func summarizeTest(cs []livestate.PageTestContact) string {
	busy, noAnswer := 0, 0
	for _, c := range cs {
		switch c.Result {
		case PageLegAnswered:
			return PageLegAnswered
		case PageLegBusy:
			busy++
		case PageLegNoAnswer:
			noAnswer++
		}
	}
	switch {
	case busy == len(cs):
		return PageLegBusy
	case noAnswer > 0:
		return PageLegNoAnswer
	}
	return "failed"
}

// testContact sends one contact's INVITE and waits for its answer; an
// answered contact is acknowledged, added to the fan-out and left for
// testPage to play the tone and hang up.
func (s *Server) testContact(ctx context.Context, c *testContact, ext string, offer []byte, fan *media.Fanout) {
	req, err := s.testInvite(c, ext, offer)
	if err != nil {
		c.res.Result = "failed"
		return
	}
	for _, h := range req.Headers() {
		switch h.Name() {
		case "Call-Info", "Alert-Info", "P-Asserted-Identity":
			c.res.Headers = append(c.res.Headers, h.Name()+": "+h.Value())
		}
	}
	lctx, cancel := context.WithTimeout(ctx, s.cfg.PageAnswerTimeout)
	defer cancel()
	dcs, err := s.uac.WriteInvite(lctx, req)
	if err != nil {
		s.log.Debug("page test INVITE failed", "contact", c.b.ContactURI, "error", err)
		c.res.Result = "failed"
		return
	}
	l := &leg{c: &call{s: s}, binding: c.b, dcs: dcs}
	err = dcs.WaitAnswer(lctx, sipgo.AnswerOptions{})
	if res := dcs.InviteResponse; err == nil || (res != nil && res.IsSuccess()) {
		ans, perr := media.ParseAudioSDP(res.Body())
		addr := udpAddr(ans.Address, ans.Port)
		if perr != nil || addr == nil || ans.PayloadType != 0 {
			l.bye()
			c.res.Result = "failed"
			return
		}
		l.ack(nil, "")
		fan.AddTarget(c.b.ContactURI, addr)
		c.l, c.res.Result = l, PageLegAnswered
		return
	}
	var de *sipgo.ErrDialogResponse
	switch {
	case errors.As(err, &de) && isBusy(de.Res.StatusCode):
		c.res.Result = PageLegBusy
	case errors.As(err, &de):
		c.res.Result = "failed"
	default:
		c.res.Result = PageLegNoAnswer
	}
}

// testInvite builds a test page's INVITE: a page leg's INVITE (sendonly
// offer, the vendor's auto-answer headers, a caller identity) from a
// system caller.
func (s *Server) testInvite(c *testContact, ext string, offer []byte) (*sip.Request, error) {
	var ruri sip.Uri
	if err := sip.ParseUri(c.b.ContactURI, &ruri); err != nil {
		return nil, err
	}
	const name, num = "Hello paging test", "paging-test"
	req := sip.NewRequest(sip.INVITE, ruri)
	callID := sip.CallIDHeader(newID())
	maxFwd := sip.MaxForwardsHeader(70)
	req.AppendHeader(&sip.FromHeader{
		DisplayName: name,
		Address:     sip.Uri{Scheme: "sip", User: num, Host: s.cfg.Domain},
		Params:      sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(16)}},
	})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: ext, Host: s.cfg.Domain}})
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE})
	req.AppendHeader(&maxFwd)
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Allow", allow))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(offer)
	for _, h := range pagingHeaders(s.cfg.Domain, c.b.UserAgent, name, num) {
		req.AppendHeader(h)
	}
	req.SetTransport("UDP")
	if err := s.routeToBinding(req, c.b); err != nil {
		return nil, fmt.Errorf("route to contact: %w", err)
	}
	return req, nil
}
