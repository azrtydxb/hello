package api

// SIP MESSAGE text messaging (RFC 3428): sending to an extension, a ring or
// paging group, every phone or an external number, the history and its
// settings. hello-control only stores the message and notifies the SIP
// nodes; one node delivers it (or holds it until the recipient registers).
//
// Message bodies are private: history is admin only, sending needs the
// operator role, and the /me/messages routes act on the caller's own
// extension only (the owner is the user's linked extension, no route takes
// an extension). Bodies are never logged.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// MessageStore is the message persistence the API needs; *store.Store
// implements it.
type MessageStore interface {
	SendMessage(ctx context.Context, in store.MessageInput) (store.MessageSent, error)
	ListMessages(ctx context.Context, f store.MessageFilter, before int64, limit int) ([]store.Message, error)
	ListMessageConversations(ctx context.Context, f store.MessageFilter, limit int) ([]store.MessageConversation, error)
	GetMessageSettings(ctx context.Context) (store.MessageSettings, error)
	PutMessageSettings(ctx context.Context, actor string, m store.MessageSettings) (store.MessageSettings, error)
	PurgeMessages(ctx context.Context, actor string, olderThanDays int) (int64, error)
	UserExtensionNumber(ctx context.Context, userID int64) (string, error)
	MessageExtensionName(ctx context.Context, number string) string
}

var _ MessageStore = (*store.Store)(nil)

// messageBody is the send request.
type messageBody struct {
	// TargetKind is extension, ring_group, paging_group, broadcast or external.
	TargetKind string `json:"targetKind"`
	// Target names the extension, group or number; empty for broadcast.
	Target string `json:"target"`
	Body   string `json:"body"`
	// Confirm must be true for a broadcast to every phone.
	Confirm bool `json:"confirm"`
}

// messageSettingsBody is the settings replace request.
type messageSettingsBody struct {
	Enabled               *bool `json:"enabled"`
	TTLHours              *int  `json:"ttlHours"`
	RetentionDays         *int  `json:"retentionDays"`
	RatePerMinute         *int  `json:"ratePerMinute"`
	MaxQueuedPerExtension *int  `json:"maxQueuedPerExtension"`
}

func (s *server) messageStore(w http.ResponseWriter) (MessageStore, bool) {
	if s.Messages == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "messaging is not configured")
		return nil, false
	}
	return s.Messages, true
}

// validate checks a send request (stage 1).
func (b messageBody) validate(allowBroadcast bool) fieldErrs {
	var f fieldErrs
	switch b.TargetKind {
	case store.MsgKindExtension:
		if b.Target == "" || len(b.Target) > 10 {
			f.add("target", "must be an extension number")
		}
	case store.MsgScopeRingGroup, store.MsgScopePagingGroup:
		if b.Target == "" || !printable(b.Target, 64, true) {
			f.add("target", "must be the name of a group")
		}
	case store.MsgKindExternal:
		if !store.ValidExternalNumber(b.Target) {
			f.add("target", "must be a phone number of 2-32 digits, optionally starting with +")
		}
	case store.MsgScopeBroadcast:
		if !allowBroadcast {
			f.add("targetKind", "cannot be broadcast here")
		} else if b.Target != "" {
			f.add("target", "must be empty for a broadcast")
		} else if !b.Confirm {
			f.add("confirm", "must be true to send to every phone")
		}
	default:
		f.add("targetKind", "must be extension, ring_group, paging_group, broadcast or external")
	}
	switch n := len(b.Body); {
	case strings.TrimSpace(b.Body) == "":
		f.add("body", "must not be empty")
	case n > store.MaxMessageBytes:
		f.add("body", "must be at most %d bytes", store.MaxMessageBytes)
	case !utf8.ValidString(b.Body) || strings.ContainsRune(b.Body, 0):
		f.add("body", "must be valid text")
	}
	return f
}

