package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Call park (phone-feature-parity spec S-6) keeps one record per occupied
// slot under hello:park:slot:<slot>, so any hello-sip node can see which
// slots are taken and which node holds the parked call's dialog and media.
// A node that parks a call refreshes the record's expiry while it holds the
// call; a node that dies lets it lapse (or its in-call HA taker re-puts it).
//
// A retrieve or pickup that lands on another node than the one holding the
// call is carried over a small command mailbox: the retrieving node pushes a
// ParkCommand to hello:park:cmd:<node>, the holder takes it, does the media
// work (it alone owns the relay) and posts a ParkReply the retriever polls.

const (
	parkSlotPrefix  = "hello:park:slot:"
	parkCmdPrefix   = "hello:park:cmd:"
	parkReplyPrefix = "hello:park:reply:"
	// ParkCommandTTL bounds how long a command waits for its node and a
	// reply is kept.
	ParkCommandTTL = 30 * time.Second
)

// The phases of a parked call.
const (
	// ParkParked is a call waiting in its slot.
	ParkParked = "parked"
	// ParkReturning is a call whose slot timed out and that rings its
	// parker (or the parker's voicemail) again; it can no longer be
	// retrieved from the slot.
	ParkReturning = "returning"
)

// ParkedCall is one occupied slot.
type ParkedCall struct {
	Slot string `json:"slot"`
	Lot  string `json:"lot"`
	// Node holds the parked call's dialog and media; CallID is the Call-ID
	// of the parked party's dialog on that node, the key commands name it by.
	Node        string `json:"node"`
	CallID      string `json:"callId"`
	Correlation string `json:"correlationId"`
	// ParkedBy is the extension that parked the call; Caller and CallerName
	// are the parked party as the retriever sees it.
	ParkedBy   string    `json:"parkedBy"`
	Caller     string    `json:"caller"`
	CallerName string    `json:"callerName"`
	ParkedAt   time.Time `json:"parkedAt"`
	// ReturnAt is when the call rings its parker again.
	ReturnAt time.Time `json:"returnAt"`
	Phase    string    `json:"phase"`
}

// Park command operations.
const (
	// ParkOpRetrieve connects the retriever to the call parked in Slot.
	ParkOpRetrieve = "retrieve"
	// ParkOpPickup connects the picker to the ringing call Correlation.
	ParkOpPickup = "pickup"
	// ParkOpBye ends the call a linked peer on another node was part of.
	ParkOpBye = "bye"
	// ParkOpHold tells the linked peer on another node that its party was
	// put on hold (Dir sendonly or inactive) or resumed (sendrecv), so the
	// peer's phone is re-INVITEd the same way.
	ParkOpHold = "hold"
)

// ParkCommand asks the node holding a call to do something to it.
type ParkCommand struct {
	ID string `json:"id"`
	Op string `json:"op"`
	// From is the node that sent it, where the peer of a linked call lives.
	From string `json:"from"`
	Slot string `json:"slot,omitempty"`
	// CallID names the target dialog (retrieve, bye); Correlation the
	// ringing call (pickup).
	CallID      string `json:"callId,omitempty"`
	Correlation string `json:"correlationId,omitempty"`
	// Offer is the retriever's SDP offer; PeerCallID the Call-ID of the
	// retriever's own dialog on node From, so a later hang-up can name it.
	Offer      string `json:"offer,omitempty"`
	PeerCallID string `json:"peerCallId,omitempty"`
	// Retriever is the extension taking the call (for scope checks and the
	// trace).
	Retriever string `json:"retriever,omitempty"`
	// Dir is the media direction of a hold command, as the holding party
	// offered it.
	Dir string `json:"dir,omitempty"`
}

