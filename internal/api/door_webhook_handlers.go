package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// Doorbell webhooks (spec phone-feature-parity S-48). A non-SIP doorbell
// posts a signed request to the trigger route, which hello-control checks and
// rate-limits and which hello-sip then answers by ringing the Door group or
// chiming a paging group. The route is the one unauthenticated addition: it
// is open only to a request whose X-Hello-Signature is the HMAC-SHA256 of
// timestamp + "." + body under the webhook's secret.

// DoorTriggers carries a verified trigger to hello-sip and keeps the rate
// window and the duplicate window; *LazyValkey implements it.
type DoorTriggers interface {
	DoorHookRate(ctx context.Context, id int64, window time.Duration) (count int64, left time.Duration, err error)
	DoorHookClaim(ctx context.Context, id int64, window time.Duration) (bool, error)
	EnqueueDoorTrigger(ctx context.Context, t livestate.DoorTrigger) error
}

const (
	// doorSkew is how far the request timestamp may be from now.
	doorSkew = 300 * time.Second
	// doorBodyMax is the largest trigger body.
	doorBodyMax = 1024
	// doorRateWindow is the window the webhook's rate limit counts in.
	doorRateWindow = time.Minute
	// doorCoalesce is the window in which a second trigger of one webhook is
	// folded into the first: a button pressed twice rings once.
	doorCoalesce = 10 * time.Second
	// doorLabelMax is the longest label kept.
	doorLabelMax = 64
)

// doorWebhookBody is the create and replace body.
type doorWebhookBody struct {
	Name           *string `json:"name"`
	Action         *string `json:"action"`
	RingGroupID    *int64  `json:"ringGroupId"`
	PagingGroupID  *int64  `json:"pagingGroupId"`
	AnnouncementID *int64  `json:"announcementId"`
	RateLimit      *int    `json:"rateLimit"`
	Enabled        *bool   `json:"enabled"`
}

func (b doorWebhookBody) toInput() store.DoorWebhookInput {
	in := store.DoorWebhookInput{RingGroupID: b.RingGroupID, PagingGroupID: b.PagingGroupID,
		AnnouncementID: b.AnnouncementID, RateLimit: store.DefaultDoorRateLimit, Enabled: true}
	if b.Name != nil {
		in.Name = *b.Name
	}
	if b.Action != nil {
		in.Action = *b.Action
	}
	if b.RateLimit != nil {
		in.RateLimit = *b.RateLimit
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	return in
}

// doorWebhookCreated is the create and rotate response: the webhook and its
// secret, shown only here.
type doorWebhookCreated struct {
	store.DoorWebhook
	Secret string `json:"secret"`
}

func (s *server) listDoorWebhooks(w http.ResponseWriter, r *http.Request) {
	hs, err := s.Store.ListDoorWebhooks(r.Context())
	if err != nil {
		s.internal(w, "list door webhooks", err)
		return
	}
	writeJSON(w, http.StatusOK, items(hs))
}

func (s *server) getDoorWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	h, err := s.Store.GetDoorWebhook(r.Context(), id)
	if err != nil {
		s.storeError(w, "door webhook", err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *server) createDoorWebhook(w http.ResponseWriter, r *http.Request) {
	var b doorWebhookBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Action == nil {
		badRequest(w, "name and action are required")
		return
	}
	h, secret, err := s.Store.CreateDoorWebhook(r.Context(), actor(r).String(), b.toInput())
	if err != nil {
		s.configError(w, "door webhook", err)
		return
	}
	writeJSON(w, http.StatusCreated, doorWebhookCreated{DoorWebhook: h, Secret: secret})
}

func (s *server) putDoorWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b doorWebhookBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Action == nil {
		badRequest(w, "name and action are required")
		return
	}
	h, err := s.Store.UpdateDoorWebhook(r.Context(), actor(r).String(), id, b.toInput())
	if err != nil {
		s.configError(w, "door webhook", err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *server) deleteDoorWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteDoorWebhook(r.Context(), actor(r).String(), id); err != nil {
		s.storeError(w, "door webhook", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) rotateDoorWebhookSecret(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	secret, err := s.Store.RotateDoorWebhookSecret(r.Context(), actor(r).String(), id)
	if err != nil {
		s.storeError(w, "door webhook", err)
		return
	}
	h, err := s.Store.GetDoorWebhook(r.Context(), id)
	if err != nil {
		s.storeError(w, "door webhook", err)
		return
	}
	writeJSON(w, http.StatusOK, doorWebhookCreated{DoorWebhook: h, Secret: secret})
}

// doorSignature is hex HMAC-SHA256(secret, timestamp + "." + body).
func doorSignature(secret, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp))
	m.Write([]byte{'.'})
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// decoySecret stands in for the secret of a webhook that does not exist, so
// an unknown id costs the same work as a known one and answers the same 401.
var decoySecret = func() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}()

func unauthorizedTrigger(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid signature")
}

// doorLabel reads the optional JSON body of a trigger: a label of printable
// characters, cut to doorLabelMax runes. A body that is not a JSON object is
// ok=false.
func doorLabel(body []byte) (label string, ok bool) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return "", true
	}
	var v struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", false
	}
	var b strings.Builder
	n := 0
	for _, r := range v.Label {
		if n == doorLabelMax {
			break
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
			n++
		}
	}
	return b.String(), true
}

