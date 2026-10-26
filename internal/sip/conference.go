// Server conference rooms (phone-feature-parity spec S-34): the room hub, one
// room's state and controls, ownership in Valkey, live-state publishing and
// recording. The call side (joining, PINs, the participant's DTMF) is
// conference_call.go; the mixer is internal/media's Room.
//
// A room is mixed on exactly one SIP node, its owner. Ownership is a lease
// in Valkey (livestate.ConfOwnerKey) the owner renews while the room has
// participants. A join that lands on another node is forwarded to the owner
// as an ordinary SIP leg (conference_call.go), so the owner's mixer is the
// only one that ever runs for a room (docs/conference.md).
package sip

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/prometheus/client_golang/prometheus"
)

// Conferences is the shared conference state (ownership leases, published
// room state, control commands); *livestate.Store satisfies it. Nil keeps
// every room on this node alone (a single-node install and the tests).
type Conferences interface {
	AcquireLease(ctx context.Context, key, node string, ttl time.Duration) (bool, error)
	ReleaseLease(ctx context.Context, key, node string) error
	ConfOwner(ctx context.Context, number string) (string, error)
	TakeConfOwner(ctx context.Context, number, from, node string, ttl time.Duration) (bool, error)
	PutConfState(ctx context.Context, st livestate.ConfRoomState) error
	DeleteConfState(ctx context.Context, number string) error
	TakeConfCommands(ctx context.Context, node string, max int) ([]livestate.ConfCommand, error)
	ConfStates(ctx context.Context, numbers []string) (map[string]livestate.ConfRoomState, error)
}

// ConferenceAudit records room control events in the audit log;
// *store.Store satisfies it.
type ConferenceAudit interface {
	Audit(ctx context.Context, actor, action, resource, resourceID string) error
}

// ConferenceHold supplies the audio a participant hears while waiting for a
// room's leader. Nil plays the built-in wait tone; music on hold plugs in
// here: return the music as samples at media.MixRate, or nil for the
// default.
type ConferenceHold interface {
	HoldSamples(room string) []int16
}

// Conference actions, shared by the DTMF controls and the API commands.
const (
	confMute      = "mute"
	confUnmute    = "unmute"
	confKick      = "kick"
	confLock      = "lock"
	confUnlock    = "unlock"
	confMuteAll   = "mute_all"
	confUnmuteAll = "unmute_all"
	confEnd       = "end"
)

// Sources of a control action, for metrics and the audit actor.
const (
	confSrcDTMF = "dtmf"
	confSrcAPI  = "api"
)

const (
	// confRecordSegment bounds the audio held in memory: the recording is
	// stored in segments, each its own recording row, so a node that dies
	// loses at most one segment.
	confRecordSegment = 10 * time.Minute
	// confPINAttempts and the room-wide guard slow PIN guessing.
	confPINAttempts     = 3
	confPINFailWindow   = 5 * time.Minute
	confPINFailLimit    = 10
	confRenewEvery      = livestate.ConfOwnerTTL / 3
	confPublishEvery    = time.Second
	confCommandEvery    = 500 * time.Millisecond
	confLampEvery       = 2 * time.Second
	confMaxCommandBatch = 32
)

// confMetrics are the conference series, registered with the node's
// Metrics.
type confMetrics struct {
	Rooms        prometheus.Gauge       // rooms this node mixes
	Participants *prometheus.GaugeVec   // room
	Joins        *prometheus.CounterVec // result
	Control      *prometheus.CounterVec // action, source
	Owner        *prometheus.CounterVec // event
	Recordings   *prometheus.CounterVec // result
}

func newConfMetrics(reg prometheus.Registerer) *confMetrics {
	m := &confMetrics{
		Rooms: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "hello_conference_rooms", Help: "Conference rooms this node currently mixes (it owns them).",
		}),
		Participants: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "hello_conference_participants", Help: "Participants in each conference room this node mixes, waiting ones included.",
		}, []string{"room"}),
		Joins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_conference_joins_total",
			Help: "Conference join attempts by result (joined, forwarded, bad_pin, locked, full, disabled, unavailable, failed).",
		}, []string{"result"}),
		Control: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_conference_control_total", Help: "Conference control actions by action and source (dtmf, api).",
		}, []string{"action", "source"}),
		Owner: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_conference_owner_total",
			Help: "Conference room ownership events (claimed, taken_over, lost, rehomed).",
		}, []string{"event"}),
		Recordings: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_conference_recordings_total", Help: "Conference recording segments by result (stored, failed, empty).",
		}, []string{"result"}),
	}
	reg.MustRegister(m.Rooms, m.Participants, m.Joins, m.Control, m.Owner, m.Recordings)
	return m
}

