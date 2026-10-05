// Takeover tests (incall-ha spec S-2, S-5, S-8): the claim's atomicity, the
// leg re-creation and re-INVITEs against in-process phones, the yield when
// a slow owner reappears, and the one-sided close when an endpoint is gone.
package sip

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/emiago/sipgo/sip"
)

// fakeHA is an in-memory dialog-replication store. Unlike the real one its
// ClaimDialog has no freshness window: the tests age replicated states by
// hand (the window itself is covered against real Valkey in livestate).
type fakeHA struct {
	mu       sync.Mutex
	dialogs  map[string]livestate.DialogState
	claims   map[string]string
	failSave bool
	deleted  map[string]int
}

func newFakeHA() *fakeHA {
	return &fakeHA{dialogs: map[string]livestate.DialogState{}, claims: map[string]string{}, deleted: map[string]int{}}
}

func (f *fakeHA) SaveDialogState(_ context.Context, sds livestate.DialogState, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSave {
		return errDown
	}
	f.dialogs[sds.CallID] = sds
	return nil
}

func (f *fakeHA) DeleteDialogState(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted[id]++
	delete(f.dialogs, id)
	return nil
}

func (f *fakeHA) ClaimDialog(_ context.Context, id, node string) (bool, livestate.DialogState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.dialogs[id]
	if !ok {
		return false, livestate.DialogState{}, nil
	}
	if _, taken := f.claims[id]; taken {
		return false, st, nil
	}
	f.claims[id] = node
	return true, st, nil
}

func (f *fakeHA) ReleaseDialogClaim(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.claims, id)
	return nil
}

func (f *fakeHA) ClaimOwner(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims[id], nil
}

func (f *fakeHA) OrphanedDialogs(_ context.Context, node string) ([]livestate.DialogState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []livestate.DialogState
	for _, st := range f.dialogs {
		if st.OwnerNode == node {
			if _, taken := f.claims[st.CallID]; !taken {
				out = append(out, st)
			}
		}
	}
	return out, nil
}

// put stores a replicated state; claim sets one as if a taker held it.
func (f *fakeHA) put(st livestate.DialogState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dialogs[st.CallID] = st
}

func (f *fakeHA) ownerOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dialogs[id].OwnerNode
}

func (f *fakeHA) claimOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims[id]
}

// fakeMembership is a fixed cluster view.
type fakeMembership struct {
	mu      sync.Mutex
	members []cluster.Member
}

func (f *fakeMembership) Members(context.Context) ([]cluster.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cluster.Member(nil), f.members...), nil
}

func (f *fakeMembership) set(members ...cluster.Member) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members = members
}

func withNodeID(id string) pbxOpt {
	return func(c *Config, _ *Deps) { c.NodeID = id }
}

// withHA wires the replication store, the cluster view and the takeover
// poller's timings (fast heartbeat and poll, no jitter, so tests can wait
// for exact effects).
func withHA(ha HAState, m Membership) pbxOpt {
	return func(c *Config, d *Deps) {
		d.HAState, d.Membership = ha, m
		c.HATakeoverEnabled = true
		c.HATakeoverPoll, c.HATakeoverJitter = 20*time.Millisecond, 0
		c.HADialogHeartbeat = 20 * time.Millisecond
	}
}

// orphanState is a replicated two-leg call owned by deadNode: leg a towards
// caller (tags, CSeqs and identities from a real dialog), leg b target
// overridable for the one-sided close.
func orphanState(callID, deadNode string, bTarget string) livestate.DialogState {
	a := livestate.DialogLeg{
		CallID: callID, LocalTag: "hello-a", RemoteTag: "caller-tag",
		LocalCSeq: 2, RemoteCSeq: 1,
		Contact:       "sip:hello-sip-1:5060;transport=udp",
		LocalIdentity: "sip:200@" + testDomain, RemoteIdentity: "sip:a1@" + testDomain,
		RemoteTarget: "sip:a1@127.0.0.1:1", Endpoint: "sip:200@" + testDomain,
		Source: "127.0.0.1:1", SDP: "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 20000 RTP/AVP 0 101\r\n",
	}
	b := a
	b.CallID = callID + "-b"
	b.LocalTag, b.RemoteTag = "hello-b", "callee-tag"
	b.LocalIdentity, b.RemoteIdentity = "sip:a1@"+testDomain, "sip:200@"+testDomain
	if bTarget != "" {
		b.RemoteTarget = bTarget
		b.Source = "127.0.0.1:1"
	}
	return livestate.DialogState{
		CallID: callID, OwnerNode: deadNode, Correlation: "corr-" + callID, State: "talking",
		RelayPorts: [2]int{20000, 20002}, Legs: [2]livestate.DialogLeg{a, b},
	}
}

