package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Click-to-call (spec integrations I-37) is a request from hello-control to a
// hello-sip node, which alone can place the calls: control validates, applies
// the limits and queues the request here; any READY SIP node takes it, rings
// the extension, dials the destination once the extension answered and posts
// every state change, which control reads for GET /calls/originate/{id}.

const (
	originateQueue  = "hello:originate:queue"
	originateState  = "hello:originate:state:"
	originateCancel = "hello:originate:cancel:"
	originateIdem   = "hello:originate:idem:"
	// OriginateQueueTTL bounds how long a request waits for a node: a
	// request nobody took in this time is closed node_unavailable.
	OriginateQueueTTL = 60 * time.Second
	// OriginateStateTTL is how long a request's state is kept after its last
	// change; control copies a terminal state into PostgreSQL long before.
	OriginateStateTTL = time.Hour
	// OriginateIdemTTL is how long an idempotency key maps to its request.
	OriginateIdemTTL = 10 * time.Minute
)

// Originate states (spec I-37).
const (
	OriginateQueued        = "queued"
	OriginateRingingAgent  = "ringing_agent"
	OriginateAgentAnswered = "agent_answered"
	OriginateDialingTarget = "dialing_target"
	OriginateConnected     = "connected"
	OriginateEnded         = "ended"
	OriginateFailed        = "failed"
)

// Originate failure reasons (spec I-37).
const (
	OriginateNoRegistration = "no_registration"
	OriginateAgentNoAnswer  = "agent_no_answer"
	OriginateAgentBusy      = "agent_busy"
	OriginateAgentDND       = "agent_dnd"
	OriginateTargetBusy     = "target_busy"
	OriginateTargetNoAnswer = "target_no_answer"
	OriginateRouteDenied    = "route_denied"
	OriginateRateLimited    = "rate_limited"
	OriginateNodeGone       = "node_unavailable"
	// OriginateCancelled is a request the client cancelled while it rang.
	OriginateCancelled = "cancelled"
)

// OriginateTerminal reports whether state is final.
func OriginateTerminal(state string) bool {
	return state == OriginateEnded || state == OriginateFailed
}

// OriginateRequest asks a SIP node to place one click-to-call.
type OriginateRequest struct {
	ID string `json:"id"`
	// Extension is the extension that rings first (leg A); DeviceID, when
	// set, limits leg A to that device's contacts.
	Extension string `json:"extension"`
	DeviceID  string `json:"deviceId,omitempty"`
	// Destination is what leg B dials, as the extension would at its desk
	// phone: digits, +, * and #.
	Destination string `json:"destination"`
	CallerID    string `json:"callerId,omitempty"`
	AutoAnswer  bool   `json:"autoAnswer,omitempty"`
	// Client is the OAuth client or token id the request came with; Actor
	// the audit actor.
	Client string `json:"client,omitempty"`
	Actor  string `json:"actor,omitempty"`
	// Restrict limits an outbound destination to numbers that start with one
	// of Prefixes (the administrator's click-to-call policy).
	Restrict bool     `json:"restrict,omitempty"`
	Prefixes []string `json:"prefixes,omitempty"`
	// RingSeconds is how long leg A may ring (0 means the node default).
	RingSeconds int       `json:"ringSeconds,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// OriginateState is a request's progress.
type OriginateState struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// Reason explains a failed (or cancelled) request.
	Reason string `json:"reason,omitempty"`
	// CorrelationID is the CDR correlation id of the bridged call, once the
	// target is being dialled.
	CorrelationID string    `json:"correlationId,omitempty"`
	Node          string    `json:"node,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// EnqueueOriginate queues a click-to-call request for a SIP node.
func (s *Store) EnqueueOriginate(ctx context.Context, r OriginateRequest) error {
	v, err := json.Marshal(r)
	if err != nil {
		return err
	}
	for _, res := range s.c.DoMulti(ctx,
		s.c.B().Rpush().Key(originateQueue).Element(string(v)).Build(),
		s.c.B().Expire().Key(originateQueue).Seconds(int64(OriginateQueueTTL/time.Second)).Build(),
	) {
		if err := res.Error(); err != nil {
			return fmt.Errorf("livestate: queue originate: %w", err)
		}
	}
	return nil
}

// TakeOriginate removes and returns the oldest queued request; ok is false
// when there is none.
func (s *Store) TakeOriginate(ctx context.Context) (r OriginateRequest, ok bool, err error) {
	v, err := s.c.Do(ctx, s.c.B().Lpop().Key(originateQueue).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("livestate: take originate: %w", err)
	}
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return r, false, fmt.Errorf("livestate: decode originate: %w", err)
	}
	return r, true, nil
}

