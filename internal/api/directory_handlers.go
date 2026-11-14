package api

// The directory engine's routes (spec integrations P8, I-1 to I-19):
// connectors, tests, sync runs, previews, applies and confirmations. Everything
// is administrator only; the bind password is write-only and needs the
// secrets scope to set.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/directory"
	"github.com/azrtydxb/hello/internal/routing"
)

// DirectoryService is the directory engine; *directory.Engine satisfies it.
// Nil answers 503 on the directory routes.
type DirectoryService interface {
	List(ctx context.Context) ([]directory.Connector, error)
	Get(ctx context.Context, id int64) (directory.Connector, error)
	Create(ctx context.Context, actor string, in directory.ConnectorInput) (directory.Connector, error)
	Update(ctx context.Context, actor string, id int64, in directory.ConnectorInput) (directory.Connector, error)
	Delete(ctx context.Context, actor string, id int64, release bool) error
	Test(ctx context.Context, actor string, id int64, m *directory.Mappings) (*directory.TestResult, error)
	RequestRun(ctx context.Context, actor string, id int64, dryRun bool) (directory.Run, error)
	ListRuns(ctx context.Context, connectorID int64, limit int) ([]directory.Run, error)
	GetRun(ctx context.Context, id int64) (directory.Run, error)
	ListChanges(ctx context.Context, runID int64, f directory.ChangeFilter) ([]directory.Change, error)
	Apply(ctx context.Context, actor string, runID int64, d directory.Decisions) (directory.Run, error)
	Confirm(ctx context.Context, actor string, runID int64, d directory.Decisions) (directory.Run, error)
}

func (s *server) directoryDisabled(w http.ResponseWriter) bool {
	if s.Directory == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the directory engine is not configured")
		return true
	}
	return false
}

// directoryError answers a failed directory operation.
func (s *server) directoryError(w http.ResponseWriter, what string, err error) {
	var (
		v  *directory.ValidationError
		de *directory.Error
	)
	switch {
	case errors.As(err, &v):
		fields := make([]routing.FieldError, len(v.Fields))
		for i, f := range v.Fields {
			fields[i] = routing.FieldError{Path: f.Path, Message: f.Message}
		}
		writeFields(w, fields)
	case errors.As(err, &de) && de.Code == directory.CodeTLSRequired:
		writeError(w, http.StatusBadRequest, "tls_required", de.Detail)
	case errors.Is(err, directory.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, directory.ErrDisabled):
		writeError(w, http.StatusConflict, "connector_disabled", "the connector is disabled")
	case errors.Is(err, directory.ErrHasUsers):
		writeError(w, http.StatusConflict, "connector_in_use", "users are still linked to this connector; delete with release=true to turn them into local users")
	case errors.Is(err, directory.ErrBusy):
		writeError(w, http.StatusConflict, "busy", "a run is in progress for this connector; try again when it ends")
	case errors.Is(err, directory.ErrNotApplicable):
		writeError(w, http.StatusConflict, "not_applicable", "the run is not in a state that allows this")
	default:
		s.internal(w, what, err)
	}
}

func (s *server) listDirectoryConnectors(w http.ResponseWriter, r *http.Request) {
	if s.directoryDisabled(w) {
		return
	}
	cs, err := s.Directory.List(r.Context())
	if err != nil {
		s.directoryError(w, "list directory connectors", err)
		return
	}
	writeJSON(w, http.StatusOK, items(cs))
}

