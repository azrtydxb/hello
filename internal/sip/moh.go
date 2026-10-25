// Music on hold (phone-feature-parity S-33): the anchor plays an admin
// chosen playlist, or a built-in tone, to a party the other side holds
// (sendonly/inactive re-INVITE) or that has been parked, and to a caller
// who would otherwise hear ringback when the ring group or inbound route
// asks for waiting music. The music stops on resume, answer or hangup.
package sip

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// haMOHDetailPrefix starts the replicated detail of a held call that plays
// music: "moh:<class id>:<leg that hears it>". A taker resumes the music
// from it (incall-ha: the call keeps its hold across a takeover).
const haMOHDetailPrefix = "moh:"

// mohCacheTTL is how long a decoded announcement stays cached, so a hold
// does not fetch from MinIO every time while a replaced WAV is still
// picked up within the interval.
const mohCacheTTL = 30 * time.Second

// mohPlayer is one running music stream.
type mohPlayer struct {
	cancel context.CancelFunc
	done   chan struct{}
	// heldLeg hears the music; holderLeg is muted while it plays (empty
	// for a ringback stream, which has no holder).
	heldLeg, holderLeg string
	classID            int64
}

type mohAudioEntry struct {
	pcm []byte
	at  time.Time
}

// noteMOHRoute records the matched inbound route's music on hold settings.
func (c *call) noteMOHRoute(dec routing.Decision) {
	c.mu.Lock()
	c.mohRouteClass, c.mohRouteRingback = dec.MOHClassID, dec.MOHRingback
	c.mu.Unlock()
}

// noteMOHGroup records the dialled ring group's music on hold settings.
func (c *call) noteMOHGroup(g snapshot.RingGroup) {
	c.mu.Lock()
	c.mohGroupClass, c.mohGroupRingback = g.MOHClassID, g.MOHRingback
	c.mu.Unlock()
}

// extensionOfLeg is the extension number behind the caller (true) or callee
// (false) side of the call, "" for an external party.
func (c *call) extensionOfLeg(snap *snapshot.Snapshot, caller bool) string {
	if snap == nil {
		return ""
	}
	if caller {
		if _, ok := snap.Extension(c.callerNum); ok {
			return c.callerNum
		}
		return ""
	}
	c.mu.Lock()
	w, dialled := c.winner, c.dialled
	c.mu.Unlock()
	if w != nil && w.binding.AOR != "" {
		var u sip.Uri
		if err := sip.ParseUri(trimAngle(w.binding.AOR), &u); err == nil {
			if d, ok := snap.DeviceByUsername(u.User); ok {
				return d.Extension
			}
		}
	}
	if _, ok := snap.Extension(dialled); ok {
		return dialled
	}
	return ""
}

// holdClassID picks the class a held party hears: the holder's extension,
// then the ring group the call dialled, then the inbound route that
// delivered it (the last two only when the callee holds, since the
// destination is what they configure), else the global default. 0 means
// the built-in tone.
func (c *call) holdClassID(snap *snapshot.Snapshot, holderIsCaller bool) int64 {
	var ids []int64
	if e, ok := snap.Extension(c.extensionOfLeg(snap, holderIsCaller)); ok {
		ids = append(ids, e.MOHClassID)
	}
	if !holderIsCaller {
		c.mu.Lock()
		ids = append(ids, c.mohGroupClass, c.mohRouteClass)
		c.mu.Unlock()
	}
	if cl, ok := snap.ResolveMOH(ids...); ok {
		return cl.ID
	}
	return 0
}

// holdMOH starts or stops the music for a hold transition the in-dialog
// handlers just decided: fromCaller says which side sent the re-INVITE.
// The holder's stream is muted, the other leg hears the music. No-op
// without an anchor (a direct call cannot play anything).
func (c *call) holdMOH(fromCaller, held bool) {
	if !held {
		c.stopHoldMOH()
		return
	}
	snap := c.s.deps.Snapshots.Current()
	if snap == nil || c.anchorRelay() == nil {
		return
	}
	holder, heard := legCaller, legCallee
	if !fromCaller {
		holder, heard = legCallee, legCaller
	}
	c.mu.Lock()
	cur := c.moh
	c.mu.Unlock()
	if cur != nil && cur.heldLeg == heard {
		return // already playing to that party (a refreshing re-INVITE)
	}
	classID := c.holdClassID(snap, fromCaller)
	c.startHoldMOH(heard, holder, classID)
}

// startHoldMOH plays class classID (0 = built-in) to heard and mutes holder.
// A parked call uses it too, with no holder.
func (c *call) startHoldMOH(heard, holder string, classID int64) {
	r := c.anchorRelay()
	if r == nil {
		return
	}
	c.mu.Lock()
	old := c.moh
	c.moh = nil
	c.mu.Unlock()
	c.stopMOHPlayer(old)
	p := c.runMOH(r, heard, holder, classID)
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		c.stopMOHPlayer(p)
		return
	}
	c.moh = p
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Music on hold plays to the %s", heard))
	c.noteHAState("", haMOHDetailPrefix+strconv.FormatInt(classID, 10)+":"+heard)
}