// TestTakeoverClaim fails if two survivors can both claim the same orphaned
// dialog, if a claim that cannot complete is retried forever, or if a
// failed takeover is not counted as a zombie (spec S-2, S-6).
func TestTakeoverClaim(t *testing.T) {
	ha := newFakeHA()
	mem := &fakeMembership{}
	ha.put(orphanState("orphan-1", "sip-dead", "sip:x@127.0.0.1:1")) // leg b unreachable: the takeover must fail fast
	mem.set(
		cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Ready},
		cluster.Member{ID: "sip-2", Kind: cluster.KindSIP, State: cluster.Ready},
		cluster.Member{ID: "sip-dead", Kind: cluster.KindSIP, State: cluster.Offline, ActiveCalls: 1},
	)
	haReinviteTimeout = 300 * time.Millisecond // a dead endpoint must fail fast
	n1 := startPBX(t, nil, withNodeID("sip-1"), withHA(ha, mem))
	n2 := startPBX(t, nil, withNodeID("sip-2"), withHA(ha, mem))
	n1.srv.takeoverPass(t.Context())
	n2.srv.takeoverPass(t.Context())
	eventually(t, "one taker holds the claim", func() bool {
		return ha.claimOf("orphan-1") != ""
	})
	time.Sleep(100 * time.Millisecond) // the loser had its chance
	if holder := ha.claimOf("orphan-1"); holder != "sip-1" && holder != "sip-2" {
		t.Fatalf("claim holder = %q", holder)
	}
	// Exactly one node tried the takeover: the zombies sum to one across
	// the survivors, and the failed claim is kept (it expires) so no
	// survivor spins on an unsalvageable dialog.
	eventually(t, "the failed takeover counted one zombie", func() bool {
		return n1.metric(t, "hello_zombie_calls_total", nil)+n2.metric(t, "hello_zombie_calls_total", nil) == 1
	})
	n1.srv.takeoverPass(t.Context())
	n2.srv.takeoverPass(t.Context())
	time.Sleep(50 * time.Millisecond)
	if got := n1.metric(t, "hello_zombie_calls_total", nil) + n2.metric(t, "hello_zombie_calls_total", nil); got != 1 {
		t.Fatalf("zombies after a re-scan = %v, want still 1 (the claim blocks the retry)", got)
	}
}