func (s *server) getDirectoryConnector(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.directoryDisabled(w) {
		return
	}
	c, err := s.Directory.Get(r.Context(), id)
	if err != nil {
		s.directoryError(w, "directory connector", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// connectorBody is a create or a patch as sent.
type connectorBody struct {
	Kind            string                `json:"kind"`
	Name            *string               `json:"name"`
	Enabled         *bool                 `json:"enabled"`
	Mode            *string               `json:"mode"`
	ScheduleMinutes *int                  `json:"scheduleMinutes"`
	Config          json.RawMessage       `json:"config"`
	Mappings        *directory.Mappings   `json:"mappings"`
	GroupRules      *directory.GroupRules `json:"groupRules"`
	Options         *directory.Options    `json:"options"`
	AutoProvision   json.RawMessage       `json:"autoProvision"`
	BindPassword    *string               `json:"bindPassword"`
}

func (b connectorBody) input() directory.ConnectorInput {
	return directory.ConnectorInput{Kind: b.Kind, Name: b.Name, Enabled: b.Enabled, Mode: b.Mode, ScheduleMinutes: b.ScheduleMinutes,
		Config: b.Config, Mappings: b.Mappings, GroupRules: b.GroupRules, Options: b.Options, AutoProvision: b.AutoProvision, Secret: b.BindPassword}
}

// needsSecrets answers 403 when a body sets the credential without the
// secrets scope (spec: credential fields need secrets).
func needsSecrets(w http.ResponseWriter, r *http.Request, b connectorBody) bool {
	if b.BindPassword != nil && !actor(r).Scopes.Has(auth.ScopeSecrets) {
		writeError(w, http.StatusForbidden, "insufficient_scope", "setting the bind password needs the secrets scope")
		return true
	}
	return false
}

func (s *server) createDirectoryConnector(w http.ResponseWriter, r *http.Request) {
	if s.directoryDisabled(w) {
		return
	}
	var in connectorBody
	if !decode(w, r, &in) || needsSecrets(w, r, in) {
		return
	}
	if in.Name == nil {
		badRequest(w, "name is required")
		return
	}
	c, err := s.Directory.Create(r.Context(), actor(r).String(), in.input())
	if err != nil {
		s.directoryError(w, "create directory connector", err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) updateDirectoryConnector(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.directoryDisabled(w) {
		return
	}
	var in connectorBody
	if !decode(w, r, &in) || needsSecrets(w, r, in) {
		return
	}
	if in.Kind != "" {
		badRequest(w, "the kind of a connector cannot change")
		return
	}
	c, err := s.Directory.Update(r.Context(), actor(r).String(), id, in.input())
	if err != nil {
		s.directoryError(w, "update directory connector", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) deleteDirectoryConnector(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.directoryDisabled(w) {
		return
	}
	release := false
	if v := r.URL.Query().Get("release"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badRequest(w, "release must be true or false")
			return
		}
		release = b
	}
	if err := s.Directory.Delete(r.Context(), actor(r).String(), id, release); err != nil {
		s.directoryError(w, "delete directory connector", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) testDirectoryConnector(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.directoryDisabled(w) {
		return
	}
	var in struct {
		Mappings *directory.Mappings `json:"mappings"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	res, err := s.Directory.Test(r.Context(), actor(r).String(), id, in.Mappings)
	if err != nil {
		s.directoryError(w, "test directory connector", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) syncDirectoryConnector(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.directoryDisabled(w) {
		return
	}
	var in struct {
		DryRun *bool `json:"dryRun"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	// A run changes users: unless the caller says dryRun is false it is a
	// preview.
	dry := in.DryRun == nil || *in.DryRun
	run, err := s.Directory.RequestRun(r.Context(), actor(r).String(), id, dry)
	if err != nil {
		s.directoryError(w, "sync directory connector", err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *server) listDirectoryRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.directoryDisabled(w) {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			badRequest(w, "limit must be 1 to 200")
			return
		}
		limit = n
	}
	runs, err := s.Directory.ListRuns(r.Context(), id, limit)
	if err != nil {
		s.directoryError(w, "directory runs", err)
		return
	}
	writeJSON(w, http.StatusOK, items(runs))
}

func (s *server) getDirectoryRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.directoryDisabled(w) {
		return
	}
	run, err := s.Directory.GetRun(r.Context(), id)
	if err != nil {
		s.directoryError(w, "directory run", err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *server) listDirectoryChanges(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.directoryDisabled(w) {
		return
	}
	q := r.URL.Query()
	f := directory.ChangeFilter{Action: q.Get("action"), Limit: 200}
	switch f.Action {
	case "", directory.ActionCreate, directory.ActionUpdate, directory.ActionLink, directory.ActionAdopt, directory.ActionDisable,
		directory.ActionEnable, directory.ActionConflict, directory.ActionSkip, directory.ActionError:
	default:
		badRequest(w, "action is not a change action")
		return
	}
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			badRequest(w, "after must be a change id")
			return
		}
		f.After = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			badRequest(w, "limit must be 1 to 1000")
			return
		}
		f.Limit = n
	}
	cs, err := s.Directory.ListChanges(r.Context(), id, f)
	if err != nil {
		s.directoryError(w, "directory changes", err)
		return
	}
	var next any
	if len(cs) == f.Limit && len(cs) > 0 {
		next = cs[len(cs)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nonNil(cs), "nextAfter": next})
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// decisionsBody is what an apply or a confirm may carry.
type decisionsBody struct {
	Adopt    []string `json:"adopt"`
	AdoptAll bool     `json:"adoptAll"`
	Override []string `json:"override"`
}

func (s *server) applyDirectoryRun(w http.ResponseWriter, r *http.Request) {
	s.finishDirectoryRun(w, r, false)
}

func (s *server) confirmDirectoryRun(w http.ResponseWriter, r *http.Request) {
	s.finishDirectoryRun(w, r, true)
}

func (s *server) finishDirectoryRun(w http.ResponseWriter, r *http.Request, confirm bool) {
	id, ok := pathID(w, r)
	if !ok || s.directoryDisabled(w) {
		return
	}
	var in decisionsBody
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if len(in.Adopt) > 50000 || len(in.Override) > 50000 {
		badRequest(w, "too many decisions")
		return
	}
	d := directory.Decisions{Adopt: in.Adopt, AdoptAll: in.AdoptAll, Override: in.Override}
	var (
		run directory.Run
		err error
	)
	if confirm {
		run, err = s.Directory.Confirm(r.Context(), actor(r).String(), id, d)
	} else {
		run, err = s.Directory.Apply(r.Context(), actor(r).String(), id, d)
	}
	if errors.Is(err, directory.ErrStale) {
		// The source changed or the preview aged: the answer carries the
		// fresh preview to review.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]string{"code": "stale_preview", "message": "the preview is out of date; review the fresh one"},
			"run":   run,
		})
		return
	}
	if err != nil {
		s.directoryError(w, "apply directory run", err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
