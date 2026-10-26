// Server conference room tests (spec S-34): the mixer through real SIP and
// RTP on loopback, the PIN and capacity rules, the DTMF controls, and the
// two-node ownership, forwarding and recovery cases.
package sip

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
)

// fakeConf is an in-memory shared conference store: the ownership leases,
// published states and command queues of the cluster, usable by several
// test nodes at once.
type fakeConf struct {
	mu     sync.Mutex
	owners map[string]confLease
	states map[string]livestate.ConfRoomState
	cmds   map[string][]livestate.ConfCommand
	down   bool
}

type confLease struct {
	node    string
	expires time.Time
}

func newFakeConf() *fakeConf {
	return &fakeConf{owners: map[string]confLease{}, states: map[string]livestate.ConfRoomState{}, cmds: map[string][]livestate.ConfCommand{}}
}

func (f *fakeConf) live(key string) (confLease, bool) {
	l, ok := f.owners[key]
	if !ok || time.Now().After(l.expires) {
		delete(f.owners, key)
		return confLease{}, false
	}
	return l, true
}

func (f *fakeConf) AcquireLease(_ context.Context, key, node string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return false, errDown
	}
	if l, ok := f.live(key); ok && l.node != node {
		return false, nil
	}
	f.owners[key] = confLease{node, time.Now().Add(ttl)}
	return true, nil
}

func (f *fakeConf) ReleaseLease(_ context.Context, key, node string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.live(key); ok && l.node == node {
		delete(f.owners, key)
	}
	return nil
}

func (f *fakeConf) ConfOwner(_ context.Context, number string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return "", errDown
	}
	l, _ := f.live(livestate.ConfOwnerKey(number))
	return l.node, nil
}

func (f *fakeConf) TakeConfOwner(_ context.Context, number, from, node string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := livestate.ConfOwnerKey(number)
	if l, ok := f.live(key); ok && l.node == from {
		f.owners[key] = confLease{node, time.Now().Add(ttl)}
		return true, nil
	}
	return false, nil
}

func (f *fakeConf) PutConfState(_ context.Context, st livestate.ConfRoomState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[st.Room] = st
	return nil
}

func (f *fakeConf) DeleteConfState(_ context.Context, number string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.states, number)
	return nil
}

func (f *fakeConf) TakeConfCommands(_ context.Context, node string, max int) ([]livestate.ConfCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.cmds[node]
	n := min(len(q), max)
	f.cmds[node] = q[n:]
	return append([]livestate.ConfCommand(nil), q[:n]...), nil
}

func (f *fakeConf) ConfStates(_ context.Context, numbers []string) (map[string]livestate.ConfRoomState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]livestate.ConfRoomState{}
	for _, n := range numbers {
		if st, ok := f.states[n]; ok {
			out[n] = st
		}
	}
	return out, nil
}

func (f *fakeConf) queue(node string, c livestate.ConfCommand) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c.At = time.Now()
	f.cmds[node] = append(f.cmds[node], c)
}

func (f *fakeConf) state(room string) (livestate.ConfRoomState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.states[room]
	return st, ok
}

func (f *fakeConf) ownerNow(room string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, _ := f.live(livestate.ConfOwnerKey(room))
	return l.node
}

// fakeAudit records audit events.
type fakeAudit struct {
	mu     sync.Mutex
	events []string
}

func (f *fakeAudit) Audit(_ context.Context, actor, action, resource, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, actor+"|"+action+"|"+resource+"|"+id)
	return nil
}

