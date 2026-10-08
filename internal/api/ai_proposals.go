package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/azrtydxb/hello/internal/ai/proposal"
)

// ProposalService is what the proposal routes need; *proposal.DBStore
// implements it. A nil service answers 503 ai_disabled.
type ProposalService interface {
	Get(ctx context.Context, id string) (proposal.Proposal, error)
	List(ctx context.Context, status proposal.Status, source string, limit int) ([]proposal.Proposal, error)
	Current(ctx context.Context, header http.Header, p proposal.Proposal) []json.RawMessage
	Apply(ctx context.Context, id string, header http.Header) (proposal.Proposal, error)
	Dismiss(ctx context.Context, id string, userID int64, reason, text string) error
}

func (s *server) proposalsEnabled(w http.ResponseWriter) bool {
	if s.Proposals == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_disabled", "the AI agent is not enabled")
		return false
	}
	return true
}

// proposalJSON is the AIProposal schema; current is set only by get.
func proposalJSON(p proposal.Proposal, current []json.RawMessage) map[string]any {
	actions := make([]map[string]any, len(p.Actions))
	hasDelete := false
	for i, a := range p.Actions {
		hasDelete = hasDelete || a.Destructive
		m := map[string]any{
			"operationId": a.OperationID, "pathParams": nonNilParams(a.PathParams),
			"before": raw(a.Before), "after": raw(a.After),
			"destructive": a.Destructive, "references": nonNilRefs(a.References),
		}
		if len(a.Body) > 0 {
			m["body"] = a.Body
		}
		if current != nil {
			m["current"] = raw(current[i])
		}
		actions[i] = m
	}
	return map[string]any{
		"id": p.ID, "source": p.Source, "sessionId": p.SessionID, "findingId": p.FindingID,
		"title": p.Title, "rationale": p.Rationale, "status": p.Status, "configRevision": p.ConfigRevision,
		"actions": actions, "hasDelete": hasDelete, "createdBy": p.CreatedBy,
		"createdAt": p.CreatedAt, "updatedAt": p.UpdatedAt, "appliedBy": p.AppliedBy, "appliedAt": p.AppliedAt,
		"dismissedBy": p.DismissedBy, "dismissedAt": p.DismissedAt, "dismissReason": p.DismissReason,
		"dismissText": p.DismissText, "failure": p.Failure,
	}
}

func raw(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func nonNilParams(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nonNilRefs(r []proposal.Reference) []proposal.Reference {
	if r == nil {
		return []proposal.Reference{}
	}
	return r
}

func (s *server) proposalError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, proposal.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "proposal not found")
	case errors.Is(err, proposal.ErrNotOpen):
		writeError(w, http.StatusConflict, "proposal_not_open", "the proposal is not open")
	default:
		s.internal(w, what, err)
	}
}

func (s *server) listAIProposals(w http.ResponseWriter, r *http.Request) {
	if !s.proposalsEnabled(w) {
		return
	}
	q := r.URL.Query()
	status := proposal.Status(q.Get("status"))
	switch status {
	case "", proposal.StatusOpen, proposal.StatusApplied, proposal.StatusFailed, proposal.StatusStale,
		proposal.StatusDismissed, proposal.StatusSuperseded:
	default:
		badRequest(w, "status: unknown status")
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			badRequest(w, "limit: must be 1-200")
			return
		}
		limit = n
	}
	ps, err := s.Proposals.List(r.Context(), status, q.Get("source"), limit)
	if err != nil {
		s.internal(w, "list proposals", err)
		return
	}
	items := make([]map[string]any, len(ps))
	for i, p := range ps {
		items[i] = proposalJSON(p, nil)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *server) getAIProposal(w http.ResponseWriter, r *http.Request) {
	if !s.proposalsEnabled(w) {
		return
	}
	p, err := s.Proposals.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.proposalError(w, "get proposal", err)
		return
	}
	cur := s.Proposals.Current(r.Context(), r.Header, p)
	if cur == nil {
		cur = make([]json.RawMessage, len(p.Actions))
	}
	writeJSON(w, http.StatusOK, proposalJSON(p, cur))
}

func (s *server) applyAIProposal(w http.ResponseWriter, r *http.Request) {
	if !s.proposalsEnabled(w) {
		return
	}
	p, err := s.Proposals.Apply(r.Context(), r.PathValue("id"), r.Header)
	if err != nil {
		s.proposalError(w, "apply proposal", err)
		return
	}
	if p.Status == proposal.StatusStale {
		writeError(w, http.StatusConflict, "proposal_stale", "the configuration changed since the proposal was made; nothing was applied")
		return
	}
	writeJSON(w, http.StatusOK, proposalJSON(p, nil))
}

func (s *server) dismissAIProposal(w http.ResponseWriter, r *http.Request) {
	if !s.proposalsEnabled(w) {
		return
	}
	var in struct {
		Reason string `json:"reason"`
		Text   string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch in.Reason {
	case proposal.ReasonNotNeeded, proposal.ReasonWrong, proposal.ReasonLater, proposal.ReasonOther:
	default:
		badRequest(w, "reason: must be not_needed, wrong, later or other")
		return
	}
	if len([]rune(in.Text)) > proposal.MaxDismissText {
		badRequest(w, "text: at most 500 characters")
		return
	}
	if err := s.Proposals.Dismiss(r.Context(), r.PathValue("id"), actor(r).UserID, in.Reason, in.Text); err != nil {
		s.proposalError(w, "dismiss proposal", err)
		return
	}
	p, err := s.Proposals.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.proposalError(w, "get proposal", err)
		return
	}
	writeJSON(w, http.StatusOK, proposalJSON(p, nil))
}