// confPart is one participant of a room this node mixes.
type confPart struct {
	id     string
	c      *call
	party  *media.Party
	number string
	name   string
	joined time.Time
	admit  chan struct{} // closed when the participant leaves the wait

	// Guarded by the room's mu.
	leader        bool
	muted         bool
	mutedByLeader bool
	waiting       bool
	admitted      bool
}

// confRoom is one room this node mixes.
type confRoom struct {
	s      *Server
	number string
	mix    *media.Room

	mu     sync.Mutex
	cfg    snapshot.Conference
	parts  map[string]*confPart
	order  []string // admission order, oldest first
	seats  int      // reserved: PIN entry, waiting and admitted
	locked bool
	closed bool
	// lost is set when another node took the room's lease: the room mixes on
	// only while its participants are handed off (a drain), else it ends.
	lost       bool
	pinFails   []time.Time
	recorder   *media.ConfRecorder
	recStarted time.Time
	recSeq     int
	annCache   map[string]confAnn
	renewFails int
}

type confAnn struct {
	samples []int16
	at      time.Time
}

// confHub holds the rooms this node mixes.
type confHub struct {
	mu    sync.Mutex
	rooms map[string]*confRoom
}

func newConfHub() *confHub { return &confHub{rooms: map[string]*confRoom{}} }

func (h *confHub) get(number string) *confRoom {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rooms[number]
}

func (h *confHub) snapshotRooms() []*confRoom {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*confRoom, 0, len(h.rooms))
	for _, r := range h.rooms {
		out = append(out, r)
	}
	return out
}

// reserve takes a seat in the room, creating it on first use; false when
// the room is full. A seat is held through PIN entry and the wait, so the
// cap counts everyone who could be admitted.
func (s *Server) confReserve(cfg snapshot.Conference) (*confRoom, bool) {
	h := s.conf
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[cfg.Number]
	if r == nil {
		r = &confRoom{
			s: s, number: cfg.Number, mix: media.NewRoom(), cfg: cfg,
			parts: map[string]*confPart{}, annCache: map[string]confAnn{},
		}
		h.rooms[cfg.Number] = r
		s.m.Conf.Rooms.Inc()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seats >= max(r.cfg.MaxParticipants, 2) {
		if r.seats == 0 { // cannot happen (cap >= 2), but never leave an empty room behind
			delete(h.rooms, cfg.Number)
		}
		return r, false
	}
	r.seats++
	return r, true
}

// confRelease gives a seat back; the last one tears the room down.
func (s *Server) confRelease(r *confRoom) {
	h := s.conf
	h.mu.Lock()
	r.mu.Lock()
	r.seats--
	empty := r.seats <= 0 && !r.closed
	if empty {
		r.closed = true
		if h.rooms[r.number] == r {
			delete(h.rooms, r.number)
		}
	}
	r.mu.Unlock()
	h.mu.Unlock()
	if empty {
		r.teardown()
	}
}

// teardown stops an emptied room: the recording is stored, the shared state
// and the ownership lease are dropped.
func (r *confRoom) teardown() {
	s := r.s
	r.finishRecording()
	r.mix.Close()
	s.m.Conf.Rooms.Dec()
	s.m.Conf.Participants.DeleteLabelValues(r.number)
	if cs := s.deps.Conferences; cs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*s.cfg.StateTimeout)
		defer cancel()
		if err := cs.DeleteConfState(ctx, r.number); err != nil {
			s.log.Debug("conference state delete failed", "room", r.number, "error", err)
		}
		r.mu.Lock()
		lost := r.lost
		r.mu.Unlock()
		if !lost { // a lost lease is another node's now
			if err := cs.ReleaseLease(ctx, livestate.ConfOwnerKey(r.number), s.cfg.NodeID); err != nil {
				s.log.Debug("conference lease release failed", "room", r.number, "error", err)
			}
		}
	}
}

