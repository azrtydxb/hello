package api

// The AIOps findings routes (spec ai-agent S-18, S-21): the detectors in
// internal/ai/detect raise them, the model explains them, an operator
// acknowledges or dismisses them here.

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/store"
)

// AIFindings is the findings persistence; *store.Store implements it. A nil
// Config.Findings is AI off: the routes answer 503 ai_disabled.
type AIFindings interface {
	ListAIFindings(ctx context.Context, f store.AIFindingFilter) ([]store.AIFinding, error)
	GetAIFinding(ctx context.Context, id string) (store.AIFinding, error)
	AIHealthScore(ctx context.Context) (int, error)
	AcknowledgeAIFinding(ctx context.Context, actor string, userID int64, id string) (store.AIFinding, error)
	DismissAIFinding(ctx context.Context, actor string, userID int64, id, reason string) (store.AIFinding, error)
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// findings answers 503 ai_disabled itself when AI is off.
func (s *server) findings(w http.ResponseWriter) (AIFindings, bool) {
	if s.Findings == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_disabled", "the AI agent is not enabled")
		return nil, false
	}
	return s.Findings, true
}

// findingID parses {id}; anything but a UUID is 404.
func findingID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !uuidRE.MatchString(id) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return "", false
	}
	return id, true
}

func (s *server) listAIFindings(w http.ResponseWriter, r *http.Request) {
	f, ok := s.findings(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	flt := store.AIFindingFilter{Status: q.Get("status"), Severity: q.Get("severity"), Type: q.Get("type"), Limit: 50}
	switch flt.Status {
	case "", store.FindingOpen, store.FindingAcknowledged, store.FindingDismissed, store.FindingResolved:
	default:
		badRequest(w, "status must be open, acknowledged, dismissed or resolved")
		return
	}
	switch flt.Severity {
	case "", store.SeverityInfo, store.SeverityWarning, store.SeverityCritical:
	default:
		badRequest(w, "severity must be info, warning or critical")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			badRequest(w, "limit must be 1-200")
			return
		}
		flt.Limit = n
	}
	items, err := f.ListAIFindings(r.Context(), flt)
	if err != nil {
		s.internal(w, "list ai findings", err)
		return
	}
	score, err := f.AIHealthScore(r.Context())
	if err != nil {
		s.internal(w, "ai health score", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "healthScore": score})
}

func (s *server) getAIFinding(w http.ResponseWriter, r *http.Request) {
	f, ok := s.findings(w)
	if !ok {
		return
	}
	id, ok := findingID(w, r)
	if !ok {
		return
	}
	got, err := f.GetAIFinding(r.Context(), id)
	if err != nil {
		s.storeError(w, "finding", err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *server) acknowledgeAIFinding(w http.ResponseWriter, r *http.Request) {
	f, ok := s.findings(w)
	if !ok {
		return
	}
	id, ok := findingID(w, r)
	if !ok {
		return
	}
	a := actor(r)
	got, err := f.AcknowledgeAIFinding(r.Context(), a.String(), a.UserID, id)
	if err != nil {
		s.findingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *server) dismissAIFinding(w http.ResponseWriter, r *http.Request) {
	f, ok := s.findings(w)
	if !ok {
		return
	}
	id, ok := findingID(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if n := utf8.RuneCountInString(in.Reason); n < 1 || n > 500 {
		badRequest(w, "reason must be 1-500 characters")
		return
	}
	a := actor(r)
	got, err := f.DismissAIFinding(r.Context(), a.String(), a.UserID, id, in.Reason)
	if err != nil {
		s.findingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *server) findingError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrFindingState) {
		writeError(w, http.StatusConflict, "conflict", "the finding is not in a state that allows this")
		return
	}
	s.storeError(w, "finding", err)
}