// ParkReply is the holder's answer to a ParkCommand.
type ParkReply struct {
	ID string `json:"id"`
	OK bool   `json:"ok"`
	// Code and Reason say why a failed command failed, as a SIP status.
	Code   int    `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Answer is the SDP answer for the retriever's offer; CallID the
	// Call-ID of the holder's dialog, which the retriever's node names in a
	// later bye.
	Answer string `json:"answer,omitempty"`
	CallID string `json:"callId,omitempty"`
}

var (
	// parkRefreshScript extends a record's expiry only while it is still
	// the same call's (KEYS[1]: slot key, ARGV[1]: Call-ID, ARGV[2]: ms).
	parkRefreshScript = valkey.NewLuaScript(`
local v = redis.call('GET', KEYS[1])
if not v then return 0 end
local d = cjson.decode(v)
if d.callId ~= ARGV[1] then return 0 end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1`)
	// parkReleaseScript deletes a record only while it is the same call's.
	parkReleaseScript = valkey.NewLuaScript(`
local v = redis.call('GET', KEYS[1])
if not v then return 0 end
local d = cjson.decode(v)
if d.callId ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1])
return 1`)
	// parkUpdateScript replaces a record in place while it is still the
	// same call's, keeping the remaining expiry (ARGV[1]: Call-ID, ARGV[2]:
	// the new record, ARGV[3]: expiry in ms).
	parkUpdateScript = valkey.NewLuaScript(`
