package api

// The inbox AI (spec messaging-channels M-30, M-30a): the inbox-wide
// settings and kill switches, each number's mode and guardrails, the FAQ an
// auto-reply answers from (administrators), undoing a triage (agents and
// supervisors), and the counts (supervisors, and an AI client through MCP:
// they carry no customer data).

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/messaging/msgai"
	"github.com/azrtydxb/hello/internal/store"
)

// messagingAIStatus is what the console shows next to the AI settings.
type messagingAIStatus struct {
	// Available is whether the in-product AI is on (HELLO_AI_*), Reason why
	// not.
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	// EndpointPublic is true when the model's endpoint is not on a private
	// network (HELLO_AI_ALLOW_PUBLIC_ENDPOINT): customer texts leave it.
	EndpointPublic bool `json:"endpointPublic"`
	// AutoAllowed is HELLO_MSG_AI_AUTO: false stops every auto-reply.
	AutoAllowed bool `json:"autoAllowed"`
}

type messagingAIView struct {
	store.MessagingAI
	Status messagingAIStatus `json:"status"`
}

func (s *server) messagingAIStatus(r *http.Request) messagingAIStatus {
	st := messagingAIStatus{AutoAllowed: s.MessagingAIAuto, Reason: "the AI agent is not configured"}
	if s.AIAgent == nil {
		return st
	}
	on, reason := s.AIAgent.Enabled()
	st.Available, st.Reason = on, reason
	if on {
		st.Reason = ""
		if as, err := s.AIAgent.Status(r.Context()); err == nil {
			st.EndpointPublic = !as.EndpointPrivate
		}
	}
	return st
}

func (s *server) getMessagingAI(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	m, err := is.GetMessagingAI(r.Context())
	if err != nil {
		s.internal(w, "read messaging AI settings", err)
		return
	}
	writeJSON(w, http.StatusOK, messagingAIView{MessagingAI: m, Status: s.messagingAIStatus(r)})
}

type messagingAIChannelBody struct {
	ID        int64 `json:"id"`
	AIEnabled bool  `json:"aiEnabled"`
}

type messagingAIBody struct {
	Enabled         *bool                    `json:"enabled"`
	AutoEnabled     *bool                    `json:"autoEnabled"`
	BusinessName    *string                  `json:"businessName"`
	Intents         *[]string                `json:"intents"`
	SensitiveTopics *[]string                `json:"sensitiveTopics"`
	Channels        []messagingAIChannelBody `json:"channels"`
}