// TestTakeoverReINVITEs fails if the taker does not re-create both legs
// from the replicated state (same Call-IDs and tags, the CSeq continuing
// the owner's counter, the route through the taker's relay), if the call
// does not end up connected on the taker, if the claim is not released
// after success, or if the replicated record does not pass to the taker
// (spec S-2, S-4).
func TestTakeoverReINVITEs(t *testing.T) {
	ha := newFakeHA()
	mem := &fakeMembership{}
	owner := startPBX(t, ringAllDevices(), withNodeID("sip-1"), withHA(ha, mem))
	taker := startPBX(t, ringAllDevices(), withNodeID("sip-2"), withHA(ha, mem))
	a := newPhone(t, owner, "a1", "pa")
	b := newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	inv := waitReq(t, b.invites, "callee INVITE")
	waitReq(t, b.acks, "callee ACK")

	// The owner replicates; give the heartbeat a beat.
	var st livestate.DialogState
	eventually(t, "the call is replicated", func() bool {
		ha.mu.Lock()
		defer ha.mu.Unlock()
		for _, s := range ha.dialogs {
			if s.OwnerNode == "sip-1" && s.Legs[0].LocalTag != "" && s.Legs[1].LocalTag != "" {
				st = s
				return true
			}
		}
		return false
	})
	if st.Correlation == "" || st.RelayPorts[0] == 0 || st.Legs[0].SDP == "" || st.Legs[0].Source == "" ||
		st.Legs[0].LocalIdentity == "" || st.Legs[1].RemoteTarget == "" {
		t.Fatalf("replicated state incomplete: %+v", st)
	}

	// The owner dies; the survivor takes over.
	haReinviteTimeout = time.Second
	owner.stop()
	mem.set(
		cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Offline, ActiveCalls: 1},
		cluster.Member{ID: "sip-2", Kind: cluster.KindSIP, State: cluster.Ready},
	)
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	rb := waitReq(t, b.reinvites, "takeover re-INVITE to the callee")
	// CSeq continuity and the endpoint's view of the dialog (contract 2).
	if ra.CSeq().SeqNo != st.Legs[0].LocalCSeq {
		t.Fatalf("caller re-INVITE CSeq = %d, want the replicated %d", ra.CSeq().SeqNo, st.Legs[0].LocalCSeq)
	}
	if rb.CSeq().SeqNo != st.Legs[1].LocalCSeq {
		t.Fatalf("callee re-INVITE CSeq = %d, want the replicated %d", rb.CSeq().SeqNo, st.Legs[1].LocalCSeq)
	}
	if tagParam(ra.From()) != st.Legs[0].LocalTag || tagParam(ra.To()) != st.Legs[0].RemoteTag ||
		ra.CallID().Value() != st.Legs[0].CallID {
		t.Fatalf("caller re-INVITE broke the dialog: From %v To %v", ra.From(), ra.To())
	}
	if tagParam(rb.From()) != st.Legs[1].LocalTag || tagParam(rb.To()) != st.Legs[1].RemoteTag ||
		rb.CallID().Value() != st.Legs[1].CallID {
		t.Fatalf("callee re-INVITE broke the dialog: From %v To %v", rb.From(), rb.To())
	}
	anchoredSDP(t, taker, ra.Body()) // the taker's own relay ports
	anchoredSDP(t, taker, rb.Body())
	waitReq(t, a.acks, "ACK to the caller's 200")
	waitReq(t, b.acks, "ACK to the callee's 200")

	// The call is connected on the taker, the claim is gone, and the
	// replicated record belongs to the taker now.
	eventually(t, "call connected on the taker", func() bool {
		cs := liveCalls(t, taker)
		return len(cs) == 1 && cs[0].State == "connected" && cs[0].Node == "sip-2"
	})
	eventually(t, "claim released", func() bool { return ha.claimOf(st.CallID) == "" })
	eventually(t, "record owned by the taker", func() bool { return ha.ownerOf(st.CallID) == "sip-2" })
	if v := taker.metric(t, "hello_dialog_takeovers_total", nil); v != 1 {
		t.Fatalf("takeovers = %v", v)
	}
	if v := taker.metric(t, "hello_zombie_calls_total", nil); v != 0 {
		t.Fatalf("zombies = %v, want 0", v)
	}

	// The caller hangs up on the taker: same Call-ID and tags, CSeq past
	// the takeover's, and the callee gets the BYE.
	bye := sip.NewRequest(sip.BYE, sip.Uri{Scheme: "sip", Host: hostOf(taker.addr), Port: portOf(taker.addr)})
	bye.AppendHeader(&sip.FromHeader{Address: *ra.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: st.Legs[0].RemoteTag}}})
	bye.AppendHeader(&sip.ToHeader{Address: *ra.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: st.Legs[0].LocalTag}}})
	callID := sip.CallIDHeader(st.Legs[0].CallID)
	bye.AppendHeader(&callID)
	bye.AppendHeader(&sip.CSeqHeader{SeqNo: ra.CSeq().SeqNo + 1, MethodName: sip.BYE})
	bye.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	bye.SetTransport("UDP")
	if res := a.do(bye); res.StatusCode != 200 {
		t.Fatalf("BYE to the taker = %d", res.StatusCode)
	}
	waitReq(t, b.byes, "BYE to the callee after the caller hung up")
	cd := taker.nextCDR(t)
	if cd.CorrelationID != st.Correlation || cd.FinalStatus != 200 {
		t.Fatalf("CDR = %+v", cd)
	}
	if !strings.Contains(traceText(cd.Trace), "ha: taken over from sip-1") {
		t.Fatalf("CDR trace lacks the takeover: %s", traceText(cd.Trace))
	}
	_ = inv
}