// config returns the room's current configuration.
func (r *confRoom) config() snapshot.Conference {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

// pinFailed records a wrong PIN and reports whether the room is guarding
// itself against guessing: too many failures in the window refuse every
// further attempt until the window passes.
func (r *confRoom) pinFailed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.pinFails = append(r.pinFails, now)
	r.trimFails(now)
}

func (r *confRoom) trimFails(now time.Time) {
	i := 0
	for i < len(r.pinFails) && now.Sub(r.pinFails[i]) > confPINFailWindow {
		i++
	}
	r.pinFails = r.pinFails[i:]
}

func (r *confRoom) pinGuarded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trimFails(time.Now())
	return len(r.pinFails) >= confPINFailLimit
}

// leaderPresent reports an admitted leader. r.mu must be held.
func (r *confRoom) leaderPresentLocked() bool {
	for _, p := range r.parts {
		if p.leader && p.admitted {
			return true
		}
	}
	return false
}

// add registers a participant (waiting or admitted) in the room's roster.
func (r *confRoom) add(p *confPart) {
	r.mu.Lock()
	r.parts[p.id] = p
	r.mu.Unlock()
}

// admit moves a participant into the mix; mute-on-entry mutes everyone but a
// leader. It returns the waiters released when a leader arrives.
func (r *confRoom) admitPart(p *confPart) (released []*confPart) {
	r.mu.Lock()
	cfg := r.cfg
	p.waiting, p.admitted = false, true
	if cfg.MuteOnEntry && !p.leader {
		p.muted = true
	}
	p.joined = time.Now()
	r.order = append(r.order, p.id)
	if p.leader {
		for _, w := range r.parts {
			if w.waiting {
				w.waiting = false
				released = append(released, w)
			}
		}
	}
	muted := p.muted
	r.mu.Unlock()
	r.mix.Admit(p.id)
	r.mix.SetHold(p.id, nil)
	r.mix.SetMuted(p.id, muted)
	r.s.m.Conf.Participants.WithLabelValues(r.number).Set(float64(r.count()))
	return released
}

func (r *confRoom) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.parts)
}

// remove takes a participant out of the roster and the mix.
func (r *confRoom) remove(p *confPart) (wasAdmitted bool, remaining int) {
	r.mu.Lock()
	if _, ok := r.parts[p.id]; ok {
		wasAdmitted = p.admitted
		delete(r.parts, p.id)
	}
	if i := slices.Index(r.order, p.id); i >= 0 {
		r.order = slices.Delete(r.order, i, i+1)
	}
	remaining = len(r.parts)
	r.mu.Unlock()
	r.mix.Remove(p.id)
	r.s.m.Conf.Participants.WithLabelValues(r.number).Set(float64(remaining))
	return wasAdmitted, remaining
}

// announce plays the join or leave sound to the room: the configured
// announcement, else the tone.
func (r *confRoom) announce(join bool) {
	cfg := r.config()
	var name string
	switch cfg.EntryExit {
	case snapshot.ConfSoundNone:
		return
	case snapshot.ConfSoundAnnouncement:
		name = cfg.ExitAnnouncement
		if join {
			name = cfg.EntryAnnouncement
		}
	}
	if name != "" {
		if s := r.announcement(name); len(s) > 0 {
			r.mix.SayAll(s)
			return
		}
	}
	tone := media.ToneLeave
	if join {
		tone = media.ToneJoin
	}
	r.mix.SayAll(media.ConfToneSamples(tone))
}

// announcement loads an uploaded announcement as mixing-rate samples,
// cached for a few minutes. A missing or unreadable one is nil (the tone
// plays instead).
func (r *confRoom) announcement(name string) []int16 {
	r.mu.Lock()
	if a, ok := r.annCache[name]; ok && time.Since(a.at) < 5*time.Minute {
		r.mu.Unlock()
		return a.samples
	}
	r.mu.Unlock()
	s := r.s
	snap := s.deps.Snapshots.Current()
	if snap == nil || s.deps.Objects == nil {
		return nil
	}
	obj, ok := snap.Announcement(name)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*s.cfg.StateTimeout)
	raw, err := s.deps.Objects.GetAnnouncement(ctx, obj)
	cancel()
	if err != nil {
		return nil
	}
	pcm, rate, err := media.DecodeWAV(raw)
	if err != nil {
		return nil
	}
	samples, err := media.ToConfRate(pcm[:len(pcm)&^1], rate)
	if err != nil {
		return nil
	}
	r.mu.Lock()
	r.annCache[name] = confAnn{samples: samples, at: time.Now()}
	r.mu.Unlock()
	return samples
}

