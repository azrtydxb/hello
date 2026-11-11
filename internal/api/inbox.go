package api

// The messaging inbox (spec messaging-channels, "inbox core"): the channels
// and numbers an administrator sets up, the opt-out list, and the unified
// inbox agents and supervisors answer customers from. A supervisor (operator
// and above, /inbox) reaches every conversation; anyone with the inbox (the
// user role included, /me/inbox) reaches the conversations assigned to them
// and those they replied to. A viewer reaches none: message text is customer
// data.
//
// Message text and customer numbers are never logged and never put in an
// audit row.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/store"
)

// InboxStore is the inbox persistence the API needs; *store.Store
// implements it.
type InboxStore interface {
	ListMsgChannels(ctx context.Context) ([]store.MsgChannel, error)
	GetMsgChannel(ctx context.Context, id int64) (store.MsgChannel, error)
	CreateMsgChannel(ctx context.Context, actor string, in store.MsgChannelInput) (store.MsgChannel, error)
	UpdateMsgChannel(ctx context.Context, actor string, id int64, in store.MsgChannelInput) (store.MsgChannel, error)
	DeleteMsgChannel(ctx context.Context, actor string, id int64) error
	ListMsgAddresses(ctx context.Context, channelID int64) ([]store.MsgAddress, error)
	GetMsgAddress(ctx context.Context, id int64) (store.MsgAddress, error)
	CreateMsgAddress(ctx context.Context, actor string, in store.MsgAddressInput) (store.MsgAddress, error)
	UpdateMsgAddress(ctx context.Context, actor string, id int64, in store.MsgAddressInput) (store.MsgAddress, error)
	DeleteMsgAddress(ctx context.Context, actor string, id int64) error
	GetMessagingSettings(ctx context.Context) (store.MessagingSettings, error)
	PutMessagingSettings(ctx context.Context, actor string, m store.MessagingSettings) (store.MessagingSettings, error)
	ListOptOuts(ctx context.Context, f store.OptOutFilter, limit int) ([]store.MsgOptOut, error)
	RecordOptOut(ctx context.Context, actor string, addressID int64, peerKey, note string) (store.MsgOptOut, error)
	Reconsent(ctx context.Context, actor string, id int64, note string) (store.MsgOptOut, error)
	ErasePeer(ctx context.Context, actor, peerKey string) (store.EraseReport, error)

	ListConversations(ctx context.Context, acc store.InboxAccess, f store.ConvFilter, limit int) ([]store.Conversation, string, error)
	GetConversation(ctx context.Context, acc store.InboxAccess, id int64) (store.Conversation, error)
	ListConversationMessages(ctx context.Context, acc store.InboxAccess, convID, before int64, limit int) ([]store.ConvMessage, int64, error)
	SendConversationText(ctx context.Context, acc store.InboxAccess, in store.SendInput) (store.ConvMessage, bool, error)
	AddNote(ctx context.Context, acc store.InboxAccess, convID int64, body string) (store.ConvMessage, error)
	ClaimConversation(ctx context.Context, acc store.InboxAccess, convID int64) (store.Conversation, error)
	AssignConversation(ctx context.Context, acc store.InboxAccess, convID, toUser int64) (store.Conversation, error)
	SetConversationStatus(ctx context.Context, acc store.InboxAccess, convID int64, status string, until *time.Time) (store.Conversation, error)
	SetConversationTags(ctx context.Context, acc store.InboxAccess, convID int64, tags []string) (store.Conversation, error)
	MarkConversationRead(ctx context.Context, acc store.InboxAccess, convID int64) error
	BulkConversations(ctx context.Context, acc store.InboxAccess, ids []int64, action string, toUser int64) (store.BulkResult, error)
	ListInboxAgents(ctx context.Context) ([]store.InboxAgent, error)
	GetInboxStatus(ctx context.Context, acc store.InboxAccess) (store.InboxStatus, error)
	GetInboxStats(ctx context.Context) (store.InboxStats, error)
	ListInboxEvents(ctx context.Context, userID, after int64, limit int) ([]store.InboxEvent, error)
	LatestInboxEventID(ctx context.Context, userID int64) (int64, error)
	TouchInboxPresence(ctx context.Context, userID int64) error
}

var _ InboxStore = (*store.Store)(nil)