func (f *fakeAudit) has(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

// withRooms installs conference rooms in the test snapshot.
func withRooms(rooms ...snapshot.Conference) pbxOpt {
	return func(_ *Config, d *Deps) { d.Snapshots.(*fakeSnaps).p.Load().WithConferences(rooms) }
}

func withConferences(cs Conferences, a ConferenceAudit) pbxOpt {
	return func(_ *Config, d *Deps) { d.Conferences, d.ConferenceAudit = cs, a }
}

// room800 is the plain test room: no PINs, ten seats, tones on entry.
func room800() snapshot.Conference {
	return snapshot.Conference{ID: 7, Name: "Weekly", Number: "800", MaxParticipants: 10, EntryExit: snapshot.ConfSoundTone, Enabled: true}
}

func confDevices() []snapshot.Device {
	return []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb1"), dev(3, "c1", "300", "pc"), dev(4, "d1", "400", "pd")}
}

// confSeat is one participant: its SIP dialog and the RTP socket that stands
// in for the phone's media.
type confSeat struct {
	t    *testing.T
	p    *phone
	dcs  *sipgo.DialogClientSession
	feed *rtpFeed
	seq  uint32
}

// seatJoin dials the room and, once answered, opens the participant's RTP.
func seatJoin(t *testing.T, p *phone, room string) (*confSeat, error) {
	t.Helper()
	r := waitCall(t, dial(t.Context(), p, room))
	if r.err != nil {
		return nil, r.err
	}
	s := &confSeat{t: t, p: p, dcs: r.dcs, feed: startRTPFeed(t, r.dcs.InviteResponse.Body())}
	s.seq = r.dcs.InviteRequest.CSeq().SeqNo + 10
	// The first packet latches the party onto this socket.
	s.feed.packet(s.feed.audio, make([]byte, 160), true)
	return s, nil
}

func (s *confSeat) leave() {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.dcs.Bye(ctx)
}

// say sends a tone for d at real time.
func (s *confSeat) say(hz, amp float64, d time.Duration) {
	f := s.feed
	end := time.Now().Add(d)
	for i := 0; time.Now().Before(end); i++ {
		pcm := make([]byte, 320)
		for j := range 160 {
			v := int16(amp * math.Sin(2*math.Pi*hz*float64(i*160+j)/8000))
			pcm[2*j], pcm[2*j+1] = byte(v), byte(v>>8)
		}
		f.packet(f.audio, media.EncodePCMU(pcm), false)
		f.ts += 160
		time.Sleep(20 * time.Millisecond)
	}
}

// hear returns the loudest sample this seat received for d.
func (s *confSeat) hear(d time.Duration) int {
	best := 0
	buf := make([]byte, 2048)
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		_ = s.feed.conn.SetReadDeadline(deadline)
		n, _, err := s.feed.conn.ReadFrom(buf)
		if err != nil {
			break
		}
		pkt, ok := media.ParseRTP(buf[:n])
		if !ok || pkt.PayloadType != s.feed.audio {
			continue
		}
		pcm := media.DecodePCMU(pkt.Payload)
		for _, v := range media.PCMToSamples(pcm) {
			best = max(best, int(math.Abs(float64(v))))
		}
	}
	return best
}

// drain discards what is buffered on the socket.
func (s *confSeat) drain() {
	buf := make([]byte, 2048)
	for {
		_ = s.feed.conn.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
		if _, _, err := s.feed.conn.ReadFrom(buf); err != nil {
			return
		}
	}
}

// keys sends digits as SIP INFO.
func (s *confSeat) keys(digits string) {
	for _, d := range digits {
		s.p.sendDTMF(s.t, s.dcs, string(d))
		time.Sleep(30 * time.Millisecond)
	}
}

// speaks has talker say something while listener hears for the same
// stretch, and returns what the listener heard.
func speaks(talker, listener *confSeat) int {
	got := make(chan int, 1)
	listener.drain()
	go func() { got <- listener.hear(500 * time.Millisecond) }()
	talker.say(500, 8000, 500*time.Millisecond)
	return <-got
}

func waitState(t *testing.T, f *fakeConf, room string, what string, cond func(livestate.ConfRoomState) bool) livestate.ConfRoomState {
	t.Helper()
	var last livestate.ConfRoomState
	eventually(t, what, func() bool {
		st, ok := f.state(room)
		last = st
		return ok && cond(st)
	})
	return last
}