// PutOriginateState publishes a request's state.
func (s *Store) PutOriginateState(ctx context.Context, st OriginateState) error {
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = time.Now().UTC()
	}
	v, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.c.Do(ctx, s.c.B().Set().Key(originateState+st.ID).Value(string(v)).Px(OriginateStateTTL).Build()).Error()
}

// GetOriginateState returns a request's state; ok is false when none is
// kept.
func (s *Store) GetOriginateState(ctx context.Context, id string) (st OriginateState, ok bool, err error) {
	v, err := s.c.Do(ctx, s.c.B().Get().Key(originateState+id).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return st, false, nil
	}
	if err != nil {
		return st, false, fmt.Errorf("livestate: get originate state: %w", err)
	}
	if err := json.Unmarshal([]byte(v), &st); err != nil {
		return st, false, fmt.Errorf("livestate: decode originate state: %w", err)
	}
	return st, true, nil
}

// CancelOriginate asks the node running request id to hang up its calls.
func (s *Store) CancelOriginate(ctx context.Context, id string) error {
	return s.c.Do(ctx, s.c.B().Set().Key(originateCancel+id).Value("1").Px(5*time.Minute).Build()).Error()
}

// OriginateCancelled reports whether CancelOriginate was called for id.
func (s *Store) OriginateCancelled(ctx context.Context, id string) (bool, error) {
	n, err := s.c.Do(ctx, s.c.B().Exists().Key(originateCancel+id).Build()).AsInt64()
	if err != nil {
		return false, fmt.Errorf("livestate: originate cancel flag: %w", err)
	}
	return n > 0, nil
}

// Idempotent maps an idempotency key to a request id for OriginateIdemTTL:
// the first caller stores id and gets it back with created true; a later
// caller gets the stored id and created false.
func (s *Store) Idempotent(ctx context.Context, key, id string) (stored string, created bool, err error) {
	k := originateIdem + key
	err = s.c.Do(ctx, s.c.B().Set().Key(k).Value(id).Nx().Px(OriginateIdemTTL).Build()).Error()
	if err == nil {
		return id, true, nil
	}
	if !valkey.IsValkeyNil(err) {
		return "", false, fmt.Errorf("livestate: idempotency key: %w", err)
	}
	v, err := s.c.Do(ctx, s.c.B().Get().Key(k).Build()).ToString()
	if err != nil {
		return "", false, fmt.Errorf("livestate: idempotency key: %w", err)
	}
	return v, false, nil
}

// LookupIdempotent returns the request id an idempotency key maps to; ok is
// false when the key is unknown or expired.
func (s *Store) LookupIdempotent(ctx context.Context, key string) (id string, ok bool, err error) {
	v, err := s.c.Do(ctx, s.c.B().Get().Key(originateIdem+key).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("livestate: idempotency key: %w", err)
	}
	return v, true, nil
}

// Incr adds one to the counter key and returns its new value; a counter
// that did not exist expires after ttl.
func (s *Store) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	res := s.c.DoMulti(ctx,
		s.c.B().Incr().Key(key).Build(),
		s.c.B().Expire().Key(key).Seconds(int64(ttl/time.Second)).Nx().Build(),
	)
	n, err := res[0].AsInt64()
	if err != nil {
		return 0, fmt.Errorf("livestate: incr: %w", err)
	}
	return n, nil
}

const originateSetup = "hello:originate:setup:"

// releaseSetup deletes the slot only when id still holds it.
var releaseSetup = valkey.NewLuaScript(`if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('del', KEYS[1]) end return 0`)

// AcquireOriginateSetup takes the extension's one click-to-call setup slot for
// request id, for at most ttl; it reports false while another request holds
// it.
func (s *Store) AcquireOriginateSetup(ctx context.Context, extension, id string, ttl time.Duration) (bool, error) {
	err := s.c.Do(ctx, s.c.B().Set().Key(originateSetup+extension).Value(id).Nx().Px(ttl).Build()).Error()
	switch {
	case err == nil:
		return true, nil
	case valkey.IsValkeyNil(err):
		return false, nil
	}
	return false, fmt.Errorf("livestate: originate setup slot: %w", err)
}

// ReleaseOriginateSetup frees the extension's setup slot when request id
// holds it.
func (s *Store) ReleaseOriginateSetup(ctx context.Context, extension, id string) error {
	if err := releaseSetup.Exec(ctx, s.c, []string{originateSetup + extension}, []string{id}).Error(); err != nil {
		return fmt.Errorf("livestate: release originate setup slot: %w", err)
	}
	return nil
}