// holdSamples is what a waiting participant hears: the ConferenceHold
// source when one is wired, else music on hold (the global default class,
// or the built-in tune when there is none), else the wait beep.
func (r *confRoom) holdSamples() []int16 {
	s := r.s
	if h := s.deps.ConferenceHold; h != nil {
		if smp := h.HoldSamples(r.number); len(smp) > 0 {
			return smp
		}
	}
	var cl snapshot.MOHClass
	if snap := s.deps.Snapshots.Current(); snap != nil {
		cl, _ = snap.DefaultMOHClass()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*s.cfg.StateTimeout)
	defer cancel()
	pl := s.mohPlaylist(ctx, cl) // an empty class plays the built-in tune
	var pcm []byte
	for range max(len(pl.objs), 1) {
		if b, err := pl.next().PCMAt(media.MixRate); err == nil {
			pcm = append(pcm, b...)
		}
	}
	if len(pcm) >= 2*media.MixFrame {
		return media.PCMToSamples(pcm[:len(pcm)&^1])
	}
	return media.WaitLoop()
}

// --- controls -----------------------------------------------------------------

// control applies one action. actor names who asked (an extension for DTMF,
// the API actor otherwise); target is the participant id for mute, unmute and
// kick. It reports whether the action changed anything.
func (r *confRoom) control(action, target, actor, source string) bool {
	s := r.s
	changed := false
	switch action {
	case confMute, confUnmute:
		changed = r.setForcedMute(target, action == confMute)
	case confKick:
		changed = r.kick(target)
	case confLock, confUnlock:
		r.mu.Lock()
		want := action == confLock
		changed = r.locked != want
		r.locked = want
		r.mu.Unlock()
		if changed {
			r.mix.SayAll(media.ConfToneSamples(media.ToneLocked))
		}
	case confMuteAll, confUnmuteAll:
		changed = r.muteAll(action == confMuteAll)
	case confEnd:
		changed = r.endAll()
	default:
		return false
	}
	if changed {
		s.m.Conf.Control.WithLabelValues(action, source).Inc()
		r.audit(actor, action, target)
		r.publish()
	}
	return changed
}

func (r *confRoom) setForcedMute(id string, mute bool) bool {
	r.mu.Lock()
	p := r.parts[id]
	if p == nil || !p.admitted {
		r.mu.Unlock()
		return false
	}
	changed := p.muted != mute || p.mutedByLeader != mute
	p.muted, p.mutedByLeader = mute, mute
	r.mu.Unlock()
	r.mix.SetMuted(id, mute)
	if changed {
		tone := media.ToneUnmute
		if mute {
			tone = media.ToneMuted
		}
		r.mix.SayTo(id, media.ConfToneSamples(tone))
	}
	return changed
}

// selfMute toggles a participant's own mute; a participant muted by a leader
// stays muted. It reports the new state and whether the request was denied.
func (r *confRoom) selfMute(p *confPart) (muted, denied bool) {
	r.mu.Lock()
	if p.mutedByLeader && !p.leader {
		r.mu.Unlock()
		return true, true
	}
	p.muted = !p.muted
	p.mutedByLeader = false
	muted = p.muted
	r.mu.Unlock()
	r.mix.SetMuted(p.id, muted)
	tone := media.ToneUnmute
	if muted {
		tone = media.ToneMuted
	}
	r.mix.SayTo(p.id, media.ConfToneSamples(tone))
	r.publish()
	return muted, false
}

func (r *confRoom) muteAll(mute bool) bool {
	r.mu.Lock()
	var ids []string
	for _, p := range r.parts {
		if p.admitted && !p.leader && (p.muted != mute || p.mutedByLeader != mute) {
			p.muted, p.mutedByLeader = mute, mute
			ids = append(ids, p.id)
		}
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.mix.SetMuted(id, mute)
	}
	if len(ids) > 0 {
		r.mix.SayAll(media.ConfToneSamples(media.ToneLocked))
	}
	return len(ids) > 0
}