// stopHoldMOH ends the hold music (resume, hangup).
func (c *call) stopHoldMOH() {
	c.mu.Lock()
	p := c.moh
	c.moh = nil
	c.mu.Unlock()
	if p == nil {
		return
	}
	c.stopMOHPlayer(p)
	c.addTrace("Music on hold stops")
	c.noteHAState("", "")
}

// withMOHDetail is the replicated detail of a call: the music a held party
// hears is carried while no other phase owns the detail.
func (c *call) withMOHDetail(phase, detail string) string {
	if detail != "" || (phase != "" && phase != haPhaseRecording) {
		return detail
	}
	c.mu.Lock()
	p := c.moh
	c.mu.Unlock()
	if p == nil {
		return ""
	}
	return haMOHDetailPrefix + strconv.FormatInt(p.classID, 10) + ":" + p.heldLeg
}

// resumeMOH restarts the music of a held call a taker adopted: detail is
// the replicated "moh:<class>:<leg>". A detail it cannot read plays the
// built-in tone to the leg the hold direction says is held, if any.
func (c *call) resumeMOH(detail string) {
	rest, ok := strings.CutPrefix(detail, haMOHDetailPrefix)
	if !ok {
		return
	}
	idStr, leg, ok := strings.Cut(rest, ":")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if !ok || err != nil || (leg != legCaller && leg != legCallee) {
		return
	}
	holder := legCallee
	if leg == legCallee {
		holder = legCaller
	}
	c.addTrace("Music on hold resumes after the takeover")
	c.startHoldMOH(leg, holder, id)
}

// ringbackMOH decides whether the caller hears music instead of ringback
// and which class: the group's setting wins over the route's flag, and the
// class follows the same order (group, route, default).
func (c *call) ringbackMOH() (classID int64, ok bool) {
	snap := c.s.deps.Snapshots.Current()
	if snap == nil {
		return 0, false
	}
	c.mu.Lock()
	gOn, rOn := c.mohGroupRingback, c.mohRouteRingback
	ids := []int64{c.mohGroupClass, c.mohRouteClass}
	if !gOn {
		ids[0], ids[1] = ids[1], ids[0]
	}
	c.mu.Unlock()
	if !gOn && !rOn {
		return 0, false
	}
	if cl, found := snap.ResolveMOH(ids...); found {
		return cl.ID, true
	}
	return 0, true
}

// startRingMOH answers the caller's INVITE with 183 Session Progress and the
// anchor's SDP, and plays the class to the caller instead of ringback. It
// reports false (the caller then sends the plain 180) when the call has no
// anchor or the early media could not be offered.
func (c *call) startRingMOH(classID int64) bool {
	r := c.anchorRelay()
	if r == nil || len(c.inv.Body()) == 0 {
		return false
	}
	body := c.anchoredAnswerA(parsedOffer(c))
	if len(body) == 0 {
		return false
	}
	c.s.m.response(sip.StatusSessionInProgress)
	if err := c.dss.Respond(sip.StatusSessionInProgress, "Session Progress", body,
		sip.NewHeader("Content-Type", "application/sdp")); err != nil {
		c.s.log.Debug("waiting music early media failed", "correlation_id", c.id, "error", err)
		return false
	}
	p := c.runMOH(r, legCaller, "", classID)
	c.mu.Lock()
	if c.ended || c.connected {
		c.mu.Unlock()
		c.stopMOHPlayer(p)
		return true
	}
	c.ringMOH = p
	c.mu.Unlock()
	c.addTrace("Waiting music plays to the caller instead of ringback")
	return true
}

// stopRingMOH ends the waiting music (the destination answered, the call
// moves on to voicemail or an announcement, or it ended).
func (c *call) stopRingMOH() {
	c.mu.Lock()
	p := c.ringMOH
	c.ringMOH = nil
	c.mu.Unlock()
	c.stopMOHPlayer(p)
}

// runMOH starts the stream goroutine on relay r.
func (c *call) runMOH(r *media.Relay, heard, holder string, classID int64) *mohPlayer {
	ctx, cancel := context.WithCancel(context.Background())
	p := &mohPlayer{cancel: cancel, done: make(chan struct{}), heldLeg: heard, holderLeg: holder, classID: classID}
	if holder != "" {
		r.Mute(holder, true)
	}
	snap := c.s.deps.Snapshots.Current()
	go func() {
		defer close(p.done)
		defer contain(c.s.log, "music on hold")
		var cl snapshot.MOHClass
		if snap != nil {
			cl, _ = snap.MOHClass(classID)
		}
		pl := c.s.mohPlaylist(ctx, cl)
		if err := r.PlayStream(ctx, heard, pl.next); err != nil && ctx.Err() == nil {
			c.s.log.Debug("music on hold ended", "leg", heard, "error", err)
		}
	}()
	return p
}

