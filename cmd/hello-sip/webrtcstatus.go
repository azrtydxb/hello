package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
)

// webrtcStatusEvery is how often a node publishes its WebRTC status; the key
// lives three periods, so a node that stops drops out within half a minute.
const webrtcStatusEvery = 10 * time.Second

// webrtcPublisher is what the status loop writes to; *livestate.Store
// implements it.
type webrtcPublisher interface {
	PutWebRTCStatus(ctx context.Context, st livestate.WebRTCNodeStatus, ttl time.Duration) error
	DeleteWebRTCStatus(ctx context.Context, node string) error
}

// runWebRTCStatus publishes the node's WebRTC legs, failures and counters for
// the Diagnostics "Clients" tab (clients S-52) until ctx ends, then removes
// them.
func runWebRTCStatus(ctx context.Context, pub webrtcPublisher, node *media.WebRTCNode, nodeID string, log *slog.Logger) {
	t := time.NewTicker(webrtcStatusEvery)
	defer t.Stop()
	for {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if err := pub.PutWebRTCStatus(pctx, webrtcStatus(node, nodeID, time.Now()), 3*webrtcStatusEvery); err != nil && ctx.Err() == nil {
			log.Debug("webrtc status publish failed", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			dctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = pub.DeleteWebRTCStatus(dctx, nodeID)
			cancel()
			return
		case <-t.C:
		}
	}
}

// webrtcStatus is the node's status now.
func webrtcStatus(node *media.WebRTCNode, nodeID string, now time.Time) livestate.WebRTCNodeStatus {
	st := livestate.WebRTCNodeStatus{Node: nodeID, Port: node.Mux().Port(), Hosts: node.Hosts(), UpdatedAt: now.UTC(),
		Legs: []livestate.WebRTCLeg{}, Failures: []livestate.WebRTCFailure{}}
	for _, l := range media.WebRTCLegs(livestate.WebRTCLegsCap) {
		st.Legs = append(st.Legs, livestate.WebRTCLeg{
			Call: l.Call, Side: l.Side, Ringing: l.Ringing, ICE: l.ICE, DTLS: l.DTLS, DTLSReason: l.DTLSReason,
			DTLSMillis: l.DTLSMillis, ICERestarts: l.ICERestarts, Remote: l.Remote, Video: l.Video,
			SRTPAuthFailures: l.SRTPAuthFailures, Since: l.Since,
		})
	}
	for _, f := range media.WebRTCRecentFailures() {
		st.Failures = append(st.Failures, livestate.WebRTCFailure{At: f.At, Call: f.Call, Side: f.Side, Kind: f.Kind, Reason: f.Reason, Detail: f.Detail})
	}
	c := media.WebRTCStats()
	st.Counters = livestate.WebRTCCounters{ICE: c.ICE, DTLSHandshakes: c.DTLSHandshakes, DTLSFailures: c.DTLSFailures, SRTPAuthFailures: c.SRTPAuthFailures}
	return st
}