// kick ends a participant's call.
func (r *confRoom) kick(id string) bool {
	r.mu.Lock()
	p := r.parts[id]
	r.mu.Unlock()
	if p == nil {
		return false
	}
	r.mix.SayTo(id, media.ConfToneSamples(media.ToneDenied))
	go func() {
		defer contain(r.s.log, "conference kick")
		time.Sleep(400 * time.Millisecond) // let the tone play
		p.c.confHangup()
	}()
	return true
}

// kickLast removes the most recently admitted participant other than the
// leader who asked and any leader.
func (r *confRoom) kickLast(by string) (string, bool) {
	r.mu.Lock()
	var target string
	for i := len(r.order) - 1; i >= 0; i-- {
		p := r.parts[r.order[i]]
		if p != nil && p.id != by && !p.leader && p.admitted {
			target = p.id
			break
		}
	}
	r.mu.Unlock()
	if target == "" {
		return "", false
	}
	return target, r.kick(target)
}

func (r *confRoom) endAll() bool {
	r.mu.Lock()
	var ps []*confPart
	for _, p := range r.parts {
		ps = append(ps, p)
	}
	r.mu.Unlock()
	for _, p := range ps {
		r.kick(p.id)
	}
	return len(ps) > 0
}

// audit records a control action in the audit log, off the media path.
func (r *confRoom) audit(actor, action, target string) {
	s := r.s
	a := s.deps.ConferenceAudit
	if a == nil {
		return
	}
	cfg := r.config()
	id := fmt.Sprint(cfg.ID)
	what := "conference." + action
	if target != "" {
		what += " " + target
	}
	s.goBG(func() {
		defer contain(s.log, "conference audit")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := a.Audit(ctx, actor, what, "conference_room", id); err != nil {
			s.log.Warn("conference audit failed", "room", r.number, "error", err)
		}
	})
}

// --- live state ---------------------------------------------------------------