// sendError answers a failed send.
func (s *server) messageSendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrMessageTarget):
		writeError(w, http.StatusNotFound, "not_found", "no such extension or group, or it has no members")
	case errors.Is(err, store.ErrMessageForbidden):
		writeError(w, http.StatusForbidden, "forbidden_role", "you may not message this group")
	case errors.Is(err, store.ErrMessageRate):
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many messages; try again shortly")
	case errors.Is(err, store.ErrMessagesDisabled):
		writeError(w, http.StatusConflict, "messaging_disabled", "messaging is turned off")
	case errors.Is(err, store.ErrNoExternalTrunk):
		writeError(w, http.StatusConflict, "no_messaging_trunk", "no enabled trunk has messaging turned on")
	case errors.Is(err, store.ErrMessageTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the message is too large")
	default:
		s.internal(w, "send message", err)
	}
}

func sentJSON(sent store.MessageSent) map[string]any {
	return map[string]any{"batch": sent.Batch, "scope": sent.Scope, "recipients": sent.Recipients}
}

// sendMessage is the administrators' and operators' send.
func (s *server) sendMessage(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.messageStore(w)
	if !ok {
		return
	}
	var b messageBody
	if !decode(w, r, &b) {
		return
	}
	if f := b.validate(true); len(f) > 0 {
		writeFields(w, f)
		return
	}
	a := actor(r).String()
	sent, err := ms.SendMessage(r.Context(), store.MessageInput{
		Sender: store.MsgSenderSystem, SenderKind: store.MsgSenderSystem, SenderName: "Admin", Actor: a,
		TargetKind: b.TargetKind, Target: b.Target, ContentType: "text/plain", Body: []byte(b.Body), Audit: true,
	})
	if err != nil {
		s.messageSendError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, sentJSON(sent))
}

// messageQuery reads the listing filter and paging.
func messageQuery(w http.ResponseWriter, r *http.Request, defaultLimit int) (f store.MessageFilter, before int64, limit int, ok bool) {
	q := r.URL.Query()
	f.Extension, f.Conversation, f.Query = q.Get("extension"), q.Get("conversation"), q.Get("q")
	f.Status = q.Get("status")
	switch {
	case len(f.Extension) > 32 || (f.Extension != "" && !printable(f.Extension, 32, false)):
		badRequest(w, "extension must be at most 32 printable characters without spaces")
		return
	case len(f.Conversation) > 200 || len(f.Query) > 200:
		badRequest(w, "conversation and q must be at most 200 characters")
		return
	case f.Status != "" && f.Status != store.MsgPending && f.Status != store.MsgDelivered && f.Status != store.MsgFailed:
		badRequest(w, "status must be pending, delivered or failed")
		return
	}
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "cursor must be a value from a previous page")
			return
		}
		before = n
	}
	limit = defaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxCDRLimit {
			badRequest(w, "limit must be 1-200")
			return
		}
		limit = n
	}
	return f, before, limit, true
}