// TestConferenceRoom is the S-34 acceptance test. It fails if participants
// cannot hear each other, if a talker hears itself, if the CDR does not
// carry the conference kind and the room, or if a hung-up participant stays
// in the room.
func TestConferenceRoom(t *testing.T) {
	pbx := startPBX(t, confDevices(), withRooms(room800()))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)

	sa, err := seatJoin(t, a, "800")
	if err != nil {
		t.Fatalf("a joins: %v", err)
	}
	sb, err := seatJoin(t, b, "800")
	if err != nil {
		t.Fatalf("b joins: %v", err)
	}
	sa.drain()
	sb.drain()
	time.Sleep(300 * time.Millisecond) // both admitted, entry tones played

	if got := speaks(sa, sb); got < 3000 {
		t.Fatalf("b heard peak %d while a talked: participants cannot hear each other", got)
	}
	if got := speaks(sb, sa); got < 3000 {
		t.Fatalf("a heard peak %d while b talked", got)
	}
	// Nobody hears themself.
	sa.drain()
	self := make(chan int, 1)
	go func() { self <- sa.hear(500 * time.Millisecond) }()
	sa.say(500, 8000, 500*time.Millisecond)
	if got := <-self; got > 600 {
		t.Fatalf("a heard its own voice (peak %d)", got)
	}

	sa.leave()
	cd := pbx.nextCDR(t)
	if cd.Kind != cdr.KindConference || cd.Destination != "800" || cd.RewrittenDestination != "800" {
		t.Fatalf("CDR = kind %q destination %q rewritten %q, want conference/800/800", cd.Kind, cd.Destination, cd.RewrittenDestination)
	}
	if v := pbx.metric(t, "hello_conference_participants", map[string]string{"room": "800"}); v != 1 {
		eventually(t, "participants gauge falls to 1", func() bool {
			return pbx.metric(t, "hello_conference_participants", map[string]string{"room": "800"}) == 1
		})
	}
	sb.leave()
	pbx.nextCDR(t)
	eventually(t, "room torn down", func() bool { return pbx.srv.conf.get("800") == nil })
}

// TestConferenceCapacity fails if a room admits more participants than its
// cap (the default is 10), or if a freed seat cannot be taken again.
func TestConferenceCapacity(t *testing.T) {
	pbx := startPBX(t, confDevices(), withRooms(room800()))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	var seats []*confSeat
	for i := 0; i < 10; i++ {
		s, err := seatJoin(t, a, "800")
		if err != nil {
			t.Fatalf("participant %d refused: %v", i+1, err)
		}
		seats = append(seats, s)
	}
	if _, err := seatJoin(t, a, "800"); err == nil {
		t.Fatal("an 11th participant joined a room capped at 10")
	}
	if v := pbx.metric(t, "hello_conference_joins_total", map[string]string{"result": "full"}); v != 1 {
		t.Fatalf("full joins = %v, want 1", v)
	}
	seats[0].leave()
	eventually(t, "a seat frees", func() bool {
		r := pbx.srv.conf.get("800")
		if r == nil {
			return false
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.seats == 9
	})
	s, err := seatJoin(t, a, "800")
	if err != nil {
		t.Fatalf("the freed seat could not be taken: %v", err)
	}
	seats = append(seats[1:], s)
	for _, s := range seats {
		s.leave()
	}
}

func pinRoom() snapshot.Conference {
	r := room800()
	r.UserPIN, r.LeaderPIN = "1234", "9999"
	return r
}

// TestConferencePIN fails if a wrong PIN joins the room, if three wrong
// attempts do not end the call, if the user PIN does not admit a plain
// participant, or if the leader PIN does not admit a leader.
func TestConferencePIN(t *testing.T) {
	f := newFakeConf()
	pbx := startPBX(t, confDevices(), withRooms(pinRoom()), withConferences(f, nil))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)

	bad, err := seatJoin(t, a, "800")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		bad.keys("0000#")
		time.Sleep(1800 * time.Millisecond)
	}
	waitReq(t, a.byes, "BYE after three wrong PINs")
	if st, ok := f.state("800"); ok && len(st.Participants) > 0 {
		t.Fatalf("a wrong PIN joined: %+v", st.Participants)
	}
	if v := pbx.metric(t, "hello_conference_joins_total", map[string]string{"result": "bad_pin"}); v != 1 {
		t.Fatalf("bad_pin joins = %v, want 1", v)
	}

	user, err := seatJoin(t, a, "800")
	if err != nil {
		t.Fatal(err)
	}
	user.keys("1234#")
	leader, err := seatJoin(t, b, "800")
	if err != nil {
		t.Fatal(err)
	}
	leader.keys("9999#")
	st := waitState(t, f, "800", "both admitted", func(s livestate.ConfRoomState) bool { return len(s.Participants) == 2 })
	leaders := 0
	for _, p := range st.Participants {
		if p.Leader {
			leaders++
		}
	}
	if leaders != 1 {
		t.Fatalf("%d leaders, want 1: %+v", leaders, st.Participants)
	}
	user.leave()
	leader.leave()
}

