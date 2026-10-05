// Dialog replication and takeover claims for in-call HA (incall-ha plan
// contract 1). The node owning a call writes its full recovery state here on
// every state change and on a heartbeat; when Phase 3 membership marks that
// node OFFLINE, a surviving node finds the dialog through OrphanedDialogs and
// claims it atomically through ClaimDialog.
package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// DialogLeg is one leg's full recovery state: everything needed to continue
// the dialog from another node (Call-ID, tags, CSeq counters, route set,
// contact, remote target, negotiated SDP, the endpoint's AOR and the media
// address it latched onto).
type DialogLeg struct {
	CallID       string   `json:"callId"`
	LocalTag     string   `json:"localTag"`
	RemoteTag    string   `json:"remoteTag"`
	LocalCSeq    uint32   `json:"localCSeq"`
	RemoteCSeq   uint32   `json:"remoteCSeq"`
	RouteSet     []string `json:"routeSet"`
	Contact      string   `json:"contact"`
	RemoteTarget string   `json:"remoteTarget"`
	SDP          string   `json:"sdp"`
	Endpoint     string   `json:"endpoint"`
	LatchedAddr  string   `json:"latchedAddr"`
}

// DialogState is a call's full recovery state.
type DialogState struct {
	CallID      string       `json:"callId"`
	OwnerNode   string       `json:"ownerNode"`
	Correlation string       `json:"correlation"`
	State       string       `json:"state"` // talking | hold | transferring | recording | announcement
	StateDetail string       `json:"stateDetail,omitempty"`
	Legs        [2]DialogLeg `json:"legs"`
	RelayPorts  [2]int       `json:"relayPorts"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

// HA timing: the owner refreshes its records every heartbeat and they expire
// 30s after the last one (three missed heartbeats plus margin).
const (
	haTTL       = 30 * time.Second
	haHeartbeat = 5 * time.Second
)

// HAHeartbeat is the replication heartbeat interval. A var so tests can
// shrink the freshness window with it.
var HAHeartbeat = haHeartbeat

// DialogTTL is how long a dialog record lives without a refresh.
const DialogTTL = haTTL

func dialogKey(callId string) string      { return "hello:dialog:" + callId }
func dialogClaimKey(callId string) string { return "hello:dialog-claim:" + callId }

// SaveDialogState writes s with the HA TTL: the owner's replication write and
// its heartbeat are the same call, so the record's expiry is its heartbeat.
func (s *Store) SaveDialogState(ctx context.Context, sds DialogState, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = haTTL
	}
	sds.UpdatedAt = time.Now().UTC()
	b, err := json.Marshal(sds)
	if err != nil {
		return err
	}
	return s.c.Do(ctx, s.c.B().Set().Key(dialogKey(sds.CallID)).Value(string(b)).Px(ttl).Build()).Error()
}

// ClaimDialog atomically claims an orphaned dialog for newNode: it succeeds
// only while the dialog has no live owner heartbeat and no other claim, and
// returns the replicated state on success. A dialog whose state was written
// within two heartbeats is refused: that owner is very likely alive (Phase 3
// membership marks a node OFFLINE long after that). The claim expires, so a
// taker that dies mid-takeover cannot block the dialog forever, and is
// dropped by ReleaseDialogClaim after a successful takeover.
func (s *Store) ClaimDialog(ctx context.Context, callId, newNode string) (bool, DialogState, error) {
	var out DialogState
	raw, err := s.c.Do(ctx, s.c.B().Get().Key(dialogKey(callId)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return false, out, nil // state expired: nothing to take over
	}
	if err != nil {
		return false, out, fmt.Errorf("livestate: dialog get: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return false, out, fmt.Errorf("livestate: dialog decode: %w", err)
	}
	if !out.UpdatedAt.IsZero() && time.Since(out.UpdatedAt) < 2*HAHeartbeat {
		return false, out, nil // the owner's heartbeat is still fresh
	}
	claim := claimOrphan.Exec(ctx, s.c, []string{dialogClaimKey(callId), dialogKey(callId)},
		[]string{newNode, strconv.FormatInt(haTTL.Milliseconds(), 10)})
	n, err := claim.AsInt64()
	if err != nil {
		return false, out, fmt.Errorf("livestate: dialog claim: %w", err)
	}
	return n == 1, out, nil
}

// claimOrphan sets the claim key only while the dialog state still exists and
// no other claim does, in one atomic step (two survivors can never both win).
var claimOrphan = valkey.NewLuaScript(`
local state = redis.call('GET', KEYS[2])
if not state then return 0 end
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', tonumber(ARGV[2]))
return 1`)

// ReleaseDialogClaim drops the taker's claim: the call belongs to the taker
// now, and the restarted owner must not retake it (the owner also yields to
// any claim it finds through ClaimOwner on its next heartbeat).
func (s *Store) ReleaseDialogClaim(ctx context.Context, callId string) error {
	return s.c.Do(ctx, s.c.B().Del().Key(dialogClaimKey(callId)).Build()).Error()
}

// ClaimOwner names the node holding the dialog's takeover claim ("" when
// none). The owner calls it on its replication heartbeat: finding someone
// else's claim means its call was taken over and it must yield.
func (s *Store) ClaimOwner(ctx context.Context, callId string) (string, error) {
	v, err := s.c.Do(ctx, s.c.B().Get().Key(dialogClaimKey(callId)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("livestate: dialog claim get: %w", err)
	}
	return v, nil
}

// OrphanedDialogs returns the replicated states of dialogs owned by
// offlineNode that nobody has claimed yet. The caller decides OFFLINE from
// Phase 3 membership and passes the node's ID; the claim filter keeps a
// dialog another survivor already took out of the list.
func (s *Store) OrphanedDialogs(ctx context.Context, offlineNode string) ([]DialogState, error) {
	keys, err := s.scan(ctx, "hello:dialog:*")
	if err != nil {
		return nil, err
	}
	var out []DialogState
	for _, k := range keys {
		v, err := s.c.Do(ctx, s.c.B().Get().Key(k).Build()).ToString()
		if valkey.IsValkeyNil(err) {
			continue // expired between SCAN and GET
		}
		if err != nil {
			return nil, fmt.Errorf("livestate: dialog get: %w", err)
		}
		var sds DialogState
		if err := json.Unmarshal([]byte(v), &sds); err != nil {
			return nil, fmt.Errorf("livestate: dialog decode: %w", err)
		}
		if sds.OwnerNode != offlineNode {
			continue
		}
		claimed, err := s.c.Do(ctx, s.c.B().Exists().Key(dialogClaimKey(sds.CallID)).Build()).AsInt64()
		if err != nil {
			return nil, fmt.Errorf("livestate: dialog claim check: %w", err)
		}
		if claimed == 1 {
			continue
		}
		out = append(out, sds)
	}
	return out, nil
}