func (s *server) listMessagesCommon(w http.ResponseWriter, r *http.Request, ms MessageStore, own string) {
	f, before, limit, ok := messageQuery(w, r, defaultCDRLimit)
	if !ok {
		return
	}
	if own != "" {
		f.Extension = own
	}
	// Status "pending" also matches rows being sent right now.
	items, err := ms.ListMessages(r.Context(), f, before, limit)
	if err != nil {
		s.internal(w, "list messages", err)
		return
	}
	next := ""
	if len(items) == limit {
		next = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next": next})
}

func (s *server) listConversationsCommon(w http.ResponseWriter, r *http.Request, ms MessageStore, own string) {
	f, _, limit, ok := messageQuery(w, r, defaultCDRLimit)
	if !ok {
		return
	}
	if own != "" {
		f.Extension = own
	}
	convs, err := ms.ListMessageConversations(r.Context(), f, limit)
	if err != nil {
		s.internal(w, "list message conversations", err)
		return
	}
	writeJSON(w, http.StatusOK, items(convs))
}

func (s *server) listMessages(w http.ResponseWriter, r *http.Request) {
	if ms, ok := s.messageStore(w); ok {
		s.listMessagesCommon(w, r, ms, "")
	}
}

func (s *server) listMessageConversations(w http.ResponseWriter, r *http.Request) {
	if ms, ok := s.messageStore(w); ok {
		s.listConversationsCommon(w, r, ms, "")
	}
}

func (s *server) getMessageSettings(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.messageStore(w)
	if !ok {
		return
	}
	m, err := ms.GetMessageSettings(r.Context())
	if err != nil {
		s.internal(w, "get message settings", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *server) putMessageSettings(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.messageStore(w)
	if !ok {
		return
	}
	var b messageSettingsBody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	rng := func(path string, v *int, lo, hi int) int {
		if v == nil {
			f.add(path, "is required")
			return 0
		}
		if *v < lo || *v > hi {
			f.add(path, "must be %d-%d", lo, hi)
		}
		return *v
	}
	m := store.MessageSettings{
		TTLHours:              rng("ttlHours", b.TTLHours, 1, 720),
		RetentionDays:         rng("retentionDays", b.RetentionDays, 1, 3650),
		RatePerMinute:         rng("ratePerMinute", b.RatePerMinute, 1, 1000),
		MaxQueuedPerExtension: rng("maxQueuedPerExtension", b.MaxQueuedPerExtension, 1, 10000),
	}
	if b.Enabled == nil {
		f.add("enabled", "is required")
	} else {
		m.Enabled = *b.Enabled
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	out, err := ms.PutMessageSettings(r.Context(), actor(r).String(), m)
	if err != nil {
		s.internal(w, "put message settings", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) purgeMessages(w http.ResponseWriter, r *http.Request) {
	ms, ok := s.messageStore(w)
	if !ok {
		return
	}
	var b struct {
		OlderThanDays *int `json:"olderThanDays"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.OlderThanDays == nil || *b.OlderThanDays < 0 || *b.OlderThanDays > 3650 {
		writeFields(w, fieldErrs{{Path: "olderThanDays", Message: "must be 0-3650 (0 deletes every finished message)"}})
		return
	}
	n, err := ms.PurgeMessages(r.Context(), actor(r).String(), *b.OlderThanDays)
	if err != nil {
		s.internal(w, "purge messages", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// My messages: the caller's own extension only.

// messageOwner returns the extension the caller's user is linked to. A
// credential that is not a user, or a user with no linked extension, gets
// 403 here, so nothing below can be reached without an owner.
func (s *server) messageOwner(w http.ResponseWriter, r *http.Request) (MessageStore, string, bool) {
	ms, ok := s.messageStore(w)
	if !ok {
		return nil, "", false
	}
	a := actor(r)
	if a.Kind == auth.KindService || a.UserID <= 0 {
		writeError(w, http.StatusForbidden, "no_user", "your messages belong to a user; this credential has none")
		return nil, "", false
	}
	ext, err := ms.UserExtensionNumber(r.Context(), a.UserID)
	switch {
	case errors.Is(err, store.ErrNoExtensionLink):
		writeError(w, http.StatusForbidden, "no_extension", "your account is not linked to an extension, so you have no messages; ask an administrator to link it")
		return nil, "", false
	case err != nil:
		s.internal(w, "message owner", err)
		return nil, "", false
	}
	return ms, ext, true
}

func (s *server) listMyMessages(w http.ResponseWriter, r *http.Request) {
	if ms, ext, ok := s.messageOwner(w, r); ok {
		s.listMessagesCommon(w, r, ms, ext)
	}
}

func (s *server) listMyMessageConversations(w http.ResponseWriter, r *http.Request) {
	if ms, ext, ok := s.messageOwner(w, r); ok {
		s.listConversationsCommon(w, r, ms, ext)
	}
}

// sendMyMessage sends from the caller's own extension; the sender is never
// taken from the request.
func (s *server) sendMyMessage(w http.ResponseWriter, r *http.Request) {
	ms, ext, ok := s.messageOwner(w, r)
	if !ok {
		return
	}
	var b messageBody
	if !decode(w, r, &b) {
		return
	}
	if f := b.validate(false); len(f) > 0 {
		writeFields(w, f)
		return
	}
	sent, err := ms.SendMessage(r.Context(), store.MessageInput{
		Sender: ext, SenderKind: store.MsgKindExtension, SenderName: ms.MessageExtensionName(r.Context(), ext),
		Actor: actor(r).String(), TargetKind: b.TargetKind, Target: b.Target, ContentType: "text/plain", Body: []byte(b.Body),
	})
	if err != nil {
		s.messageSendError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, sentJSON(sent))
}