// TestConferenceControls fails if *6 does not mute and unmute a participant,
// if a plain participant can lock the room or kick, if a leader's *7 does not
// lock (and keep a plain joiner out while a leader still gets in), if *8 does
// not remove the last joiner, or if API commands do not mute, kick and
// unlock with an audit event.
func TestConferenceControls(t *testing.T) {
	f := newFakeConf()
	audit := &fakeAudit{}
	room := pinRoom()
	room.UserPIN, room.LeaderPIN = "", "9999"
	pbx := startPBX(t, confDevices(), withRooms(room), withConferences(f, audit))
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	a.register(t)
	b.register(t)
	c.register(t)

	lead, _ := seatJoin(t, a, "800")
	lead.keys("9999#")
	user, _ := seatJoin(t, b, "800")
	user.keys("#") // a room with only a leader PIN lets others in with #
	waitState(t, f, "800", "two in", func(s livestate.ConfRoomState) bool { return len(s.Participants) == 2 })
	time.Sleep(400 * time.Millisecond)

	// *6 mutes the user; the leader no longer hears them; *6 again unmutes.
	user.keys("*6")
	waitState(t, f, "800", "user muted", func(s livestate.ConfRoomState) bool {
		for _, p := range s.Participants {
			if !p.Leader && p.Muted {
				return true
			}
		}
		return false
	})
	time.Sleep(400 * time.Millisecond)
	if got := speaks(user, lead); got > 600 {
		t.Fatalf("a muted participant was heard (peak %d)", got)
	}
	user.keys("*6")
	waitState(t, f, "800", "user unmuted", func(s livestate.ConfRoomState) bool {
		for _, p := range s.Participants {
			if !p.Leader && !p.Muted {
				return true
			}
		}
		return false
	})
	time.Sleep(400 * time.Millisecond)
	if got := speaks(user, lead); got < 3000 {
		t.Fatalf("the unmuted participant is not heard (peak %d)", got)
	}

	// A plain participant cannot lock.
	user.keys("*7")
	time.Sleep(300 * time.Millisecond)
	if st, _ := f.state("800"); st.Locked {
		t.Fatal("a plain participant locked the room")
	}
	// The leader locks; a plain joiner is refused, the leader PIN gets in.
	lead.keys("*7")
	waitState(t, f, "800", "locked", func(s livestate.ConfRoomState) bool { return s.Locked })
	late, _ := seatJoin(t, c, "800")
	late.keys("#")
	waitReq(t, c.byes, "BYE for a joiner of a locked room")
	if v := pbx.metric(t, "hello_conference_joins_total", map[string]string{"result": "locked"}); v != 1 {
		t.Fatalf("locked joins = %v, want 1", v)
	}
	lead2, _ := seatJoin(t, c, "800")
	lead2.keys("9999#")
	waitState(t, f, "800", "leader joined a locked room", func(s livestate.ConfRoomState) bool { return len(s.Participants) == 3 })

	// *8: the leader removes the last plain joiner (the user; lead2 is a leader).
	lead.keys("*8")
	waitReq(t, b.byes, "BYE for the participant removed with *8")
	waitState(t, f, "800", "two left", func(s livestate.ConfRoomState) bool { return len(s.Participants) == 2 })
	if !audit.has("ext:100|conference.kick") {
		t.Fatalf("no audit event for the DTMF kick: %v", audit.events)
	}

	// API commands: mute the second leader, unlock, then end.
	st, _ := f.state("800")
	var target string
	for _, p := range st.Participants {
		if p.Number == "300" {
			target = p.ID
		}
	}
	f.queue("sip-test", livestate.ConfCommand{ID: "1", Room: "800", Action: "mute", Participant: target, Actor: "user:admin"})
	f.queue("sip-test", livestate.ConfCommand{ID: "2", Room: "800", Action: "unlock", Actor: "user:admin"})
	waitState(t, f, "800", "API mute and unlock applied", func(s livestate.ConfRoomState) bool {
		if s.Locked {
			return false
		}
		for _, p := range s.Participants {
			if p.ID == target && p.Muted && p.MutedByLeader {
				return true
			}
		}
		return false
	})
	f.queue("sip-test", livestate.ConfCommand{ID: "3", Room: "800", Action: "end", Actor: "user:admin"})
	waitReq(t, a.byes, "BYE when the room is ended")
	eventually(t, "room gone", func() bool { return pbx.srv.conf.get("800") == nil })
	if _, ok := f.state("800"); ok {
		t.Fatal("the emptied room's state was not removed")
	}
	if f.ownerNow("800") != "" {
		t.Fatal("the emptied room kept its ownership lease")
	}
}

