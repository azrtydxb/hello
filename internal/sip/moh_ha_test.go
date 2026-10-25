package sip

import (
	"context"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// TestMusicOnHoldTakeover fails if the music a held party hears does not
// resume on the surviving node after the anchor's node dies mid-hold
// (in-call HA: the call keeps its hold, and S-33's music follows it).
func TestMusicOnHoldTakeover(t *testing.T) {
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/hold1.wav", loudWAV())
	feats := func(s *snapshot.Snapshot) {
		s.WithMOHClasses([]snapshot.MOHClass{{ID: 1, Name: "default", IsDefault: true, Names: []string{"hold1"}, Objects: []string{"ann/hold1.wav"}}})
	}
	ha, mem := newFakeHA(), &fakeMembership{}
	owner := startPBX(t, ringAllDevices(), withNodeID("sip-1"), withHA(ha, mem), withMedia(false), withFeatures(feats), withVoicemail(newVMStore(), objs))
	taker := startPBX(t, ringAllDevices(), withNodeID("sip-2"), withHA(ha, mem), withMedia(false), withFeatures(feats), withVoicemail(newVMStore(), objs))
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	listenRTP(t, a)
	bRTP := listenRTP(t, b)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	waitReq(t, b.invites, "callee INVITE")
	waitReq(t, b.acks, "callee ACK")

	hold := a.sdp + "a=sendonly\r\n"
	re := sip.NewRequest(sip.INVITE, r.dcs.InviteResponse.Contact().Address)
	re.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	re.SetBody([]byte(hold))
	if res, err := r.dcs.Do(t.Context(), re); err != nil || res.StatusCode != 200 {
		t.Fatalf("hold re-INVITE = %v, %v", res, err)
	}
	if n, _ := rtpCount(bRTP, time.Second); n < 20 {
		t.Fatalf("the held callee heard %d packets on the owner, want music", n)
	}
	eventually(t, "the hold and its music are replicated", func() bool {
		ha.mu.Lock()
		defer ha.mu.Unlock()
		for _, s := range ha.dialogs {
			if s.OwnerNode == "sip-1" && s.State == "hold" && s.StateDetail == "moh:1:b" {
				return true
			}
		}
		return false
	})

	haReinviteTimeout = time.Second
	owner.stop()
	mem.set(
		cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Offline, ActiveCalls: 1},
		cluster.Member{ID: "sip-2", Kind: cluster.KindSIP, State: cluster.Ready},
	)
	taker.srv.takeoverPass(t.Context())
	waitReq(t, a.reinvites, "takeover re-INVITE to the holder")
	waitReq(t, b.reinvites, "takeover re-INVITE to the held callee")
	waitReq(t, a.acks, "ACK to the holder")
	waitReq(t, b.acks, "ACK to the held callee")
	rtpCount(bRTP, 100*time.Millisecond)
	if n, peak := rtpCount(bRTP, 1500*time.Millisecond); n < 40 || peak < 500 {
		t.Fatalf("after the takeover the held callee heard %d packets (peak %d), want the music again", n, peak)
	}
}
