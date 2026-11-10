package api

// Outbound webhooks (spec integrations I-42): endpoints, signing secrets, the
// delivery log and the dead-letter queue. Everything here is administrator
// only; creating an endpoint and rotating its secret also need the secrets
// scope, because they return the signing secret, once.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/webhook"
)

// WebhookService is the webhook logic; *webhook.Service satisfies it. Nil
// answers 503 on the webhook routes.
type WebhookService interface {
	ListEndpoints(ctx context.Context) ([]webhook.Endpoint, error)
	GetEndpoint(ctx context.Context, id int64) (webhook.Endpoint, error)
	Create(ctx context.Context, actor string, in webhook.EndpointInput) (webhook.Endpoint, string, bool, error)
	Update(ctx context.Context, actor string, id int64, p webhook.Patch) (webhook.Endpoint, error)
	DeleteEndpoint(ctx context.Context, actor string, id int64) error
	Rotate(ctx context.Context, actor string, id int64) (string, webhook.Endpoint, error)
	Test(ctx context.Context, id int64) (webhook.TestResult, error)
	ListDeliveries(ctx context.Context, q webhook.DeliveryQuery) ([]webhook.Delivery, error)
	GetDelivery(ctx context.Context, id int64) (webhook.DeliveryDetail, error)
	Redeliver(ctx context.Context, actor string, id int64) (webhook.Delivery, error)
	RedeliverDead(ctx context.Context, actor string, endpointID int64) (int64, error)
}

func (s *server) webhooksDisabled(w http.ResponseWriter) bool {
	if s.Webhooks == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "webhooks are not configured")
		return true
	}
	return false
}

// webhookError answers a failed webhook operation.
func (s *server) webhookError(w http.ResponseWriter, what string, err error) {
	var v *webhook.ValidationError
	switch {
	case errors.As(err, &v):
		fields := make([]routing.FieldError, len(v.Fields))
		for i, f := range v.Fields {
			fields[i] = routing.FieldError{Path: f.Path, Message: f.Message}
		}
		writeFields(w, fields)
	case errors.Is(err, webhook.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", what+": not found")
	case errors.Is(err, webhook.ErrInFlight):
		writeError(w, http.StatusConflict, "conflict", "the delivery is being sent; try again in a moment")
	default:
		s.internal(w, what, err)
	}
}

// webhookWithSecret is an endpoint with its signing secret, returned once.
type webhookWithSecret struct {
	webhook.Endpoint
	// Secret is the signing secret; null when the endpoint already existed.
	Secret *string `json:"secret"`
}

type webhookBody struct {
	Name         *string   `json:"name"`
	URL          *string   `json:"url"`
	Events       *[]string `json:"events"`
	Extensions   *[]string `json:"extensions"`
	Enabled      *bool     `json:"enabled"`
	AllowPrivate *bool     `json:"allowPrivate"`
}

func (s *server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	eps, err := s.Webhooks.ListEndpoints(r.Context())
	if err != nil {
		s.webhookError(w, "list webhooks", err)
		return
	}
	writeJSON(w, http.StatusOK, items(eps))
}

func (s *server) getWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	e, err := s.Webhooks.GetEndpoint(r.Context(), id)
	if err != nil {
		s.webhookError(w, "webhook", err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) createWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	var b webhookBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.URL == nil {
		badRequest(w, "name and url are required")
		return
	}
	in := webhook.EndpointInput{Name: *b.Name, URL: *b.URL, Enabled: b.Enabled == nil || *b.Enabled}
	if b.Events != nil {
		in.Events = *b.Events
	}
	if b.Extensions != nil {
		in.Extensions = *b.Extensions
	}
	if b.AllowPrivate != nil {
		in.AllowPrivate = *b.AllowPrivate
	}
	e, secret, created, err := s.Webhooks.Create(r.Context(), actor(r).String(), in)
	if err != nil {
		s.webhookError(w, "create webhook", err)
		return
	}
	if !created {
		// A repeated subscription (a Zapier REST hook): the same endpoint,
		// and no second look at its secret.
		writeJSON(w, http.StatusOK, webhookWithSecret{Endpoint: e})
		return
	}
	writeJSON(w, http.StatusCreated, webhookWithSecret{Endpoint: e, Secret: &secret})
}

func (s *server) updateWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b webhookBody
	if !decode(w, r, &b) {
		return
	}
	e, err := s.Webhooks.Update(r.Context(), actor(r).String(), id, webhook.Patch{
		Name: b.Name, URL: b.URL, Events: b.Events, Extensions: b.Extensions, Enabled: b.Enabled, AllowPrivate: b.AllowPrivate})
	if err != nil {
		s.webhookError(w, "update webhook", err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Webhooks.DeleteEndpoint(r.Context(), actor(r).String(), id); err != nil {
		s.webhookError(w, "delete webhook", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	secret, e, err := s.Webhooks.Rotate(r.Context(), actor(r).String(), id)
	if err != nil {
		s.webhookError(w, "rotate webhook secret", err)
		return
	}
	writeJSON(w, http.StatusOK, webhookWithSecret{Endpoint: e, Secret: &secret})
}

func (s *server) testWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	res, err := s.Webhooks.Test(r.Context(), id)
	if err != nil {
		s.webhookError(w, "test webhook", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	dq := webhook.DeliveryQuery{EndpointID: id, Limit: 100}
	switch st := q.Get("status"); st {
	case "":
	case webhook.StatusPending, webhook.StatusRetrying, webhook.StatusSucceeded, webhook.StatusDead:
		dq.Status = st
	default:
		badRequest(w, "status must be pending, retrying, succeeded or dead")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			badRequest(w, "limit must be between 1 and 500")
			return
		}
		dq.Limit = n
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			badRequest(w, "before must be a delivery id")
			return
		}
		dq.Before = n
	}
	if _, err := s.Webhooks.GetEndpoint(r.Context(), id); err != nil {
		s.webhookError(w, "webhook", err)
		return
	}
	ds, err := s.Webhooks.ListDeliveries(r.Context(), dq)
	if err != nil {
		s.webhookError(w, "list webhook deliveries", err)
		return
	}
	next := ""
	if len(ds) == dq.Limit {
		next = strconv.FormatInt(ds[len(ds)-1].ID, 10)
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []webhook.Delivery `json:"items"`
		NextCursor string             `json:"nextCursor"`
	}{items(ds).Items, next})
}

func (s *server) getWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	did, err := strconv.ParseInt(r.PathValue("deliveryId"), 10, 64)
	if err != nil || did <= 0 {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	d, err := s.Webhooks.GetDelivery(r.Context(), did)
	if err != nil || d.EndpointID != id {
		if err == nil {
			err = webhook.ErrNotFound
		}
		s.webhookError(w, "webhook delivery", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		webhook.Delivery
		Log   []webhook.Attempt `json:"log"`
		Event json.RawMessage   `json:"event"`
	}{d.Delivery, d.Log, d.Event})
}

func (s *server) redeliverWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.Webhooks.Redeliver(r.Context(), actor(r).String(), id)
	if err != nil {
		s.webhookError(w, "webhook delivery", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) redeliverDeadWebhooks(w http.ResponseWriter, r *http.Request) {
	if s.webhooksDisabled(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	n, err := s.Webhooks.RedeliverDead(r.Context(), actor(r).String(), id)
	if err != nil {
		s.webhookError(w, "webhook", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Redelivered int64 `json:"redelivered"`
	}{n})
}