// state renders the room's live state for hello-control.
func (r *confRoom) state() livestate.ConfRoomState {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := livestate.ConfRoomState{
		Room: r.number, Node: r.s.cfg.NodeID, Locked: r.locked, Recording: r.recorder != nil,
		Participants: []livestate.ConfParticipant{},
	}
	for _, p := range r.parts {
		st.Participants = append(st.Participants, livestate.ConfParticipant{
			ID: p.id, Number: p.number, Name: p.name, Leader: p.leader, Muted: p.muted,
			MutedByLeader: p.mutedByLeader, Waiting: p.waiting, JoinedAt: p.joined,
		})
	}
	slices.SortFunc(st.Participants, func(a, b livestate.ConfParticipant) int {
		if c := a.JoinedAt.Compare(b.JoinedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return st
}

// publish writes the room's state to the shared store, off the caller's
// goroutine; the periodic loop republishes anyway, so a lost write heals.
func (r *confRoom) publish() {
	cs := r.s.deps.Conferences
	if cs == nil {
		return
	}
	r.mu.Lock()
	if r.closed || r.lost {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	st := r.state()
	r.s.goBG(func() {
		defer contain(r.s.log, "conference publish")
		ctx, cancel := context.WithTimeout(context.Background(), 2*r.s.cfg.StateTimeout)
		defer cancel()
		if err := cs.PutConfState(ctx, st); err != nil {
			r.s.log.Debug("conference state write failed", "room", r.number, "error", err)
		}
	})
}

// --- recording ----------------------------------------------------------------

// startRecording begins recording the room's full mix. Segments are stored
// every confRecordSegment and when the room empties.
func (r *confRoom) startRecording() {
	r.mu.Lock()
	if r.recorder != nil || r.closed {
		r.mu.Unlock()
		return
	}
	rec := media.NewConfRecorder(0)
	r.recorder, r.recStarted = rec, time.Now()
	r.mu.Unlock()
	r.mix.SetTap(rec.Append)
	r.s.noteRecordingNotice(r)
	r.publish()
}

// rotateRecording stores the audio so far and starts the next segment.
func (r *confRoom) rotateRecording() {
	r.mu.Lock()
	old, started := r.recorder, r.recStarted
	if old == nil || time.Since(started) < confRecordSegment {
		r.mu.Unlock()
		return
	}
	next := media.NewConfRecorder(0)
	r.recorder, r.recStarted = next, time.Now()
	r.mu.Unlock()
	r.mix.SetTap(next.Append)
	r.s.goBG(func() { r.storeSegment(old, started) })
}

// finishRecording stores the last segment when the room empties.
func (r *confRoom) finishRecording() {
	r.mu.Lock()
	old, started := r.recorder, r.recStarted
	r.recorder = nil
	r.mu.Unlock()
	if old == nil {
		return
	}
	r.mix.SetTap(nil)
	r.storeSegment(old, started)
}

// storeSegment uploads one segment to MinIO and records its row, like a call
// recording: three attempts, then the metadata. The correlation id names the
// room and the segment, so the recordings list shows where it came from.
func (r *confRoom) storeSegment(rec *media.ConfRecorder, started time.Time) {
	s := r.s
	wav, durMs, err := rec.Result()
	if err != nil {
		s.m.Conf.Recordings.WithLabelValues("empty").Inc()
		return
	}
	if s.deps.Objects == nil {
		s.m.Conf.Recordings.WithLabelValues("failed").Inc()
		return
	}
	r.mu.Lock()
	r.recSeq++
	seq := r.recSeq
	r.mu.Unlock()
	corr := fmt.Sprintf("conf-%s-%d-%d", r.number, started.Unix(), seq)
	key, err := media.RecordingKey(corr, started)
	if err != nil {
		s.m.Conf.Recordings.WithLabelValues("failed").Inc()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stored := false
	for i := range 3 {
		pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
		err = s.deps.Objects.PutRecording(pctx, key, wav)
		pcancel()
		if err == nil {
			stored = true
			break
		}
		s.log.Warn("conference recording upload failed", "room", r.number, "attempt", i+1, "error", err)
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(100*(i+1)) * time.Millisecond):
		}
	}
	if !stored {
		s.m.Conf.Recordings.WithLabelValues("failed").Inc()
		return
	}
	if s.deps.Recordings != nil {
		rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := s.deps.Recordings.InsertRecording(rctx, corr, key, "default", durMs)
		rcancel()
		if err != nil {
			s.log.Warn("conference recording row failed", "room", r.number, "error", err)
			s.m.Conf.Recordings.WithLabelValues("failed").Inc()
			return
		}
	}
	s.m.Conf.Recordings.WithLabelValues("stored").Inc()
	r.audit("system", "recording_stored", corr)
}

// noteRecordingNotice plays the recording notice, when configured, as a
// call recording does.
func (s *Server) noteRecordingNotice(r *confRoom) {
	if !s.cfg.MediaRecordingNotice {
		return
	}
	if samples := r.announcement(recordingNotice); len(samples) > 0 {
		r.mix.SayAll(samples)
	}
}

// --- hub loop -----------------------------------------------------------------

// confLoop renews the leases of the rooms this node mixes, publishes their
// state, rotates recordings and applies queued control commands.
func (s *Server) confLoop(ctx context.Context) {
	defer contain(s.log, "conference loop")
	cmd := time.NewTicker(confCommandEvery)
	defer cmd.Stop()
	renew := time.NewTicker(confRenewEvery)
	defer renew.Stop()
	pub := time.NewTicker(confPublishEvery)
	defer pub.Stop()
	lamps := time.NewTicker(confLampEvery)
	defer lamps.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cmd.C:
			s.confApplyCommands()
		case <-renew.C:
			s.confRenew()
		case <-lamps.C:
			s.confLamps()
		case <-pub.C:
			for _, r := range s.conf.snapshotRooms() {
				r.publish()
				r.rotateRecording()
			}
		}
	}
}

// confHalt stops the mixers of the rooms this node mixes, once the server
// has stopped: a stopped node sends no more conference audio. A mixer left
// running would keep sending to its participants' RTP addresses, ports a
// later relay or party (in tests, another node in the same process) may
// bind. Leases and shared state stay as a crashed node leaves them: their
// lapse is what moves a room to another node.
func (s *Server) confHalt() {
	for _, r := range s.conf.snapshotRooms() {
		r.mix.Close()
	}
}

// confOccupied reports whether a room has participants anywhere in the
// cluster: here, or in the owner's published state.
func (s *Server) confOccupied(number string) bool {
	if r := s.conf.get(number); r != nil && r.count() > 0 {
		return true
	}
	cs := s.deps.Conferences
	if cs == nil {
		return false
	}
	ctx, cancel := s.stateCtx()
	defer cancel()
	st, err := cs.ConfStates(ctx, []string{number})
	return err == nil && len(st[number].Participants) > 0
}

// confLamps keeps the busy lamps of conference keys (BLF subscriptions to a
// room number) right: when a subscribed room fills or empties anywhere in
// the cluster, its subscribers on this node are told.
func (s *Server) confLamps() {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	s.subsMu.Lock()
	var rooms []string
	for ext, subs := range s.byExt {
		if _, ok := snap.Conference(ext); ok && len(subs) > 0 {
			rooms = append(rooms, ext)
		}
	}
	s.subsMu.Unlock()
	for _, number := range rooms {
		state := StateIdle
		if s.confOccupied(number) {
			state = StateOnCall
		}
		s.confLampMu.Lock()
		changed := s.confLamp[number] != state
		s.confLamp[number] = state
		s.confLampMu.Unlock()
		if changed {
			s.notifyExtension(number, state)
		}
	}
}

// confApplyCommands takes the control commands queued for this node.
func (s *Server) confApplyCommands() {
	cs := s.deps.Conferences
	if cs == nil || len(s.conf.snapshotRooms()) == 0 {
		return
	}
	ctx, cancel := s.stateCtx()
	cmds, err := cs.TakeConfCommands(ctx, s.cfg.NodeID, confMaxCommandBatch)
	cancel()
	if err != nil {
		s.log.Debug("conference commands unavailable", "error", err)
		return
	}
	for _, c := range cmds {
		r := s.conf.get(c.Room)
		if r == nil {
			continue // the room moved or emptied; the command is stale
		}
		r.control(c.Action, c.Participant, c.Actor, confSrcAPI)
	}
}

// confRenew renews the lease of every room this node mixes. A room whose
// lease another node took is "lost": it ends, unless the node is draining
// and its participants are being handed off.
func (s *Server) confRenew() {
	cs := s.deps.Conferences
	if cs == nil {
		return
	}
	for _, r := range s.conf.snapshotRooms() {
		r.mu.Lock()
		lost := r.lost
		r.mu.Unlock()
		if lost {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*s.cfg.StateTimeout)
		ok, err := cs.AcquireLease(ctx, livestate.ConfOwnerKey(r.number), s.cfg.NodeID, livestate.ConfOwnerTTL)
		cancel()
		switch {
		case err != nil:
			r.mu.Lock()
			r.renewFails++
			r.mu.Unlock()
			s.log.Warn("conference lease renewal failed", "room", r.number, "error", err)
		case !ok:
			s.confLostOwnership(r)
		default:
			r.mu.Lock()
			r.renewFails = 0
			r.mu.Unlock()
		}
	}
}

// confLostOwnership handles a room whose lease another node holds: that node
// mixes the room now, so this one must not (two mixes would split the
// room). While the node drains, its participants are handed off to
// survivors and the room mixes on until they have gone; otherwise it ends.
func (s *Server) confLostOwnership(r *confRoom) {
	r.mu.Lock()
	if r.lost {
		r.mu.Unlock()
		return
	}
	r.lost = true
	r.mu.Unlock()
	s.m.Conf.Owner.WithLabelValues("lost").Inc()
	s.log.Warn("conference room lease taken by another node", "room", r.number)
	if s.handoff.Load() {
		return
	}
	r.endAll()
}

// confNodeUsable reports whether a node can still own rooms: a READY
// member of the cluster. Without membership every node is presumed usable.
func (s *Server) confMember(ctx context.Context, node string) (cluster.Member, bool) {
	if s.deps.Membership == nil {
		return cluster.Member{}, false
	}
	ms, err := s.deps.Membership.Members(ctx)
	if err != nil {
		return cluster.Member{}, false
	}
	for _, m := range ms {
		if m.ID == node && m.Kind == cluster.KindSIP {
			return m, true
		}
	}
	return cluster.Member{}, false
}

// confRefresh updates the hosted rooms' configuration when the snapshot
// reloads: a PIN change applies to the next caller, a deleted or disabled
// room takes no more joins (those check the snapshot themselves).
func (s *Server) confRefresh(cur *snapshot.Snapshot) {
	for _, r := range s.conf.snapshotRooms() {
		if cfg, ok := cur.Conference(r.number); ok {
			r.mu.Lock()
			r.cfg = cfg
			r.mu.Unlock()
		}
	}
}
