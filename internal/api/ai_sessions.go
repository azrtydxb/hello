package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/assistant/chat"
	"github.com/azrtydxb/hello/internal/auth"
)

// AIAssistant is the in-product assistant's sessions and messages (spec
// ai-agent S-8); *assistant.Assistant implements it. nil while AI is off:
// the routes answer 503 ai_disabled.
type AIAssistant interface {
	ListSessions(ctx context.Context, u chat.User) ([]chat.Session, error)
	CreateSession(ctx context.Context, u chat.User, title string) (chat.Session, error)
	GetSession(ctx context.Context, u chat.User, id string) (chat.Detail, error)
	RenameSession(ctx context.Context, u chat.User, id, title string) (chat.Session, error)
	DeleteSession(ctx context.Context, u chat.User, id string) error
	PostMessage(ctx context.Context, u chat.User, sessionID, content string) (messageID int64, taskID string, err error)
}

// assistantUser is the caller as the assistant sees them, or false after
// answering: AI off (503) or an actor that is not a user (403).
func (s *server) assistantUser(w http.ResponseWriter, r *http.Request) (chat.User, bool) {
	if s.Assistant == nil {
		writeError(w, http.StatusServiceUnavailable, ai.CodeDisabled, "AI is not enabled")
		return chat.User{}, false
	}
	a, _ := auth.ActorFrom(r.Context())
	if a.UserID == 0 {
		writeError(w, http.StatusForbidden, "forbidden_role", "the assistant works for users, not service accounts")
		return chat.User{}, false
	}
	return chat.User{ID: a.UserID, Admin: a.Role.AtLeast(auth.RoleAdmin)}, true
}

// assistantError answers an assistant failure.
func (s *server) assistantError(w http.ResponseWriter, what string, err error) {
	switch code := ai.CodeOf(err); {
	case errors.Is(err, chat.ErrInvalid):
		badRequest(w, err.Error())
	case errors.Is(err, chat.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, chat.ErrTaskRunning):
		writeError(w, http.StatusConflict, "task_running", "a message is still being answered in this session")
	case code == ai.CodeBusy || code == ai.CodeBudgetExhausted:
		writeError(w, http.StatusTooManyRequests, code, err.Error())
	case code == ai.CodeDisabled:
		writeError(w, http.StatusServiceUnavailable, code, "AI is not enabled")
	default:
		s.internal(w, what, err)
	}
}

func (s *server) listAISessions(w http.ResponseWriter, r *http.Request) {
	u, ok := s.assistantUser(w, r)
	if !ok {
		return
	}
	v, err := s.Assistant.ListSessions(r.Context(), u)
	if err != nil {
		s.assistantError(w, "list sessions", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

func (s *server) createAISession(w http.ResponseWriter, r *http.Request) {
	u, ok := s.assistantUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	// The body is optional: no title names the session after its first
	// message.
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	v, err := s.Assistant.CreateSession(r.Context(), u, in.Title)
	if err != nil {
		s.assistantError(w, "create session", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) getAISession(w http.ResponseWriter, r *http.Request) {
	u, ok := s.assistantUser(w, r)
	if !ok {
		return
	}
	v, err := s.Assistant.GetSession(r.Context(), u, r.PathValue("id"))
	if err != nil {
		s.assistantError(w, "session", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) updateAISession(w http.ResponseWriter, r *http.Request) {
	u, ok := s.assistantUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Title *string `json:"title"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Title == nil {
		badRequest(w, "title is required")
		return
	}
	v, err := s.Assistant.RenameSession(r.Context(), u, r.PathValue("id"), *in.Title)
	if err != nil {
		s.assistantError(w, "session", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteAISession(w http.ResponseWriter, r *http.Request) {
	u, ok := s.assistantUser(w, r)
	if !ok {
		return
	}
	if err := s.Assistant.DeleteSession(r.Context(), u, r.PathValue("id")); err != nil {
		s.assistantError(w, "session", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) postAIMessage(w http.ResponseWriter, r *http.Request) {
	u, ok := s.assistantUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if !decode(w, r, &in) {
		return
	}
	msgID, taskID, err := s.Assistant.PostMessage(r.Context(), u, r.PathValue("id"), in.Content)
	if err != nil {
		s.assistantError(w, "session", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"taskId": taskID, "messageId": msgID})
}