func (s *server) inboxStore(w http.ResponseWriter) (InboxStore, bool) {
	if s.Inbox == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the messaging inbox is not configured")
		return nil, false
	}
	return s.Inbox, true
}

// storeFailure answers a failed inbox store call: the inbox's own refusals
// first, then the usual 404, 409 and 500.
func (s *server) inboxError(w http.ResponseWriter, what string, err error) {
	var v *store.ValidationError
	switch {
	case errors.As(err, &v):
		writeFields(w, v.Fields)
	case errors.Is(err, store.ErrOptedOut):
		writeError(w, http.StatusUnprocessableEntity, "opted_out", "the customer opted out of this number; nobody can text them until they reply START")
	case errors.Is(err, store.ErrNotAgent):
		writeError(w, http.StatusUnprocessableEntity, "not_agent", "that user cannot take conversations")
	case errors.Is(err, store.ErrChannelUnavailable):
		writeError(w, http.StatusConflict, "channel_unavailable", "the channel or number is disabled or cannot send")
	case errors.Is(err, store.ErrConversationState):
		writeError(w, http.StatusConflict, "conversation_state", "the conversation is not in a state that allows this")
	case errors.Is(err, store.ErrMessagesDisabled):
		writeError(w, http.StatusConflict, "messaging_disabled", "text messaging is turned off")
	case errors.Is(err, store.ErrNoExternalTrunk):
		writeError(w, http.StatusConflict, "no_messaging_trunk", "no enabled trunk has messaging turned on")
	case errors.Is(err, store.ErrNoSecretBox):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the messaging inbox needs HELLO_SECRET_KEY")
	default:
		s.storeError(w, what, err)
	}
}

// inboxCaller identifies the caller of an inbox route and what they reach.
// all is true on the supervisor routes. Writes need a user (the actor's id
// is the assignee, the sender of a reply); reads do not, so a service account
// granted the messages scope can read.
func (s *server) inboxCaller(w http.ResponseWriter, r *http.Request, all, write bool) (InboxStore, store.InboxAccess, bool) {
	var acc store.InboxAccess
	is, ok := s.inboxStore(w)
	if !ok {
		return nil, acc, false
	}
	a := actor(r)
	if !a.Role.AtLeast(auth.RoleUser) || a.Role == auth.RoleViewer {
		writeError(w, http.StatusForbidden, "forbidden_role", "the inbox is for agents and supervisors; a viewer cannot read customer messages")
		return nil, acc, false
	}
	if write && (a.UserID <= 0 || a.Kind == auth.KindService) {
		writeError(w, http.StatusForbidden, "no_user", "this credential has no user to act as")
		return nil, acc, false
	}
	name := a.Username
	if a.Kind == auth.KindService {
		name = a.ServiceName
	}
	return is, store.InboxAccess{UserID: a.UserID, All: all, Actor: a.String(), Name: name}, true
}

// convID parses the {id} of an inbox route; anything else is 404.
func convID(w http.ResponseWriter, r *http.Request) (int64, bool) { return pathID(w, r) }

// Channels, numbers, settings, opt-outs (admin).

// msgChannelBody is the create and replace request.
type msgChannelBody struct {
	Kind     *string `json:"kind"`
	Provider *string `json:"provider"`
	Name     *string `json:"name"`
	Enabled  *bool   `json:"enabled"`
	TrunkID  *int64  `json:"trunkId"`
}