func (s *server) putMessagingAI(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	var b messagingAIBody
	if !decode(w, r, &b) {
		return
	}
	cur, err := is.GetMessagingAI(r.Context())
	if err != nil {
		s.internal(w, "read messaging AI settings", err)
		return
	}
	in := store.MessagingAIInput{Enabled: cur.Enabled, AutoEnabled: cur.AutoEnabled, BusinessName: cur.BusinessName,
		Intents: cur.Intents, SensitiveTopics: cur.SensitiveTopics, Channels: map[int64]bool{}}
	setIf(&in.Enabled, b.Enabled)
	setIf(&in.AutoEnabled, b.AutoEnabled)
	var f fieldErrs
	if b.BusinessName != nil {
		in.BusinessName = strings.TrimSpace(*b.BusinessName)
	}
	if !printable(in.BusinessName, 100, true) {
		f.add("businessName", "must be at most 100 printable characters")
	}
	if b.Intents != nil {
		v, ok := msgai.CleanLabels(*b.Intents)
		switch {
		case !ok:
			f.add("intents", "at most %d labels of at most %d printable characters", msgai.MaxLabels, msgai.MaxLabelLen)
		case len(v) == 0:
			f.add("intents", "needs at least one intent")
		default:
			in.Intents = v
		}
		for _, i := range in.Intents {
			if i == msgai.IntentOther {
				f.add("intents", "other is always allowed; do not list it")
				break
			}
		}
	}
	if b.SensitiveTopics != nil {
		v, ok := msgai.CleanLabels(*b.SensitiveTopics)
		if !ok {
			f.add("sensitiveTopics", "at most %d labels of at most %d printable characters", msgai.MaxLabels, msgai.MaxLabelLen)
		} else {
			in.SensitiveTopics = v
		}
	}
	for i, c := range b.Channels {
		if c.ID <= 0 {
			f.add("channels["+strconv.Itoa(i)+"].id", "must be a channel id")
			continue
		}
		in.Channels[c.ID] = c.AIEnabled
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	m, err := is.PutMessagingAI(r.Context(), actor(r).String(), in)
	if err != nil {
		s.inboxError(w, "messaging AI settings", err)
		return
	}
	writeJSON(w, http.StatusOK, messagingAIView{MessagingAI: m, Status: s.messagingAIStatus(r)})
}

func (s *server) getMessagingAddressAI(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	m, err := is.GetMsgAddressAI(r.Context(), id)
	if err != nil {
		s.storeError(w, "messaging number", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

type addressAIBody struct {
	Mode           *string   `json:"mode"`
	Redact         *bool     `json:"redact"`
	Disclosure     *bool     `json:"disclosure"`
	DisclosureText *string   `json:"disclosureText"`
	HoldingText    *string   `json:"holdingText"`
	MaxReplies     *int      `json:"maxReplies"`
	HourlyCap      *int      `json:"hourlyCap"`
	MinConfidence  *float64  `json:"minConfidence"`
	AutoIntents    *[]string `json:"autoIntents"`
	HandoffWords   *[]string `json:"handoffWords"`
}

// addressAIInput applies a request to a number's AI settings; what it
// leaves out keeps its value.
func addressAIInput(b addressAIBody, m store.MsgAddressAI) (store.MsgAddressAI, fieldErrs) {
	var f fieldErrs
	setIf(&m.Mode, b.Mode)
	setIf(&m.Redact, b.Redact)
	setIf(&m.Disclosure, b.Disclosure)
	setIf(&m.MaxReplies, b.MaxReplies)
	setIf(&m.HourlyCap, b.HourlyCap)
	setIf(&m.MinConfidence, b.MinConfidence)
	if b.DisclosureText != nil {
		m.DisclosureText = strings.TrimSpace(*b.DisclosureText)
	}
	if b.HoldingText != nil {
		m.HoldingText = strings.TrimSpace(*b.HoldingText)
	}
	if !msgai.ValidMode(m.Mode) {
		f.add("mode", "must be one of %v", msgai.Modes)
	}
	if !printable(m.DisclosureText, msgai.MaxDisclosure, true) {
		f.add("disclosureText", "must be at most %d printable characters", msgai.MaxDisclosure)
	}
	if m.Disclosure && m.DisclosureText == "" {
		f.add("disclosureText", "is required while the disclosure is on")
	}
	if !printable(m.HoldingText, msgai.MaxHolding, true) {
		f.add("holdingText", "must be at most %d printable characters", msgai.MaxHolding)
	}
	if m.MaxReplies < 1 || m.MaxReplies > 10 {
		f.add("maxReplies", "must be 1-10")
	}
	if m.HourlyCap < 1 || m.HourlyCap > 1000 {
		f.add("hourlyCap", "must be 1-1000")
	}
	if m.MinConfidence < 0 || m.MinConfidence > 1 {
		f.add("minConfidence", "must be between 0 and 1")
	}
	if b.AutoIntents != nil {
		if v, ok := msgai.CleanLabels(*b.AutoIntents); ok {
			m.AutoIntents = v
		} else {
			f.add("autoIntents", "at most %d labels of at most %d printable characters", msgai.MaxLabels, msgai.MaxLabelLen)
		}
	}
	if b.HandoffWords != nil {
		v, ok := msgai.CleanLabels(*b.HandoffWords)
		switch {
		case !ok:
			f.add("handoffWords", "at most %d words of at most %d printable characters", msgai.MaxLabels, msgai.MaxLabelLen)
		case len(v) == 0:
			f.add("handoffWords", "needs at least one word: a customer must always be able to reach a person")
		default:
			m.HandoffWords = v
		}
	}
	return m, f
}

func (s *server) putMessagingAddressAI(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b addressAIBody
	if !decode(w, r, &b) {
		return
	}
	cur, err := is.GetMsgAddressAI(r.Context(), id)
	if err != nil {
		s.storeError(w, "messaging number", err)
		return
	}
	in, f := addressAIInput(b, cur)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	m, err := is.PutMsgAddressAI(r.Context(), actor(r).String(), in)
	if err != nil {
		s.storeError(w, "messaging number", err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// The FAQ.

type faqBody struct {
	AddressID *int64  `json:"addressId"`
	Title     *string `json:"title"`
	Body      *string `json:"body"`
	Enabled   *bool   `json:"enabled"`
}

func faqInput(b faqBody, cur store.MsgFAQInput) (store.MsgFAQInput, fieldErrs) {
	var f fieldErrs
	if b.Title != nil {
		cur.Title = strings.TrimSpace(*b.Title)
	}
	if b.Body != nil {
		cur.Body = strings.TrimSpace(strings.ReplaceAll(*b.Body, "\r", ""))
	}
	setIf(&cur.Enabled, b.Enabled)
	if field, problem := msgai.CheckFAQ(cur.Title, cur.Body); field != "" {
		f.add(field, "%s", problem)
	}
	return cur, f
}

func (s *server) listMessagingFAQ(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	var address int64
	if v := r.URL.Query().Get("addressId"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "addressId must be a messaging number id")
			return
		}
		address = n
	}
	fs, err := is.ListMsgFAQ(r.Context(), address)
	if err != nil {
		s.internal(w, "list FAQ", err)
		return
	}
	writeJSON(w, http.StatusOK, items(fs))
}

func (s *server) createMessagingFAQ(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	var b faqBody
	if !decode(w, r, &b) {
		return
	}
	in, f := faqInput(b, store.MsgFAQInput{Enabled: true})
	if b.AddressID == nil || *b.AddressID <= 0 {
		f.add("addressId", "is required")
	} else {
		in.AddressID = *b.AddressID
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	got, err := is.CreateMsgFAQ(r.Context(), actor(r).String(), in)
	if errors.Is(err, store.ErrFAQFull) {
		writeError(w, http.StatusConflict, "faq_full", "the number has 50 FAQ snippets; remove one first")
		return
	}
	if err != nil {
		s.inboxError(w, "FAQ snippet", err)
		return
	}
	writeJSON(w, http.StatusCreated, got)
}

func (s *server) getMessagingFAQ(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	got, err := is.GetMsgFAQ(r.Context(), id)
	if err != nil {
		s.storeError(w, "FAQ snippet", err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *server) putMessagingFAQ(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b faqBody
	if !decode(w, r, &b) {
		return
	}
	cur, err := is.GetMsgFAQ(r.Context(), id)
	if err != nil {
		s.storeError(w, "FAQ snippet", err)
		return
	}
	in, f := faqInput(b, store.MsgFAQInput{AddressID: cur.AddressID, Title: cur.Title, Body: cur.Body, Enabled: cur.Enabled})
	if b.AddressID != nil && *b.AddressID != cur.AddressID {
		f.add("addressId", "a snippet stays on its number; create one on the other number instead")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	got, err := is.UpdateMsgFAQ(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.storeError(w, "FAQ snippet", err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *server) deleteMessagingFAQ(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := is.DeleteMsgFAQ(r.Context(), actor(r).String(), id); err != nil {
		s.storeError(w, "FAQ snippet", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The inbox.

func (s *server) revertInboxAICommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, true)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	c, err := is.RevertConversationAI(r.Context(), acc, id)
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) revertInboxAI(w http.ResponseWriter, r *http.Request) {
	s.revertInboxAICommon(w, r, true)
}

func (s *server) revertMyInboxAI(w http.ResponseWriter, r *http.Request) {
	s.revertInboxAICommon(w, r, false)
}

func (s *server) getInboxAIStats(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	st, err := is.GetInboxAIStats(r.Context())
	if err != nil {
		s.internal(w, "inbox AI counts", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
