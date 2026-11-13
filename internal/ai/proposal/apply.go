package proposal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/replay"
)

// ViaPrefix is the audit via of an applied action: "ai-proposal:<id>".
const ViaPrefix = "ai-proposal:"

// Current reads each action's target now with the caller's own Cookie and
// Authorization (nil for a create or a read that failed), for the diff's
// live column.
func (s *DBStore) Current(ctx context.Context, header http.Header, p Proposal) []json.RawMessage {
	return s.v.Current(ctx, header, p.Actions)
}

// ViaAI is the prefix of the via of a batch the assistant applied itself
// for the user (flow canvas decision of 2026-11-10): "ai:<session>". The
// batch row, the proposal's applied_via and its audit row carry it, with
// the user as the actor.
const ViaAI = "ai:"

// ErrNotBatch refuses an agent apply of a proposal that is not one batch.
var ErrNotBatch = errorString("the assistant applies only a proposal whose one action is a configuration batch")

// Apply replays an open proposal's actions in order with the applier's own
// Cookie and Authorization headers (spec S-13). The row stays locked for
// the whole apply, so a second applier gets ErrNotOpen. If the
// configuration revision moved and any target's current state differs from
// its before, the proposal becomes stale and nothing is sent. The first
// non-2xx stops: failed, with that action's detail and the ones applied
// before it; a 409 makes it stale. The returned proposal carries the new
// status; the error is for ErrNotFound, ErrNotOpen and infrastructure.
func (s *DBStore) Apply(ctx context.Context, id string, userHeader http.Header) (Proposal, error) {
	actor, ok := auth.ActorFrom(ctx)
	if !ok {
		return Proposal{}, errors.New("apply needs an authenticated user")
	}
	return s.apply(ctx, id, pick(userHeader), actor.UserID, ViaPrefix+id, "")
}

// ApplyAsAgent is the assistant applying its own batch proposal for the
// user it works for (flow canvas decision of 2026-11-10): the batch is
// replayed as ident with auth.Agent.Apply, so the user's own role decides
// (a viewer is refused 403) and the agent may write only through the batch
// route. The batch row, the proposal's applied_via and its audit row carry
// via ("ai:<session>") with the user as the actor, and the batch is
// reverted like any other. Only a proposal whose one action is BatchOp is
// applied (ErrNotBatch). Status and errors are Apply's.
func (s *DBStore) ApplyAsAgent(ctx context.Context, id string, ident Identity, via string) (Proposal, error) {
	if !strings.HasPrefix(via, ViaAI) || len(via) == len(ViaAI) {
		return Proposal{}, fmt.Errorf("an agent apply needs a via %s<session>", ViaAI)
	}
	if ident.UserID == 0 {
		return Proposal{}, errors.New("an agent apply needs the user it works for")
	}
	ident.Preview, ident.Apply = false, true
	// No actor in this context: the replayed batch authenticates as the
	// agent, never with a header.
	return s.apply(auth.WithAgent(ctx, ident), id, nil, ident.UserID, via, via)
}

// apply is Apply and ApplyAsAgent: replay as header (or as the agent in
// ctx when header is nil) with the changes' via, record the outcome for
// userID with appliedVia ("" for a person's own apply).
func (s *DBStore) apply(ctx context.Context, id string, header http.Header, userID int64, via, appliedVia string) (Proposal, error) {
	h := s.v.api.Load()
	if h == nil {
		return Proposal{}, errors.New("the API is not wired")
	}
	// The outcome is written even if the caller goes away mid-way: actions
	// already replayed are applied.
	tx, err := s.db.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return Proposal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := lock(ctx, tx, id)
	if err != nil {
		return p, err
	}
	if appliedVia != "" && (len(p.Actions) != 1 || p.Actions[0].OperationID != BatchOp) {
		return p, ErrNotBatch
	}
	var rev int64
	if err := tx.QueryRowContext(ctx, `SELECT config_revision FROM schema_info`).Scan(&rev); err != nil {
		return p, err
	}
	var (
		status  Status
		failure *Failure
		batchID string
	)
	if rev != p.ConfigRevision && s.changed(ctx, header, p) {
		status = StatusStale
	} else {
		status, failure, batchID = s.replayAll(auth.WithVia(ctx, via), *h, header, p)
	}
	var fj any
	if failure != nil {
		raw, _ := json.Marshal(failure)
		fj = raw
	}
	wc := context.WithoutCancel(ctx)
	if _, err := tx.ExecContext(wc, `UPDATE ai_proposals SET status = $2, failure = $3, updated_at = now(),
		applied_by = CASE WHEN $2 = 'applied' THEN $4::bigint END, applied_at = CASE WHEN $2 = 'applied' THEN now() END,
		applied_via = CASE WHEN $2 = 'applied' THEN NULLIF($5, '') END,
		batch_id = CASE WHEN $2 = 'applied' THEN NULLIF($6, '')::uuid END
		WHERE id = $1`, id, string(status), fj, userID, appliedVia, batchID); err != nil {
		return p, err
	}
	if err := audit(wc, tx, userID, "apply_"+string(status), id, appliedVia); err != nil {
		return p, err
	}
	if err := tx.Commit(); err != nil {
		return p, err
	}
	s.notify(p.Source, status)
	return s.Get(wc, id)
}

// changed reports whether any target's current state differs from its
// before (a failed or missing read counts as changed).
func (s *DBStore) changed(ctx context.Context, header http.Header, p Proposal) bool {
	cur := s.v.Current(ctx, header, p.Actions)
	for i, a := range p.Actions {
		if s.v.ops[a.OperationID].Method == http.MethodPost {
			continue
		}
		if cur[i] == nil || !Equal(cur[i], a.Before) {
			return true
		}
	}
	return false
}

// replayAll replays the actions in order; batchID is the configuration
// batch a BatchOp action ran as.
func (s *DBStore) replayAll(ctx context.Context, h http.Handler, header http.Header, p Proposal) (status Status, f *Failure, batchID string) {
	var applied []int
	for i, a := range p.Actions {
		op, ok := s.v.ops[a.OperationID]
		if !ok || !Allowed(a.OperationID) {
			return StatusFailed, &Failure{Index: i, Status: http.StatusBadRequest, Code: "not_allowed", Message: "the operation is not allowed", Applied: applied}, ""
		}
		res, err := replay.Do(ctx, h, replay.Request{
			Method: op.Method, Path: pathFor(op.Path, a.PathParams), Body: []byte(a.Body), Header: header,
		})
		if err != nil {
			return StatusFailed, &Failure{Index: i, Code: "replay", Message: err.Error(), Applied: applied}, ""
		}
		if res.Fail != "" {
			return StatusFailed, &Failure{Index: i, Status: res.Status, Code: res.Fail, Message: "the request did not complete", Applied: applied}, ""
		}
		if res.Status >= 200 && res.Status <= 299 {
			applied = append(applied, i)
			if a.OperationID == BatchOp {
				var out struct {
					BatchID string `json:"batchId"`
				}
				if json.Unmarshal(res.Body, &out) == nil && uuidRE.MatchString(out.BatchID) {
					batchID = out.BatchID
				}
			}
			continue
		}
		var e struct {
			Error struct{ Code, Message string }
		}
		_ = json.Unmarshal(res.Body, &e)
		f = &Failure{Index: i, Status: res.Status, Code: e.Error.Code, Message: e.Error.Message, Applied: applied}
		if res.Status == http.StatusConflict {
			return StatusStale, f, ""
		}
		return StatusFailed, f, ""
	}
	return StatusApplied, nil, batchID
}