func (b msgChannelBody) input(create bool, cur store.MsgChannel) (store.MsgChannelInput, fieldErrs) {
	var f fieldErrs
	in := store.MsgChannelInput{Kind: cur.Kind, Provider: cur.Provider, Name: cur.Name, Enabled: true, TrunkID: cur.TrunkID}
	if !create {
		in.Enabled = cur.Enabled
	}
	if b.Kind != nil {
		if *b.Kind != messaging.KindSMS {
			f.add("kind", "only sms channels exist yet")
		}
		if !create && *b.Kind != cur.Kind {
			f.add("kind", "cannot be changed")
		}
		in.Kind = *b.Kind
	}
	if b.Provider != nil {
		if *b.Provider != messaging.ProviderSIPTrunk {
			f.add("provider", "only sip_trunk exists yet")
		}
		if !create && *b.Provider != cur.Provider {
			f.add("provider", "cannot be changed")
		}
		in.Provider = *b.Provider
	}
	if create {
		if b.Kind == nil {
			f.add("kind", "is required")
		}
		if b.Provider == nil {
			f.add("provider", "is required")
		}
	}
	if b.Name != nil {
		in.Name = strings.TrimSpace(*b.Name)
	}
	if in.Name == "" || !printable(in.Name, 100, true) {
		f.add("name", "must be 1-100 printable characters")
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	if b.TrunkID != nil {
		in.TrunkID = b.TrunkID
	}
	if in.TrunkID == nil || *in.TrunkID <= 0 {
		f.add("trunkId", "a SIP trunk channel needs a trunk")
	}
	return in, f
}

func (s *server) listMessagingChannels(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	cs, err := is.ListMsgChannels(r.Context())
	if err != nil {
		s.internal(w, "list messaging channels", err)
		return
	}
	writeJSON(w, http.StatusOK, items(cs))
}

func (s *server) createMessagingChannel(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	var b msgChannelBody
	if !decode(w, r, &b) {
		return
	}
	in, f := b.input(true, store.MsgChannel{})
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	c, err := is.CreateMsgChannel(r.Context(), actor(r).String(), in)
	if err != nil {
		s.inboxError(w, "messaging channel", err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) getMessagingChannel(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := is.GetMsgChannel(r.Context(), id)
	if err != nil {
		s.storeError(w, "messaging channel", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) putMessagingChannel(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b msgChannelBody
	if !decode(w, r, &b) {
		return
	}
	cur, err := is.GetMsgChannel(r.Context(), id)
	if err != nil {
		s.storeError(w, "messaging channel", err)
		return
	}
	in, f := b.input(false, cur)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	c, err := is.UpdateMsgChannel(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.inboxError(w, "messaging channel", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) deleteMessagingChannel(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := is.DeleteMsgChannel(r.Context(), actor(r).String(), id); err != nil {
		s.inboxError(w, "messaging channel", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testMessagingChannel checks that a channel can work: its trunk exists, is
// enabled and has SMS over SIP turned on, and the channel has a number.
func (s *server) testMessagingChannel(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := is.GetMsgChannel(r.Context(), id)
	if err != nil {
		s.storeError(w, "messaging channel", err)
		return
	}
	type check struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	checks := []check{}
	add := func(name string, ok bool, detail string) { checks = append(checks, check{name, ok, detail}) }
	add("channel", c.Enabled, map[bool]string{true: "enabled", false: "the channel is turned off"}[c.Enabled])
	if c.TrunkID == nil {
		add("trunk", false, "the channel's trunk was deleted; pick another")
	} else if t, err := s.Store.GetTrunk(r.Context(), *c.TrunkID); err != nil {
		add("trunk", false, "the channel's trunk was deleted; pick another")
	} else {
		add("trunk", t.Enabled, map[bool]string{true: "trunk " + t.Name + " is enabled", false: "trunk " + t.Name + " is disabled"}[t.Enabled])
		add("sms over sip", t.Messaging, map[bool]string{true: "turned on for the trunk", false: "turn on SMS over SIP on the trunk, or no text arrives"}[t.Messaging])
	}
	addrs, err := is.ListMsgAddresses(r.Context(), id)
	if err != nil {
		s.internal(w, "list messaging addresses", err)
		return
	}
	on := 0
	for _, a := range addrs {
		if a.Enabled {
			on++
		}
	}
	add("numbers", on > 0, fmt.Sprintf("%d enabled", on))
	all := true
	for _, c := range checks {
		all = all && c.OK
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": all, "checks": checks})
}

// addressBody is the create and replace request of a number.
type addressBody struct {
	ChannelID   *int64    `json:"channelId"`
	Address     *string   `json:"address"`
	Label       *string   `json:"label"`
	Enabled     *bool     `json:"enabled"`
	OptOutWords *[]string `json:"optOutKeywords"`
	HelpText    *string   `json:"helpText"`
	OptOutText  *string   `json:"optOutText"`
}

func (s *server) addressInput(b addressBody, create bool, cur store.MsgAddress) (store.MsgAddressInput, fieldErrs) {
	var f fieldErrs
	in := store.MsgAddressInput{
		ChannelID: cur.ChannelID, Address: cur.Address, Key: cur.Key, Label: cur.Label, Enabled: true,
		OptOutWords: cur.OptOutWords, HelpText: cur.HelpText, OptOutText: cur.OptOutText,
	}
	if !create {
		in.Enabled = cur.Enabled
	}
	if create {
		if b.ChannelID == nil || *b.ChannelID <= 0 {
			f.add("channelId", "is required")
		} else {
			in.ChannelID = *b.ChannelID
		}
		if b.Address == nil {
			f.add("address", "is required")
		} else {
			in.Address = strings.TrimSpace(*b.Address)
			key, ok := s.Dialing.Key(in.Address, nil)
			if !store.ValidExternalNumber(in.Address) || !ok {
				f.add("address", "must be a phone number of at least 4 digits, optionally starting with +")
			}
			in.Key = key
			in.Suffix, _ = phonebook.Suffix(key)
		}
	} else {
		if b.ChannelID != nil && *b.ChannelID != cur.ChannelID {
			f.add("channelId", "cannot be changed")
		}
		if b.Address != nil && strings.TrimSpace(*b.Address) != cur.Address {
			f.add("address", "cannot be changed")
		}
	}
	if b.Label != nil {
		in.Label = strings.TrimSpace(*b.Label)
	}
	if !printable(in.Label, 100, true) {
		f.add("label", "must be at most 100 printable characters")
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	if b.OptOutWords != nil {
		in.OptOutWords = *b.OptOutWords
		if len(in.OptOutWords) > 20 {
			f.add("optOutKeywords", "at most 20 keywords")
		}
		for _, w := range in.OptOutWords {
			if w = strings.TrimSpace(w); w == "" || len(w) > 32 || strings.ContainsAny(w, " \t\r\n") || !printable(w, 32, false) {
				f.add("optOutKeywords", "each must be one word of at most 32 characters")
				break
			}
		}
	}
	for path, v := range map[string]*string{"helpText": b.HelpText, "optOutText": b.OptOutText} {
		if v == nil {
			continue
		}
		t, ok := messaging.CleanBody(*v, 480)
		if *v != "" && !ok {
			f.add(path, "must be at most 480 bytes of text")
		}
		if path == "helpText" {
			in.HelpText = t
		} else {
			in.OptOutText = t
		}
	}
	return in, f
}

func (s *server) listMessagingAddresses(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	var channel int64
	if v := r.URL.Query().Get("channelId"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "channelId must be a channel id")
			return
		}
		channel = n
	}
	as, err := is.ListMsgAddresses(r.Context(), channel)
	if err != nil {
		s.internal(w, "list messaging addresses", err)
		return
	}
	writeJSON(w, http.StatusOK, items(as))
}

func (s *server) createMessagingAddress(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	var b addressBody
	if !decode(w, r, &b) {
		return
	}
	in, f := s.addressInput(b, true, store.MsgAddress{})
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	a, err := is.CreateMsgAddress(r.Context(), actor(r).String(), in)
	if err != nil {
		s.inboxError(w, "messaging number", err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *server) getMessagingAddress(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, err := is.GetMsgAddress(r.Context(), id)
	if err != nil {
		s.storeError(w, "messaging number", err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) putMessagingAddress(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b addressBody
	if !decode(w, r, &b) {
		return
	}
	cur, err := is.GetMsgAddress(r.Context(), id)
	if err != nil {
		s.storeError(w, "messaging number", err)
		return
	}
	in, f := s.addressInput(b, false, cur)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	a, err := is.UpdateMsgAddress(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.inboxError(w, "messaging number", err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) deleteMessagingAddress(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := is.DeleteMsgAddress(r.Context(), actor(r).String(), id); err != nil {
		s.inboxError(w, "messaging number", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type messagingSettingsBody struct {
	RetentionSMSDays  *int  `json:"retentionSmsDays"`
	RetentionChatDays *int  `json:"retentionChatDays"`
	ReopenDays        *int  `json:"reopenDays"`
	EmailNotify       *bool `json:"emailNotify"`
}

func (s *server) getMessagingSettings(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	m, err := is.GetMessagingSettings(r.Context())
	if err != nil {
		s.internal(w, "read messaging settings", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *server) putMessagingSettings(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	var b messagingSettingsBody
	if !decode(w, r, &b) {
		return
	}
	cur, err := is.GetMessagingSettings(r.Context())
	if err != nil {
		s.internal(w, "read messaging settings", err)
		return
	}
	setIf(&cur.RetentionSMSDays, b.RetentionSMSDays)
	setIf(&cur.RetentionChatDays, b.RetentionChatDays)
	setIf(&cur.ReopenDays, b.ReopenDays)
	setIf(&cur.EmailNotify, b.EmailNotify)
	var f fieldErrs
	if cur.RetentionSMSDays < 1 || cur.RetentionSMSDays > 3650 {
		f.add("retentionSmsDays", "must be 1-3650")
	}
	if cur.RetentionChatDays < 1 || cur.RetentionChatDays > 3650 {
		f.add("retentionChatDays", "must be 1-3650")
	}
	if cur.ReopenDays < 0 || cur.ReopenDays > 365 {
		f.add("reopenDays", "must be 0-365")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	m, err := is.PutMessagingSettings(r.Context(), actor(r).String(), cur)
	if err != nil {
		s.internal(w, "save messaging settings", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// peerKey reduces a number a person typed to the key conversations are filed
// under; ok is false for a number that cannot name a customer.
func (s *server) peerKey(number string) (string, bool) {
	number = strings.TrimSpace(number)
	if !store.ValidExternalNumber(number) {
		return "", false
	}
	return s.Dialing.Key(number, nil)
}

func (s *server) listMessagingOptOuts(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.OptOutFilter{Q: strings.TrimSpace(q.Get("q")), All: q.Get("all") == "true"}
	if len(f.Q) > 32 {
		badRequest(w, "q must be at most 32 characters")
		return
	}
	if v := q.Get("addressId"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "addressId must be a number id")
			return
		}
		f.AddressID = n
	}
	out, err := is.ListOptOuts(r.Context(), f, 500)
	if err != nil {
		s.internal(w, "list opt-outs", err)
		return
	}
	writeJSON(w, http.StatusOK, items(out))
}

type optOutBody struct {
	AddressID int64  `json:"addressId"`
	Peer      string `json:"peer"`
	Note      string `json:"note"`
}

func (s *server) recordMessagingOptOut(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	var b optOutBody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	key, kok := s.peerKey(b.Peer)
	if !kok {
		f.add("peer", "must be a phone number")
	}
	if b.AddressID <= 0 {
		f.add("addressId", "is required")
	}
	note, nok := messaging.CleanBody(b.Note, 200)
	if b.Note != "" && !nok {
		f.add("note", "must be at most 200 bytes of text")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	o, err := is.RecordOptOut(r.Context(), actor(r).String(), b.AddressID, key, note)
	if err != nil {
		s.inboxError(w, "opt-out", err)
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

type reconsentBody struct {
	Note string `json:"note"`
}

func (s *server) reconsentMessagingOptOut(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b reconsentBody
	if !decode(w, r, &b) {
		return
	}
	note, nok := messaging.CleanBody(b.Note, 200)
	if !nok {
		writeFields(w, fieldErrs{{Path: "note", Message: "is required: say how the customer gave their consent"}})
		return
	}
	o, err := is.Reconsent(r.Context(), actor(r).String(), id, note)
	if err != nil {
		s.inboxError(w, "opt-out", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

type eraseBody struct {
	Peer string `json:"peer"`
}

func (s *server) eraseMessagingPeer(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	var b eraseBody
	if !decode(w, r, &b) {
		return
	}
	key, kok := s.peerKey(b.Peer)
	if !kok {
		writeFields(w, fieldErrs{{Path: "peer", Message: "must be a phone number"}})
		return
	}
	rep, err := is.ErasePeer(r.Context(), actor(r).String(), key)
	if err != nil {
		s.inboxError(w, "erase", err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// The inbox (agents and supervisors).

const (
	defaultInboxLimit = 50
	maxInboxLimit     = 100
)

func (s *server) listInboxConversationsCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.ConvFilter{View: q.Get("view"), Status: q.Get("status"), Q: strings.TrimSpace(q.Get("q"))}
	switch f.View {
	case "", "all", "mine", "unassigned":
	default:
		badRequest(w, "view must be all, mine or unassigned")
		return
	}
	switch f.Status {
	case "", "any", messaging.StatusOpen, messaging.StatusPending, messaging.StatusSnoozed, messaging.StatusResolved, messaging.StatusSpam:
	default:
		badRequest(w, "status must be open, pending, snoozed, resolved, spam or any")
		return
	}
	if len(f.Q) > 100 {
		badRequest(w, "q must be at most 100 characters")
		return
	}
	if v := q.Get("channelId"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "channelId must be a channel id")
			return
		}
		f.ChannelID = n
	}
	if v := q.Get("cursor"); v != "" {
		c, ok := store.ParseConvCursor(v)
		if !ok {
			badRequest(w, "cursor must be a value from a previous page")
			return
		}
		f.After = &c
	}
	limit := defaultInboxLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxInboxLimit {
			badRequest(w, "limit must be 1-100")
			return
		}
		limit = n
	}
	cs, next, err := is.ListConversations(r.Context(), acc, f, limit)
	if err != nil {
		s.internal(w, "list conversations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": itemsOrEmpty(cs), "next": next})
}

// itemsOrEmpty makes a nil slice an empty JSON array.
func itemsOrEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func (s *server) getInboxConversationCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, false)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	c, err := is.GetConversation(r.Context(), acc, id)
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) listInboxMessagesCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, false)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var before int64
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "cursor must be a value from a previous page")
			return
		}
		before = n
	}
	limit := defaultInboxLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxInboxLimit {
			badRequest(w, "limit must be 1-100")
			return
		}
		limit = n
	}
	ms, next, err := is.ListConversationMessages(r.Context(), acc, id, before, limit)
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	nextCursor := ""
	if next > 0 {
		nextCursor = strconv.FormatInt(next, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": itemsOrEmpty(ms), "next": nextCursor})
}

type inboxSendBody struct {
	Body        string `json:"body"`
	ClientMsgID string `json:"clientMsgId"`
}

func (s *server) sendInboxMessageCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, true)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	var b inboxSendBody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	body, bok := messaging.CleanBody(b.Body, messaging.MaxBodyBytes)
	if !bok {
		f.add("body", "must be 1-%d bytes of text", messaging.MaxBodyBytes)
	}
	if b.ClientMsgID != "" && !printable(b.ClientMsgID, 64, false) {
		f.add("clientMsgId", "must be at most 64 printable characters without spaces")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	m, created, err := is.SendConversationText(r.Context(), acc, store.SendInput{ConversationID: id, Body: body, ClientMsgID: b.ClientMsgID})
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, m)
}

type inboxNoteBody struct {
	Body string `json:"body"`
}

func (s *server) addInboxNoteCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, true)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	var b inboxNoteBody
	if !decode(w, r, &b) {
		return
	}
	body, bok := messaging.CleanBody(b.Body, messaging.MaxNoteBytes)
	if !bok {
		writeFields(w, fieldErrs{{Path: "body", Message: fmt.Sprintf("must be 1-%d bytes of text", messaging.MaxNoteBytes)}})
		return
	}
	m, err := is.AddNote(r.Context(), acc, id, body)
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *server) claimInboxConversationCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, true)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	c, err := is.ClaimConversation(r.Context(), acc, id)
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type inboxAssignBody struct {
	// UserID is the user to hand the conversation to; 0 returns it to the
	// pool (supervisors only).
	UserID int64 `json:"userId"`
}

func (s *server) assignInboxConversationCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, true)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	var b inboxAssignBody
	if !decode(w, r, &b) {
		return
	}
	if b.UserID < 0 {
		writeFields(w, fieldErrs{{Path: "userId", Message: "must be a user id, or 0 for the pool"}})
		return
	}
	c, err := is.AssignConversation(r.Context(), acc, id, b.UserID)
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type inboxStatusBody struct {
	Status       string     `json:"status"`
	SnoozedUntil *time.Time `json:"snoozedUntil"`
}

func (s *server) setInboxStatusCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, true)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	var b inboxStatusBody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	switch b.Status {
	case messaging.StatusOpen, messaging.StatusPending, messaging.StatusResolved, messaging.StatusSpam:
		if b.SnoozedUntil != nil {
			f.add("snoozedUntil", "only a snoozed conversation has one")
		}
	case messaging.StatusSnoozed:
		switch {
		case b.SnoozedUntil == nil:
			f.add("snoozedUntil", "is required to snooze")
		case !b.SnoozedUntil.After(time.Now()) || b.SnoozedUntil.After(time.Now().AddDate(0, 0, 30)):
			f.add("snoozedUntil", "must be in the future, within 30 days")
		}
	default:
		f.add("status", "must be open, pending, snoozed, resolved or spam")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	c, err := is.SetConversationStatus(r.Context(), acc, id, b.Status, b.SnoozedUntil)
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type inboxTagsBody struct {
	Tags []string `json:"tags"`
}

func (s *server) setInboxTagsCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, true)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	var b inboxTagsBody
	if !decode(w, r, &b) {
		return
	}
	tags, tok := messaging.CleanTags(b.Tags)
	if !tok {
		writeFields(w, fieldErrs{{Path: "tags", Message: fmt.Sprintf("at most %d tags of at most %d printable characters", messaging.MaxTags, messaging.MaxTagLen)}})
		return
	}
	c, err := is.SetConversationTags(r.Context(), acc, id, tags)
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) readInboxConversationCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, true)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	if err := is.MarkConversationRead(r.Context(), acc, id); err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The supervisor and agent routes share their handlers; they differ only in
// what the caller reaches.

func (s *server) listInboxConversations(w http.ResponseWriter, r *http.Request) {
	s.listInboxConversationsCommon(w, r, true)
}
func (s *server) getInboxConversation(w http.ResponseWriter, r *http.Request) {
	s.getInboxConversationCommon(w, r, true)
}
func (s *server) listInboxMessages(w http.ResponseWriter, r *http.Request) {
	s.listInboxMessagesCommon(w, r, true)
}
func (s *server) sendInboxMessage(w http.ResponseWriter, r *http.Request) {
	s.sendInboxMessageCommon(w, r, true)
}
func (s *server) addInboxNote(w http.ResponseWriter, r *http.Request) {
	s.addInboxNoteCommon(w, r, true)
}
func (s *server) claimInboxConversation(w http.ResponseWriter, r *http.Request) {
	s.claimInboxConversationCommon(w, r, true)
}
func (s *server) assignInboxConversation(w http.ResponseWriter, r *http.Request) {
	s.assignInboxConversationCommon(w, r, true)
}
func (s *server) setInboxStatus(w http.ResponseWriter, r *http.Request) {
	s.setInboxStatusCommon(w, r, true)
}
func (s *server) setInboxTags(w http.ResponseWriter, r *http.Request) {
	s.setInboxTagsCommon(w, r, true)
}
func (s *server) readInboxConversation(w http.ResponseWriter, r *http.Request) {
	s.readInboxConversationCommon(w, r, true)
}

func (s *server) listMyInboxConversations(w http.ResponseWriter, r *http.Request) {
	s.listInboxConversationsCommon(w, r, false)
}
func (s *server) getMyInboxConversation(w http.ResponseWriter, r *http.Request) {
	s.getInboxConversationCommon(w, r, false)
}
func (s *server) listMyInboxMessages(w http.ResponseWriter, r *http.Request) {
	s.listInboxMessagesCommon(w, r, false)
}
func (s *server) sendMyInboxMessage(w http.ResponseWriter, r *http.Request) {
	s.sendInboxMessageCommon(w, r, false)
}
func (s *server) addMyInboxNote(w http.ResponseWriter, r *http.Request) {
	s.addInboxNoteCommon(w, r, false)
}
func (s *server) claimMyInboxConversation(w http.ResponseWriter, r *http.Request) {
	s.claimInboxConversationCommon(w, r, false)
}
func (s *server) assignMyInboxConversation(w http.ResponseWriter, r *http.Request) {
	s.assignInboxConversationCommon(w, r, false)
}
func (s *server) setMyInboxStatus(w http.ResponseWriter, r *http.Request) {
	s.setInboxStatusCommon(w, r, false)
}
func (s *server) setMyInboxTags(w http.ResponseWriter, r *http.Request) {
	s.setInboxTagsCommon(w, r, false)
}
func (s *server) readMyInboxConversation(w http.ResponseWriter, r *http.Request) {
	s.readInboxConversationCommon(w, r, false)
}

type inboxBulkBody struct {
	IDs    []int64 `json:"ids"`
	Action string  `json:"action"`
	UserID int64   `json:"userId"`
}

func (s *server) bulkInboxConversations(w http.ResponseWriter, r *http.Request) {
	is, acc, ok := s.inboxCaller(w, r, true, true)
	if !ok {
		return
	}
	var b inboxBulkBody
	if !decode(w, r, &b) {
		return
	}
	var f fieldErrs
	if len(b.IDs) == 0 || len(b.IDs) > 200 {
		f.add("ids", "must list 1-200 conversations")
	}
	for _, id := range b.IDs {
		if id <= 0 {
			f.add("ids", "must be conversation ids")
			break
		}
	}
	switch b.Action {
	case "resolve", "reopen":
		if b.UserID != 0 {
			f.add("userId", "only an assign has one")
		}
	case "assign":
		if b.UserID < 0 {
			f.add("userId", "must be a user id, or 0 for the pool")
		}
	default:
		f.add("action", "must be resolve, reopen or assign")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	res, err := is.BulkConversations(r.Context(), acc, b.IDs, b.Action, b.UserID)
	if err != nil {
		s.inboxError(w, "conversations", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) inboxStats(w http.ResponseWriter, r *http.Request) {
	is, _, ok := s.inboxCaller(w, r, true, false)
	if !ok {
		return
	}
	st, err := is.GetInboxStats(r.Context())
	if err != nil {
		s.internal(w, "inbox stats", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) myInboxStatus(w http.ResponseWriter, r *http.Request) {
	is, acc, ok := s.inboxCaller(w, r, actor(r).Role.AtLeast(auth.RoleOperator), false)
	if !ok {
		return
	}
	st, err := is.GetInboxStatus(r.Context(), acc)
	if err != nil {
		s.internal(w, "inbox status", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) listMyInboxAgents(w http.ResponseWriter, r *http.Request) {
	is, _, ok := s.inboxCaller(w, r, false, false)
	if !ok {
		return
	}
	as, err := is.ListInboxAgents(r.Context())
	if err != nil {
		s.internal(w, "list inbox agents", err)
		return
	}
	writeJSON(w, http.StatusOK, items(as))
}

// inboxPoll is how often a stream looks for new events; presenceEvery is how
// often it tells the email fallback the inbox is open. Variables for tests.
var (
	inboxPoll     = time.Second
	presenceEvery = 30 * time.Second
)

// streamMyInbox is the live feed of the caller's inbox: server-sent events
// whose ids come from the conversation_events table, so a stream that
// reconnects with Last-Event-ID misses nothing, on whichever pod it lands. An
// event is only ids (conversation, message); the page reads the rest through
// the API, which checks access again. A user only ever gets their own
// events.
func (s *server) streamMyInbox(w http.ResponseWriter, r *http.Request) {
	is, acc, ok := s.inboxCaller(w, r, false, true)
	if !ok {
		return
	}
	ctx := r.Context()
	var after int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			badRequest(w, "Last-Event-ID must be an event id")
			return
		}
		after = n
	} else {
		latest, err := is.LatestInboxEventID(ctx, acc.UserID)
		if err != nil {
			s.internal(w, "inbox stream", err)
			return
		}
		after = latest
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("retry: 3000\n\n")); err != nil || rc.Flush() != nil {
		return
	}
	_ = is.TouchInboxPresence(ctx, acc.UserID)
	poll := time.NewTicker(inboxPoll)
	defer poll.Stop()
	keep := time.NewTicker(sseKeepalive)
	defer keep.Stop()
	present := time.NewTicker(presenceEvery)
	defer present.Stop()
	for {
		evs, err := is.ListInboxEvents(ctx, acc.UserID, after, 100)
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Error("inbox stream: reading events", "error", err)
			}
			return
		}
		for _, e := range evs {
			after = e.ID
			frame := fmt.Sprintf("id: %d\nevent: %s\ndata: {\"conversationId\":%d,\"messageId\":%d}\n\n", e.ID, e.Type, e.ConversationID, e.MessageID)
			if _, err := w.Write([]byte(frame)); err != nil {
				return
			}
		}
		if len(evs) > 0 && rc.Flush() != nil {
			return
		}
		if len(evs) == 100 {
			continue // more waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-keep.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
				return
			}
		case <-present.C:
			_ = is.TouchInboxPresence(ctx, acc.UserID)
		case <-poll.C:
		}
	}
}
