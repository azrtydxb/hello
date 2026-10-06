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
	// Additive (Phase 7, omitempty on the wire): the endpoint request
	// source the owner matched dialogs on, and both sides' identity URIs,
	// which a taker needs to build in-dialog requests that keep the
	// endpoint's view of the dialog unchanged.
	Source         string `json:"source,omitempty"`
	LocalIdentity  string `json:"localIdentity,omitempty"`
	RemoteIdentity string `json:"remoteIdentity,omitempty"`
	// RemoteSDP is the endpoint's own last SDP (SDP above is Hello's side
	// of the negotiation): a taker of a call Hello answered itself binds
	// fresh media for the endpoint's codecs and address from it.
	RemoteSDP string `json:"remoteSdp,omitempty"`
}

// DialogState is a call's full recovery state.
type DialogState struct {
	CallID      string       `json:"callId"`
	OwnerNode   string       `json:"ownerNode"`
	Correlation string       `json:"correlation"`
	State       string       `json:"state"` // talking | hold | transferring | recording | announcement | voicemail
	StateDetail string       `json:"stateDetail,omitempty"`
	Legs        [2]DialogLeg `json:"legs"`
	RelayPorts  [2]int       `json:"relayPorts"`
	UpdatedAt   time.Time    `json:"updatedAt"`
	// Additive (Phase 7 gaps): the call's caller and destination numbers
	// as the CDR and live view show them; a transferred call's destination
	// is its target, not what its caller's dialog first dialled.
	Caller      string `json:"caller,omitempty"`
	Destination string `json:"destination,omitempty"`
	// Handoff marks a dialog its live owner is handing over (the owner is
	// draining): survivors claim it at once, without waiting for the owner
	// to go OFFLINE or its heartbeat to age.
	Handoff bool `json:"handoff,omitempty"`
	// AnsweredAt is when the call was first answered: the start of its
	// maximum-duration clock, which a taker continues instead of
	// restarting (zero from older nodes: the taker starts it afresh).
	AnsweredAt time.Time `json:"answeredAt,omitzero"`
	// OwnerIncarnation is the owning process's incarnation (cluster
	// membership's Incarnation): a record whose owner node is alive under
	// another incarnation belongs to a process that died — a crash
	// restarted in place under the same node ID — and is taken over at
	// once. Empty from older nodes (never treated as stale).
	OwnerIncarnation string `json:"ownerIncarnation,omitempty"`
	// The CDR continuity a taker needs to close the one logical call with
	// one correct CDR: when the call started and first rang, how it was
	// routed, and the routing trace so far.
	StartedAt time.Time `json:"startedAt,omitzero"`
	RingAt    time.Time `json:"ringAt,omitzero"`
	Direction string    `json:"direction,omitempty"`
	Route     string    `json:"route,omitempty"`
	Trunk     string    `json:"trunk,omitempty"`
	Rewritten string    `json:"rewritten,omitempty"`
	Trace     []string  `json:"trace,omitempty"`
}

// LegCallIDs are the Call-IDs of the dialogs the record carries.
func (d DialogState) LegCallIDs() []string {
	out := []string{d.CallID}
	if id := d.Legs[1].CallID; id != "" && id != d.CallID {
		out = append(out, id)
	}
	return out
}

// HA timing, scaled to membership (1s heartbeat, 4s TTL): the owner
// refreshes its records every heartbeat (1s, the member heartbeat), a
// record younger than two heartbeats (2s) belongs to a live owner and is
// never claimed on OFFLINE grounds (membership needs 4s without a
// heartbeat to say OFFLINE), and records expire 10s after the last write:
// past detection (4s), the takeover poll (≤1.5s) and both re-INVITEs (≤3s
// each run in sequence only on a failing leg), so a dead owner's call is
// still claimable when it is found.
const (
	haTTL       = 10 * time.Second
	haHeartbeat = time.Second
)

// HAHeartbeat is the replication heartbeat interval. A var so tests can
// shrink the freshness window with it.
var HAHeartbeat = haHeartbeat

// DialogTTL is how long a dialog record lives without a refresh.
const DialogTTL = haTTL

func dialogKey(callId string) string      { return "hello:dialog:" + callId }
func dialogClaimKey(callId string) string { return "hello:dialog-claim:" + callId }
func dialogTakenKey(node string) string   { return "hello:dialog-taken:" + node }
func dialogLegKey(callId string) string   { return "hello:dialog-leg:" + callId }

// takenNode is the key the zombie reaper counts a claim under: the owner
// node, or for a claim on a dead incarnation of a node that is alive again
// (restarted in place), that incarnation - the reaper of the node's later
// death must not subtract claims on calls of an earlier process.
func takenNode(owner, staleIncarnation string) string {
	if staleIncarnation == "" {
		return owner
	}
	return owner + "@" + staleIncarnation
}

// dialogTakenTTL bounds the per-node claim counter: long past any reap.
const dialogTakenTTL = 10 * time.Minute

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
	cmds := []valkey.Completed{s.c.B().Set().Key(dialogKey(sds.CallID)).Value(string(b)).Px(ttl).Build()}
	// The callee leg's Call-ID points at the record too, so a node that
	// gets an in-dialog request on either dialog finds the call.
	if id := sds.Legs[1].CallID; id != "" && id != sds.CallID {
		cmds = append(cmds, s.c.B().Set().Key(dialogLegKey(id)).Value(sds.CallID).Px(ttl).Build())
	}
	for _, r := range s.c.DoMulti(ctx, cmds...) {
		if err := r.Error(); err != nil {
			return err
		}
	}
	return nil
}