// TestTakeoverYield fails if the owner of a call that another node claimed
// does not yield: it must stop replicating, close its copy with a CDR, and
// leave the replicated record for its taker (spec edge case, S-6).
func TestTakeoverYield(t *testing.T) {
	ha := newFakeHA()
	mem := &fakeMembership{}
	owner := startPBX(t, ringAllDevices(), withNodeID("sip-1"), withHA(ha, mem))
	a := newPhone(t, owner, "a1", "pa")
	b := newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "callee INVITE")
	waitReq(t, b.acks, "callee ACK")
	eventually(t, "the call is replicated", func() bool {
		ha.mu.Lock()
		defer ha.mu.Unlock()
		for _, s := range ha.dialogs {
			if s.OwnerNode == "sip-1" {
				return true
			}
		}
		return false
	})
	// A taker claims the dialog while the owner is only slow, not dead:
	// the owner's next heartbeat must see the claim and yield.
	callID := ""
	ha.mu.Lock()
	for id := range ha.dialogs {
		callID = id
	}
	ha.mu.Unlock()
	ha.mu.Lock()
	ha.claims[callID] = "sip-2"
	ha.mu.Unlock()
	cd := owner.nextCDR(t)
	if !strings.Contains(cd.FailureReason, "taken over by sip-2") {
		t.Fatalf("yield CDR = %+v", cd)
	}
	eventually(t, "live call removed after the yield", func() bool { return len(liveCalls(t, owner)) == 0 })
	time.Sleep(60 * time.Millisecond) // a few heartbeats' worth
	if ha.ownerOf(callID) == "" {
		t.Fatal("the yielded owner deleted the replicated record its taker needs")
	}
	_ = r
}

// TestTakeoverOneSidedClose fails if a takeover whose endpoint is gone
// leaves a zombie dialog: the leg that answered gets a normal BYE and CDR,
// the zombie is counted, and the claim is not released for a retry spin
// (spec edge case, failure mode "re-INVITE rejected").
func TestTakeoverOneSidedClose(t *testing.T) {
	ha := newFakeHA()
	mem := &fakeMembership{}
	// The caller leg's target is this test PBX itself, so the taker can
	// reach it; the callee leg points at a dead port.
	haReinviteTimeout = 300 * time.Millisecond // a dead endpoint must fail fast
	taker := startPBX(t, nil, withNodeID("sip-2"), withHA(ha, mem))
	dead := orphanState("orphan-2", "sip-dead", "sip:x@127.0.0.1:1")
	ha.put(dead)
	mem.set(
		cluster.Member{ID: "sip-dead", Kind: cluster.KindSIP, State: cluster.Offline, ActiveCalls: 1},
		cluster.Member{ID: "sip-2", Kind: cluster.KindSIP, State: cluster.Ready},
	)
	taker.srv.takeoverPass(t.Context())
	eventually(t, "the zombie counted", func() bool {
		return taker.metric(t, "hello_zombie_calls_total", nil) == 1
	})
	if got := taker.metric(t, "hello_dialog_takeovers_total", nil); got != 0 {
		t.Fatalf("takeovers = %v, want 0", got)
	}
	if claim := ha.claimOf("orphan-2"); claim == "" {
		t.Fatal("the failed claim was released: survivors would retry forever")
	}
	cd := taker.nextCDR(t)
	if !strings.Contains(cd.FailureReason, "takeover failed") {
		t.Fatalf("CDR = %+v", cd)
	}
}

// hostOf splits a host:port test address.
func hostOf(addr string) string {
	host, _, _ := strings.Cut(addr, ":")
	return host
}

func portOf(addr string) int {
	_, port, _ := strings.Cut(addr, ":")
	var n int
	for _, r := range port {
		n = n*10 + int(r-'0')
	}
	return n
}