// triggerDoorWebhook is POST /api/v1/door/webhooks/{id}/trigger. It checks
// the signature first, so a missing or wrong one is 401 whether or not the id
// exists, then the rate limit (429 with Retry-After), then folds a duplicate
// within doorCoalesce into the ring in progress and otherwise queues the
// trigger for a SIP node. The body is audited by nobody.
func (s *server) triggerDoorWebhook(w http.ResponseWriter, r *http.Request) {
	ts, sig := r.Header.Get("X-Hello-Timestamp"), r.Header.Get("X-Hello-Signature")
	if ts == "" || sig == "" {
		unauthorizedTrigger(w)
		return
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(secs, 0)).Abs() > doorSkew {
		unauthorizedTrigger(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, doorBodyMax+1))
	if err != nil {
		badRequest(w, "could not read the request body")
		return
	}
	if len(body) > doorBodyMax {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the body is at most 1 KiB")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	hook, found, err := s.Store.DoorWebhookForTrigger(r.Context(), id)
	if err != nil {
		s.internal(w, "read door webhook", err)
		return
	}
	key := decoySecret
	if found {
		key = hook.Secret
	}
	want := doorSignature(key, ts, body)
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(sig))) || !found || !hook.Enabled {
		unauthorizedTrigger(w)
		return
	}
	// From here the caller holds the secret: the answers may be specific.
	label, ok := doorLabel(body)
	if !ok {
		badRequest(w, "the body, when sent, is a JSON object such as {\"label\":\"Gate\"}")
		return
	}
	if s.DoorTriggers == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the doorbell webhook needs Valkey and a running hello-sip node")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	n, left, err := s.DoorTriggers.DoorHookRate(ctx, hook.ID, doorRateWindow)
	if err != nil {
		s.liveDown(w, "count door trigger", err)
		return
	}
	if n > int64(hook.RateLimit) {
		secs := int((left + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many triggers; see Retry-After")
		return
	}
	who := "webhook:" + hook.Name
	first, err := s.DoorTriggers.DoorHookClaim(ctx, hook.ID, doorCoalesce)
	if err != nil {
		s.liveDown(w, "claim door trigger", err)
		return
	}
	if !first {
		s.auditDoorTrigger(r.Context(), who, hook.ID, "coalesce")
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "coalesced"})
		return
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.internal(w, "door trigger id", err)
		return
	}
	err = s.DoorTriggers.EnqueueDoorTrigger(ctx, livestate.DoorTrigger{
		ID: hex.EncodeToString(raw[:]), Webhook: hook.ID, Name: hook.Name, Action: hook.Action,
		RingGroup: hook.RingGroup, PagingCode: hook.PagingCode, Announcement: hook.Announcement,
		Label: label, At: time.Now(),
	})
	if err != nil {
		s.liveDown(w, "queue door trigger", err)
		return
	}
	s.auditDoorTrigger(r.Context(), who, hook.ID, "trigger")
	if err := s.Store.NoteDoorWebhookTriggered(r.Context(), hook.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.Log.Warn("stamp door webhook", "error", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// auditDoorTrigger records a trigger without its body or label.
func (s *server) auditDoorTrigger(ctx context.Context, who string, id int64, action string) {
	if err := s.Store.Audit(ctx, who, action, "door_webhook", strconv.FormatInt(id, 10)); err != nil {
		s.Log.Warn("audit door trigger", "error", err)
	}
}