// stopMOHPlayer cancels p, waits for its stream to end and unmutes the
// holder.
func (c *call) stopMOHPlayer(p *mohPlayer) {
	if p == nil {
		return
	}
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
	if p.holderLeg != "" {
		if r := c.anchorRelay(); r != nil {
			r.Mute(p.holderLeg, false)
		}
	}
}

// mohPlaylist is a class's tracks, decoded, with its play order.
type mohPlaylist struct {
	tracks  [][]byte
	shuffle bool
	order   []int
	pos     int
	last    int
}

// next returns the next track's PCM: in order for a loop, in a fresh random
// order per pass (never repeating the previous track back to back) for a
// shuffle.
func (p *mohPlaylist) next() []byte {
	n := len(p.tracks)
	if n == 0 {
		return nil
	}
	if !p.shuffle || n == 1 {
		t := p.tracks[p.pos%n]
		p.pos++
		return t
	}
	if p.pos >= len(p.order) {
		p.order = rand.Perm(n)
		if p.order[0] == p.last {
			p.order[0], p.order[n-1] = p.order[n-1], p.order[0]
		}
		p.pos = 0
	}
	p.last = p.order[p.pos]
	p.pos++
	return p.tracks[p.last]
}

// mohPlaylist loads a class's announcements. A class with none, or none that
// load, plays the built-in track, so a hold never hears silence.
func (s *Server) mohPlaylist(ctx context.Context, cl snapshot.MOHClass) *mohPlaylist {
	pl := &mohPlaylist{shuffle: cl.Shuffle, last: -1}
	for _, obj := range cl.Objects {
		if ctx.Err() != nil {
			break
		}
		if pcm, ok := s.mohAudio(ctx, obj); ok {
			pl.tracks = append(pl.tracks, pcm)
		}
	}
	if len(pl.tracks) == 0 {
		pl.tracks = [][]byte{media.DefaultMOHPCM()}
	}
	return pl
}

// mohAudio fetches and decodes one announcement object, cached briefly.
func (s *Server) mohAudio(ctx context.Context, obj string) ([]byte, bool) {
	s.mohMu.Lock()
	e, ok := s.mohCache[obj]
	s.mohMu.Unlock()
	if ok && time.Since(e.at) < mohCacheTTL {
		return e.pcm, true
	}
	objs := s.deps.Objects
	if objs == nil {
		return nil, false
	}
	fctx, cancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
	raw, err := objs.GetAnnouncement(fctx, obj)
	cancel()
	if err != nil {
		s.log.Debug("music on hold announcement unavailable", "object", obj, "error", err)
		return nil, false
	}
	pcm, rate, err := media.DecodeWAV(raw)
	if err != nil || len(pcm) == 0 {
		s.log.Debug("music on hold announcement is not a WAV", "object", obj)
		return nil, false
	}
	pcm = downsampleTo8k(pcm, rate)
	s.mohMu.Lock()
	if s.mohCache == nil {
		s.mohCache = map[string]mohAudioEntry{}
	}
	s.mohCache[obj] = mohAudioEntry{pcm: pcm, at: time.Now()}
	s.mohMu.Unlock()
	return pcm, true
}

// setHAPeerDir overrides the replicated direction of the dialog that did not
// send the hold re-INVITE (setHADirs gives it the offered direction).
func (c *call) setHAPeerDir(fromCaller bool, dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fromCaller {
		c.haDirs[1] = dir
		return
	}
	c.haDirs[0] = dir
}

// endMOH stops every music stream as the call ends; the relay closing ends
// whatever is still running.
func (c *call) endMOH() {
	c.mu.Lock()
	hold, ring := c.moh, c.ringMOH
	c.moh, c.ringMOH = nil, nil
	c.mu.Unlock()
	for _, p := range []*mohPlayer{hold, ring} {
		if p != nil {
			p.cancel()
		}
	}
}

// downsampleTo8k averages 16-bit mono PCM down to 8 kHz when its rate is a
// multiple of 8000; other rates pass through unchanged (the relay's stream
// is 8 kHz G.711).
func downsampleTo8k(pcm []byte, rate int) []byte {
	if rate <= 8000 || rate%8000 != 0 {
		return pcm
	}
	k := rate / 8000
	n := len(pcm) / 2 / k
	out := make([]byte, 2*n)
	for i := range n {
		sum := 0
		for j := range k {
			o := 2 * (i*k + j)
			sum += int(int16(uint16(pcm[o]) | uint16(pcm[o+1])<<8)) //nolint:gosec // G115: PCM sample bytes.
		}
		v := uint16(int16(sum / k)) //nolint:gosec // G115: PCM sample bytes.
		out[2*i], out[2*i+1] = byte(v), byte(v>>8)
	}
	return out
}