// DialogByLeg finds the replicated record carrying the dialog callId (the
// caller's or the callee's leg); ok is false when there is none.
func (s *Store) DialogByLeg(ctx context.Context, callId string) (DialogState, bool, error) {
	var out DialogState
	raw, err := s.c.Do(ctx, s.c.B().Get().Key(dialogKey(callId)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		key, lerr := s.c.Do(ctx, s.c.B().Get().Key(dialogLegKey(callId)).Build()).ToString()
		if valkey.IsValkeyNil(lerr) {
			return out, false, nil
		}
		if lerr != nil {
			return out, false, fmt.Errorf("livestate: dialog leg get: %w", lerr)
		}
		raw, err = s.c.Do(ctx, s.c.B().Get().Key(dialogKey(key)).Build()).ToString()
	}
	if valkey.IsValkeyNil(err) {
		return out, false, nil
	}
	if err != nil {
		return out, false, fmt.Errorf("livestate: dialog get: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, false, fmt.Errorf("livestate: dialog decode: %w", err)
	}
	return out, true, nil
}

// ClaimDialog atomically claims an orphaned dialog for newNode: it succeeds
// only while the dialog has no live owner heartbeat and no other claim, and
// returns the replicated state on success. A dialog whose state was written
// within two heartbeats is refused: that owner is very likely alive (Phase 3
// membership marks a node OFFLINE long after that) - unless the record is
// marked for handoff, or staleIncarnation names the record's owner
// incarnation: that process is known dead (its node is alive under another
// incarnation), however fresh its last write. A non-empty staleIncarnation
// that does not match the record refuses the claim. The claim is a
// compare-and-set on the record as read: a record rewritten in between (the
// owner alive after all, or another taker that already re-homed the call
// and released its claim) is never claimed. The claim expires, so a taker
// that dies mid-takeover cannot block the dialog forever, and is dropped by
// ReleaseDialogClaim after a successful takeover.
func (s *Store) ClaimDialog(ctx context.Context, callId, newNode, staleIncarnation string) (bool, DialogState, error) {
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
	if staleIncarnation != "" && out.OwnerIncarnation != staleIncarnation {
		return false, out, nil // not the dead process's record (any more)
	}
	if staleIncarnation == "" && !out.Handoff && !out.UpdatedAt.IsZero() && time.Since(out.UpdatedAt) < 2*HAHeartbeat {
		return false, out, nil // the owner's heartbeat is still fresh
	}
	claim := claimOrphan.Exec(ctx, s.c,
		[]string{dialogClaimKey(callId), dialogKey(callId), dialogTakenKey(takenNode(out.OwnerNode, staleIncarnation))},
		[]string{newNode, strconv.FormatInt(haTTL.Milliseconds(), 10), strconv.FormatInt(dialogTakenTTL.Milliseconds(), 10), raw})
	n, err := claim.AsInt64()
	if err != nil {
		return false, out, fmt.Errorf("livestate: dialog claim: %w", err)
	}
	return n == 1, out, nil
}

// claimOrphan sets the claim key only while the dialog state is still
// exactly the one the claimant read (ARGV[4]) and no other claim exists, in
// one atomic step (two survivors can never both win, and a record rewritten
// since the read is never claimed on stale information), and counts the
// claim against the dead owner (KEYS[3]), so the zombie reaper knows how
// many of its calls a survivor took on.
var claimOrphan = valkey.NewLuaScript(`
local state = redis.call('GET', KEYS[2])
if not state or state ~= ARGV[4] then return 0 end
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', tonumber(ARGV[2]))
redis.call('INCR', KEYS[3])
redis.call('PEXPIRE', KEYS[3], tonumber(ARGV[3]))
return 1`)

// TakenOver is how many of node's dialogs survivors have claimed (each
// claim counted once, atomically with it). The zombie reaper subtracts it
// from the node's live calls at death: the rest had no replicated state.
// A claim on a dead incarnation of a node restarted in place is counted
// apart (takenNode): that node never goes OFFLINE for it.
func (s *Store) TakenOver(ctx context.Context, node string) (int, error) {
	n, err := s.c.Do(ctx, s.c.B().Get().Key(dialogTakenKey(node)).Build()).AsInt64()
	if valkey.IsValkeyNil(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("livestate: dialog taken count: %w", err)
	}
	return int(n), nil
}

// ReleaseDialogClaim drops the taker's claim: the call belongs to the taker
// now, and the restarted owner must not retake it (the owner also yields to
// any claim it finds through ClaimOwner on its next heartbeat).
func (s *Store) ReleaseDialogClaim(ctx context.Context, callId string) error {
	return s.c.Do(ctx, s.c.B().Del().Key(dialogClaimKey(callId)).Build()).Error()
}

// DeleteDialogState removes an ended call's record, so its dialog is never
// offered to takers.
func (s *Store) DeleteDialogState(ctx context.Context, callId string) error {
	return s.c.Do(ctx, s.c.B().Del().Key(dialogKey(callId)).Build()).Error()
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

// DialogOwner names the node a dialog's record says owns it ("" when there
// is no record). An owner handing a call over yields once this names
// another node.
func (s *Store) DialogOwner(ctx context.Context, callId string) (string, error) {
	raw, err := s.c.Do(ctx, s.c.B().Get().Key(dialogKey(callId)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("livestate: dialog get: %w", err)
	}
	var sds DialogState
	if err := json.Unmarshal([]byte(raw), &sds); err != nil {
		return "", fmt.Errorf("livestate: dialog decode: %w", err)
	}
	return sds.OwnerNode, nil
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
