package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/slack"
)

// Slack notifications (spec integrations P17): missed calls and voicemail
// messages posted to a Slack channel. The incoming webhook URL and the bot
// token are write-only: they are sealed, never returned, logged or sent over
// MCP.

// SlackService is the Slack administration; *slack.Service satisfies it. Nil
// answers 503 on the Slack routes.
type SlackService interface {
	List(ctx context.Context) ([]slack.Destination, error)
	Get(ctx context.Context, id int64) (slack.Destination, error)
	Create(ctx context.Context, actor string, in slack.Input) (slack.Destination, error)
	Update(ctx context.Context, actor string, id int64, p slack.Patch) (slack.Destination, error)
	Delete(ctx context.Context, actor string, id int64) error
	Test(ctx context.Context, id int64) (slack.TestResult, error)
	Posts(ctx context.Context, q slack.PostQuery) ([]slack.Post, error)
	Retry(ctx context.Context, actor string, id int64) (slack.Post, error)
}

func (s *server) slackReady(w http.ResponseWriter) bool {
	if s.Slack == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Slack notifications are not configured")
		return false
	}
	return true
}

func (s *server) slackError(w http.ResponseWriter, what string, err error) {
	var ve *slack.ValidationError
	switch {
	case errors.As(err, &ve):
		var f fieldErrs
		for _, x := range ve.Fields {
			f = append(f, routing.FieldError{Path: x.Path, Message: x.Message})
		}
		writeFields(w, f)
	case errors.Is(err, slack.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, slack.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", what+": the name is taken, or the post is not a dead letter")
	case errors.Is(err, slack.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "at most 6 tests per minute")
	default:
		s.internal(w, what, err)
	}
}

type slackBody struct {
	Name       *string   `json:"name"`
	Mode       *string   `json:"mode"`
	Channel    *string   `json:"channel"`
	Events     *[]string `json:"events"`
	Extensions *[]string `json:"extensions"`
	Enabled    *bool     `json:"enabled"`
	Secret     *string   `json:"secret"`
}

func (s *server) listSlack(w http.ResponseWriter, r *http.Request) {
	if !s.slackReady(w) {
		return
	}
	ds, err := s.Slack.List(r.Context())
	if err != nil {
		s.slackError(w, "list Slack destinations", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ds))
}

func (s *server) createSlack(w http.ResponseWriter, r *http.Request) {
	if !s.slackReady(w) {
		return
	}
	var b slackBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Mode == nil || b.Secret == nil {
		badRequest(w, "name, mode and secret are required")
		return
	}
	in := slack.Input{Name: *b.Name, Mode: *b.Mode, Secret: *b.Secret, Enabled: true}
	if b.Channel != nil {
		in.Channel = *b.Channel
	}
	if b.Events != nil {
		in.Events = *b.Events
	}
	if b.Extensions != nil {
		in.Extensions = *b.Extensions
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	d, err := s.Slack.Create(r.Context(), actor(r).String(), in)
	if err != nil {
		s.slackError(w, "Slack destination", err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *server) getSlack(w http.ResponseWriter, r *http.Request) {
	if !s.slackReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.Slack.Get(r.Context(), id)
	if err != nil {
		s.slackError(w, "Slack destination", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) updateSlack(w http.ResponseWriter, r *http.Request) {
	if !s.slackReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b slackBody
	if !decode(w, r, &b) {
		return
	}
	if b.Mode != nil {
		cur, err := s.Slack.Get(r.Context(), id)
		if err != nil {
			s.slackError(w, "Slack destination", err)
			return
		}
		if *b.Mode != cur.Mode {
			writeFields(w, fieldErrs{{Path: "mode", Message: "cannot change; create a new destination"}})
			return
		}
	}
	d, err := s.Slack.Update(r.Context(), actor(r).String(), id, slack.Patch{Name: b.Name, Channel: b.Channel, Events: b.Events,
		Extensions: b.Extensions, Enabled: b.Enabled, Secret: b.Secret})
	if err != nil {
		s.slackError(w, "Slack destination", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) deleteSlack(w http.ResponseWriter, r *http.Request) {
	if !s.slackReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Slack.Delete(r.Context(), actor(r).String(), id); err != nil {
		s.slackError(w, "Slack destination", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) testSlack(w http.ResponseWriter, r *http.Request) {
	if !s.slackReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	res, err := s.Slack.Test(r.Context(), id)
	if err != nil {
		s.slackError(w, "Slack destination", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type slackPostPage struct {
	Items      []slack.Post `json:"items"`
	NextBefore int64        `json:"nextBefore"`
}

var slackPostStatuses = []string{slack.StatusPending, slack.StatusRetrying, slack.StatusSucceeded, slack.StatusDead}

func (s *server) listSlackPosts(w http.ResponseWriter, r *http.Request) {
	if !s.slackReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.Slack.Get(r.Context(), id); err != nil {
		s.slackError(w, "Slack destination", err)
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && !slices.Contains(slackPostStatuses, status) {
		badRequest(w, "status must be one of "+strings.Join(slackPostStatuses, ", "))
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			badRequest(w, "limit must be 1 to 200")
			return
		}
		limit = n
	}
	var before int64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			badRequest(w, "before must be a post id")
			return
		}
		before = n
	}
	rows, err := s.Slack.Posts(r.Context(), slack.PostQuery{DestinationID: id, Status: status, Before: before, Limit: limit})
	if err != nil {
		s.slackError(w, "Slack posts", err)
		return
	}
	page := slackPostPage{Items: rows}
	if len(rows) == limit {
		page.NextBefore = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) retrySlackPost(w http.ResponseWriter, r *http.Request) {
	if !s.slackReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := s.Slack.Retry(r.Context(), actor(r).String(), id)
	if err != nil {
		s.slackError(w, "Slack post", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