// TestConferenceWaitForLeader fails if a participant is mixed in before a
// leader arrives, if the waiter does not hear the hold source, or if the
// leader's arrival does not admit the waiter.
func TestConferenceWaitForLeader(t *testing.T) {
	f := newFakeConf()
	room := pinRoom()
	room.UserPIN, room.LeaderPIN, room.WaitForLeader = "", "9999", true
	hold := hold500{}
	pbx := startPBX(t, confDevices(), withRooms(room), withConferences(f, nil))
	pbx.srv.deps.ConferenceHold = hold
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	w, _ := seatJoin(t, a, "800")
	w.keys("#")
	st := waitState(t, f, "800", "waiting", func(s livestate.ConfRoomState) bool {
		return len(s.Participants) == 1 && s.Participants[0].Waiting
	})
	_ = st
	if got := w.hear(600 * time.Millisecond); got < 1500 {
		t.Fatalf("the waiting participant hears peak %d, want the hold source", got)
	}
	l, _ := seatJoin(t, b, "800")
	l.keys("9999#")
	waitState(t, f, "800", "waiter admitted", func(s livestate.ConfRoomState) bool {
		if len(s.Participants) != 2 {
			return false
		}
		for _, p := range s.Participants {
			if p.Waiting {
				return false
			}
		}
		return true
	})
	time.Sleep(500 * time.Millisecond)
	if got := speaks(w, l); got < 3000 {
		t.Fatalf("the admitted waiter is not heard (peak %d)", got)
	}
}

// hold500 is a test hold source: a steady 500 Hz tone.
type hold500 struct{}

func (hold500) HoldSamples(string) []int16 {
	out := make([]int16, 8000)
	for i := range out {
		out[i] = int16(4000 * math.Sin(2*math.Pi*500*float64(i)/8000))
	}
	return out
}

// TestConferenceMuteOnEntry fails if a new participant is audible although
// the room mutes on entry, or if a leader is muted too.
func TestConferenceMuteOnEntry(t *testing.T) {
	room := room800()
	room.MuteOnEntry = true
	pbx := startPBX(t, confDevices(), withRooms(room))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	x, _ := seatJoin(t, a, "800")
	y, _ := seatJoin(t, b, "800")
	time.Sleep(600 * time.Millisecond)
	if got := speaks(x, y); got > 600 {
		t.Fatalf("a participant was heard despite mute-on-entry (peak %d)", got)
	}
	x.keys("*6")
	time.Sleep(300 * time.Millisecond)
	if got := speaks(x, y); got < 3000 {
		t.Fatalf("*6 did not unmute (peak %d)", got)
	}
}