local v = redis.call('GET', KEYS[1])
if not v then return 0 end
local d = cjson.decode(v)
if d.callId ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return 1`)
)

// ParkReserve occupies the slot for p with the given expiry; false means the
// slot is taken.
func (s *Store) ParkReserve(ctx context.Context, p ParkedCall, ttl time.Duration) (bool, error) {
	v, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	err = s.c.Do(ctx, s.c.B().Set().Key(parkSlotPrefix+p.Slot).Value(string(v)).Nx().Px(ttl).Build()).Error()
	if valkey.IsValkeyNil(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("livestate: reserve park slot: %w", err)
	}
	return true, nil
}

// ParkGet returns the call parked in slot.
func (s *Store) ParkGet(ctx context.Context, slot string) (p ParkedCall, ok bool, err error) {
	v, err := s.c.Do(ctx, s.c.B().Get().Key(parkSlotPrefix+slot).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return p, false, nil
	}
	if err != nil {
		return p, false, fmt.Errorf("livestate: get park slot: %w", err)
	}
	if err := json.Unmarshal([]byte(v), &p); err != nil {
		return p, false, fmt.Errorf("livestate: decode park slot: %w", err)
	}
	return p, true, nil
}

// ParkClaim removes and returns the call parked in slot, atomically: of two
// retrievers racing for the slot exactly one gets the call.
func (s *Store) ParkClaim(ctx context.Context, slot string) (p ParkedCall, ok bool, err error) {
	v, err := s.c.Do(ctx, s.c.B().Getdel().Key(parkSlotPrefix+slot).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return p, false, nil
	}
	if err != nil {
		return p, false, fmt.Errorf("livestate: claim park slot: %w", err)
	}
	if err := json.Unmarshal([]byte(v), &p); err != nil {
		return p, false, fmt.Errorf("livestate: decode park slot: %w", err)
	}
	return p, true, nil
}

// ParkRefresh extends the expiry of the slot's record while it is still the
// call callID's; false means the slot is no longer this call's.
func (s *Store) ParkRefresh(ctx context.Context, slot, callID string, ttl time.Duration) (bool, error) {
	n, err := parkRefreshScript.Exec(ctx, s.c, []string{parkSlotPrefix + slot},
		[]string{callID, fmt.Sprint(ttl.Milliseconds())}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("livestate: refresh park slot: %w", err)
	}
	return n == 1, nil
}

// ParkUpdate replaces the slot's record while it is still the call's.
func (s *Store) ParkUpdate(ctx context.Context, p ParkedCall, ttl time.Duration) (bool, error) {
	v, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	n, err := parkUpdateScript.Exec(ctx, s.c, []string{parkSlotPrefix + p.Slot},
		[]string{p.CallID, string(v), fmt.Sprint(ttl.Milliseconds())}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("livestate: update park slot: %w", err)
	}
	return n == 1, nil
}

// ParkRelease frees the slot while it is still the call callID's.
func (s *Store) ParkRelease(ctx context.Context, slot, callID string) error {
	err := parkReleaseScript.Exec(ctx, s.c, []string{parkSlotPrefix + slot}, []string{callID}).Error()
	if err != nil {
		return fmt.Errorf("livestate: release park slot: %w", err)
	}
	return nil
}

// ParkList returns every occupied slot, ordered by slot.
func (s *Store) ParkList(ctx context.Context) ([]ParkedCall, error) {
	var keys []string
	var cursor uint64
	for {
		e, err := s.c.Do(ctx, s.c.B().Scan().Cursor(cursor).Match(parkSlotPrefix+"*").Count(100).Build()).AsScanEntry()
		if err != nil {
			return nil, fmt.Errorf("livestate: scan park slots: %w", err)
		}
		keys = append(keys, e.Elements...)
		if e.Cursor == 0 {
			break
		}
		cursor = e.Cursor
	}
	out := []ParkedCall{}
	if len(keys) == 0 {
		return out, nil
	}
	vals, err := s.c.Do(ctx, s.c.B().Mget().Key(keys...).Build()).ToArray()
	if err != nil {
		return nil, fmt.Errorf("livestate: read park slots: %w", err)
	}
	for _, v := range vals {
		raw, err := v.ToString()
		if err != nil || raw == "" {
			continue // expired between the scan and the read
		}
		var p ParkedCall
		if json.Unmarshal([]byte(raw), &p) == nil {
			out = append(out, p)
		}
	}
	SortParked(out)
	return out, nil
}

// SortParked orders parked calls by slot number (numerically).
func SortParked(ps []ParkedCall) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && slotLess(ps[j].Slot, ps[j-1].Slot); j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

func slotLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// ParkSend queues a command for node.
func (s *Store) ParkSend(ctx context.Context, node string, cmd ParkCommand) error {
	v, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	for _, res := range s.c.DoMulti(ctx,
		s.c.B().Rpush().Key(parkCmdPrefix+node).Element(string(v)).Build(),
		s.c.B().Pexpire().Key(parkCmdPrefix+node).Milliseconds(ParkCommandTTL.Milliseconds()).Build(),
	) {
		if err := res.Error(); err != nil {
			return fmt.Errorf("livestate: queue park command: %w", err)
		}
	}
	return nil
}

// ParkTake removes and returns the oldest command queued for node.
func (s *Store) ParkTake(ctx context.Context, node string) (cmd ParkCommand, ok bool, err error) {
	v, err := s.c.Do(ctx, s.c.B().Lpop().Key(parkCmdPrefix+node).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return cmd, false, nil
	}
	if err != nil {
		return cmd, false, fmt.Errorf("livestate: take park command: %w", err)
	}
	if err := json.Unmarshal([]byte(v), &cmd); err != nil {
		return cmd, false, fmt.Errorf("livestate: decode park command: %w", err)
	}
	return cmd, true, nil
}

// ParkPutReply posts the answer to command r.ID.
func (s *Store) ParkPutReply(ctx context.Context, r ParkReply) error {
	v, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.c.Do(ctx, s.c.B().Set().Key(parkReplyPrefix+r.ID).Value(string(v)).Px(ParkCommandTTL).Build()).Error()
}

// ParkGetReply returns the answer to command id; ok is false until the
// holder has posted it.
func (s *Store) ParkGetReply(ctx context.Context, id string) (r ParkReply, ok bool, err error) {
	v, err := s.c.Do(ctx, s.c.B().Get().Key(parkReplyPrefix+id).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("livestate: get park reply: %w", err)
	}
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return r, false, fmt.Errorf("livestate: decode park reply: %w", err)
	}
	return r, true, nil
}