// TestConferenceTwoNodes fails if a join landing on the node that does not
// own the room is not carried to the owner (both hear each other across
// nodes, one mixer, one CDR for the forwarded participant), if DTMF does not
// reach the owner through the forwarding node, or if the owner dying does not
// move the forwarded participant onto the surviving node's own mixer.
func TestConferenceTwoNodes(t *testing.T) {
	f := newFakeConf()
	mem := &fakeMembership{}
	devs := confDevices()
	room := room800()
	// Two nodes share this process, so they get separate RTP port ranges.
	ports := func(lo, hi int) pbxOpt { return func(c *Config, _ *Deps) { c.RTPPortMin, c.RTPPortMax = lo, hi } }
	pbxA := startPBX(t, devs, withNodeID("sip-a"), ports(20000, 20499), withRooms(room), withConferences(f, nil))
	pbxB := startPBX(t, devs, withNodeID("sip-b"), ports(20500, 20999), withRooms(room), withConferences(f, nil))
	for _, p := range []*testPBX{pbxA, pbxB} {
		p.srv.deps.Membership = mem
	}
	ready := func(id, addr string) cluster.Member {
		return cluster.Member{ID: id, Kind: cluster.KindSIP, State: cluster.Ready, SIPAddr: addr}
	}
	mem.set(ready("sip-a", pbxA.addr), ready("sip-b", pbxB.addr))

	pa := newPhone(t, pbxA, "a1", "pa")
	pa.register(t)
	pb := newPhone(t, pbxB, "b1", "pb1")
	pb.register(t)

	local, err := seatJoin(t, pa, "800") // node A takes the lease and mixes
	if err != nil {
		t.Fatal(err)
	}
	remote, err := seatJoin(t, pb, "800") // lands on B, forwarded to A
	if err != nil {
		t.Fatalf("forwarded join: %v", err)
	}
	if o := f.ownerNow("800"); o != "sip-a" {
		t.Fatalf("owner = %q, want sip-a", o)
	}
	if pbxB.srv.conf.get("800") != nil {
		t.Fatal("node B started a second mixer for the room")
	}
	waitState(t, f, "800", "both in the one room", func(s livestate.ConfRoomState) bool { return len(s.Participants) == 2 && s.Node == "sip-a" })
	remote.say(500, 0, 600*time.Millisecond) // the phone's steady stream (silence) opens the relay both ways
	if got := speaks(local, remote); got < 3000 {
		t.Fatalf("the forwarded participant did not hear the local one (peak %d)", got)
	}
	if got := speaks(remote, local); got < 3000 {
		t.Fatalf("the local participant did not hear the forwarded one (peak %d)", got)
	}
	// DTMF through the forwarding node reaches the owner's participant.
	remote.keys("*6")
	waitState(t, f, "800", "forwarded participant muted over INFO", func(s livestate.ConfRoomState) bool {
		for _, p := range s.Participants {
			if p.Number == "200" && p.Muted {
				return true
			}
		}
		return false
	})
	remote.keys("*6")
	time.Sleep(300 * time.Millisecond)

	// Node A dies: the forwarded participant continues on B's own mixer.
	local.leave()
	pbxA.stop()
	mem.set(ready("sip-b", pbxB.addr), cluster.Member{ID: "sip-a", Kind: cluster.KindSIP, State: cluster.Offline})
	f.mu.Lock()
	delete(f.owners, livestate.ConfOwnerKey("800")) // the dead owner's lease lapses
	f.mu.Unlock()
	eventually(t, "room re-homed on B", func() bool { return pbxB.srv.conf.get("800") != nil })
	if v := pbxB.metric(t, "hello_conference_owner_total", map[string]string{"event": "rehomed"}); v != 1 {
		t.Fatalf("rehomed = %v, want 1", v)
	}
	// A second caller on B joins the same room, now owned by B.
	pc := newPhone(t, pbxB, "c1", "pc")
	pc.register(t)
	third, err := seatJoin(t, pc, "800")
	if err != nil {
		t.Fatalf("join after the owner died: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if got := speaks(third, remote); got < 3000 {
		t.Fatalf("the re-homed participant does not hear the new one (peak %d)", got)
	}
	remote.leave()
	third.leave()
}

// TestConferenceTakeover fails if a participant of a room whose node dies is
// not re-INVITEd onto a surviving node's own mixer and put back in the room
// (taking the room's ownership from the dead node), if the room's replicated
// state is not marked as a conference, or if the rejoined participant cannot
// hear a participant who joins on the survivor afterwards.
func TestConferenceTakeover(t *testing.T) {
	ha, mem, f := newFakeHA(), &fakeMembership{}, newFakeConf()
	ports := func(lo, hi int) pbxOpt { return func(c *Config, _ *Deps) { c.RTPPortMin, c.RTPPortMax = lo, hi } }
	room := room800()
	owner := startPBX(t, confDevices(), withNodeID("sip-1"), withHA(ha, mem), ports(20000, 20499), withRooms(room), withConferences(f, nil))
	taker := startPBX(t, confDevices(), withNodeID("sip-2"), withHA(ha, mem), ports(20500, 20999), withRooms(room), withConferences(f, nil))
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, taker, "b1", "pb1")
	a.register(t)
	b.register(t)

	seat, err := seatJoin(t, a, "800")
	if err != nil {
		t.Fatal(err)
	}
	st := waitRecord(t, ha, "the participant is replicated as a conference", "sip-1", func(s livestate.DialogState) bool {
		return s.State == haPhaseConference && s.StateDetail == confHADetail+"800" && s.Legs[1].CallID == ""
	})
	if f.ownerNow("800") != "sip-1" {
		t.Fatalf("owner = %q before the crash", f.ownerNow("800"))
	}
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the participant")
	waitReq(t, a.acks, "ACK to the participant's 200")
	feed := startRTPFeed(t, ra.Body())
	feed.packet(feed.audio, make([]byte, 160), true)
	seat = &confSeat{t: t, p: a, dcs: seat.dcs, feed: feed}
	eventually(t, "the participant rejoined on the survivor", func() bool {
		r := taker.srv.conf.get("800")
		return r != nil && r.count() == 1
	})
	if o := f.ownerNow("800"); o != "sip-2" {
		t.Fatalf("owner after the takeover = %q, want sip-2", o)
	}
	nb, err := seatJoin(t, b, "800")
	if err != nil {
		t.Fatalf("join on the survivor: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if got := speaks(nb, seat); got < 3000 {
		t.Fatalf("the taken-over participant does not hear the new one (peak %d)", got)
	}
	if z := taker.metric(t, "hello_zombie_calls_total", nil); z != 0 {
		t.Fatalf("zombies = %v, want 0", z)
	}
	_ = st
	nb.leave()
}

// TestConferenceKeyLamp fails if a phone cannot subscribe to a room number
// (a conference key's lamp), if the lamp is not lit when the room has people
// in it (as published by whichever node mixes it), or if it does not go out
// when the room empties.
func TestConferenceKeyLamp(t *testing.T) {
	f := newFakeConf()
	pbx := startPBX(t, confDevices(), withRooms(room800()), withConferences(f, nil))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	if res := b.subscribe(t, "800", 120); res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE to a room = %d, want 200", res.StatusCode)
	}
	state := func(what string) string {
		n := waitReq(t, b.notifies, what)
		return string(n.Body())
	}
	if body := state("initial NOTIFY"); strings.Contains(body, "confirmed") {
		t.Fatalf("the lamp of an empty room is lit: %s", body)
	}
	seat, err := seatJoin(t, a, "800")
	if err != nil {
		t.Fatal(err)
	}
	lit := false
	deadline := time.After(8 * time.Second)
	for !lit {
		select {
		case n := <-b.notifies:
			lit = strings.Contains(string(n.Body()), "confirmed")
		case <-deadline:
			t.Fatal("the lamp did not light when the room filled")
		}
	}
	seat.leave()
	// The owner's state expires once the room empties (Valkey's TTL; the
	// fake has none, and a publish in flight can land after the delete).
	expire := time.NewTicker(100 * time.Millisecond)
	defer expire.Stop()
	out := false
	deadline = time.After(10 * time.Second)
	for !out {
		select {
		case <-expire.C:
			f.mu.Lock()
			delete(f.states, "800")
			f.mu.Unlock()
		case n := <-b.notifies:
			out = !strings.Contains(string(n.Body()), "confirmed")
		case <-deadline:
			t.Fatal("the lamp did not go out when the room emptied")
		}
	}
}

// TestConferenceWaitMusic fails if a participant waiting for a leader hears
// silence when no hold source is wired: it must hear music on hold (the
// built-in tune when no class is set).
func TestConferenceWaitMusic(t *testing.T) {
	f := newFakeConf()
	room := pinRoom()
	room.UserPIN, room.LeaderPIN, room.WaitForLeader = "", "9999", true
	pbx := startPBX(t, confDevices(), withRooms(room), withConferences(f, nil))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	w, _ := seatJoin(t, a, "800")
	w.keys("#")
	waitState(t, f, "800", "waiting", func(s livestate.ConfRoomState) bool {
		return len(s.Participants) == 1 && s.Participants[0].Waiting
	})
	if got := w.hear(800 * time.Millisecond); got < 500 {
		t.Fatalf("the waiting participant hears peak %d: no music on hold", got)
	}
}
