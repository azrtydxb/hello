package api

// The chat sites of the LAN chat widget (spec messaging-channels M-28, M-29a
// to M-29c): administrators set the sites up and lift blocks; agents block a
// visitor, answer an offline chat by email and tell the visitor they are
// typing, from the inbox they already use. The widget itself is served by the
// chat listener, not by this API.
//
// Visitor names, email addresses and message text are never logged and never
// in an audit row.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/mailer"
	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/store"
)

// ChatStore is the chat persistence the API needs; *store.Store implements
// it. The inbox store carries it, so one wiring serves both.
type ChatStore interface {
	ListChatSites(ctx context.Context) ([]store.ChatSite, error)
	GetChatSite(ctx context.Context, id int64) (store.ChatSite, error)
	CreateChatSite(ctx context.Context, actor string, in store.ChatSiteInput) (store.ChatSite, error)
	UpdateChatSite(ctx context.Context, actor string, id int64, in store.ChatSiteInput) (store.ChatSite, error)
	DeleteChatSite(ctx context.Context, actor string, id int64) error
	ListChatBlocks(ctx context.Context, siteID int64) ([]store.ChatBlock, error)
	DeleteChatBlock(ctx context.Context, actor string, id int64) error
	BlockChatVisitor(ctx context.Context, acc store.InboxAccess, convID int64, ttl time.Duration) (store.ChatBlock, error)
	PrepareChatEmailReply(ctx context.Context, acc store.InboxAccess, in store.SendInput) (store.ChatEmailReply, bool, error)
	FinishChatEmailReply(ctx context.Context, msgID int64, sent bool) error
	ChatAgentTyping(ctx context.Context, acc store.InboxAccess, convID int64) error
}

var _ ChatStore = (*store.Store)(nil)

var accentRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// chatSiteView is a site with what the administrator should know about it.
type chatSiteView struct {
	store.ChatSite
	// Warnings are settings that work but deserve a second look.
	Warnings []string `json:"warnings"`
}

func viewChatSite(c store.ChatSite) chatSiteView {
	w := []string{}
	for _, o := range c.AllowedOrigins {
		if messaging.OriginLooksPublic(o) {
			w = append(w, fmt.Sprintf("%s looks like a public address: the LAN chat widget is for intranet pages, and a public site needs the public listener", o))
		}
	}
	if len(c.AllowedOrigins) == 0 {
		w = append(w, "no allowed origins: no page can embed the widget until one is listed")
	}
	if c.Challenge == store.ChallengeNone {
		w = append(w, "no challenge: anyone on the network can open sessions without proving anything; the limits still apply")
	}
	if c.Challenge == store.ChallengeTurnstile {
		w = append(w, "Turnstile needs the visitor's browser and this server to reach Cloudflare; it does not work on an isolated LAN")
	}
	return chatSiteView{ChatSite: c, Warnings: w}
}

func (s *server) chatStore(w http.ResponseWriter) (ChatStore, bool) {
	cs, ok := s.Inbox.(ChatStore)
	if !ok || s.Inbox == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the messaging inbox is not configured")
		return nil, false
	}
	return cs, true
}

type chatSiteBody struct {
	Name              *string          `json:"name"`
	Enabled           *bool            `json:"enabled"`
	AllowedOrigins    *[]string        `json:"allowedOrigins"`
	Greeting          *string          `json:"greeting"`
	Accent            *string          `json:"accent"`
	Position          *string          `json:"position"`
	Collect           *string          `json:"collect"`
	Hours             *messaging.Hours `json:"hours"`
	OfflineMode       *string          `json:"offlineMode"`
	PrivacyURL        *string          `json:"privacyUrl"`
	Challenge         *string          `json:"challenge"`
	PowBits           *int             `json:"powBits"`
	TurnstileSiteKey  *string          `json:"turnstileSiteKey"`
	TurnstileSecret   *string          `json:"turnstileSecret"`
	StripLinks        *bool            `json:"stripLinks"`
	MaxOpenUnassigned *int             `json:"maxOpenUnassigned"`
	Transcripts       *bool            `json:"transcripts"`
	RetentionDays     *int             `json:"retentionDays"`
}

func oneOf(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// input merges the body over the current site (zero for a new one, which
// starts from the defaults) and validates the result.
func (b chatSiteBody) input(create bool, cur store.ChatSite) (store.ChatSiteInput, fieldErrs) {
	var f fieldErrs
	in := store.ChatSiteInput{
		Name: cur.Name, Enabled: true, AllowedOrigins: cur.AllowedOrigins, Greeting: cur.Greeting, Accent: "#2563eb", Position: "right",
		Collect: "off", OfflineMode: "collect", Challenge: store.ChallengePoW, PowBits: 16, MaxOpenUnassigned: 200,
	}
	if !create {
		in = store.ChatSiteInput{
			Name: cur.Name, Enabled: cur.Enabled, AllowedOrigins: cur.AllowedOrigins, Greeting: cur.Greeting, Accent: cur.Accent,
			Position: cur.Position, Collect: cur.Collect, Hours: cur.Hours, OfflineMode: cur.OfflineMode, PrivacyURL: cur.PrivacyURL,
			Challenge: cur.Challenge, PowBits: cur.PowBits, TurnstileSiteKey: cur.TurnstileSiteKey, StripLinks: cur.StripLinks,
			MaxOpenUnassigned: cur.MaxOpenUnassigned, Transcripts: cur.Transcripts, RetentionDays: cur.RetentionDays,
		}
	}
	if b.Name != nil {
		in.Name = strings.TrimSpace(*b.Name)
	}
	if create && b.Name == nil {
		f.add("name", "is required")
	} else if in.Name == "" || !printable(in.Name, 100, true) {
		f.add("name", "must be 1-100 printable characters")
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	if b.AllowedOrigins != nil {
		seen := map[string]bool{}
		out := []string{}
		if len(*b.AllowedOrigins) > 20 {
			f.add("allowedOrigins", "at most 20 origins")
		}
		for _, o := range *b.AllowedOrigins {
			o = strings.TrimSpace(o)
			if !messaging.ValidOrigin(o) {
				f.add("allowedOrigins", "%q is not an origin (scheme://host[:port], nothing else)", o)
				continue
			}
			if u, err := url.Parse(o); err == nil {
				o = strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
			}
			if !seen[o] {
				seen[o] = true
				out = append(out, o)
			}
		}
		in.AllowedOrigins = out
	}
	if b.Greeting != nil {
		in.Greeting = strings.TrimSpace(*b.Greeting)
	}
	if !printable(in.Greeting, 500, true) {
		f.add("greeting", "must be at most 500 printable characters")
	}
	if b.Accent != nil {
		in.Accent = *b.Accent
	}
	if !accentRE.MatchString(in.Accent) {
		f.add("accent", "must be a colour like #2563eb")
	}
	if b.Position != nil {
		in.Position = *b.Position
	}
	if !oneOf(in.Position, "left", "right") {
		f.add("position", "must be left or right")
	}
	if b.Collect != nil {
		in.Collect = *b.Collect
	}
	if !oneOf(in.Collect, "off", "optional", "required") {
		f.add("collect", "must be off, optional or required")
	}
	if b.Hours != nil {
		in.Hours = *b.Hours
	}
	if err := in.Hours.Validate(); err != nil {
		f.add("hours", "%s", err.Error())
	}
	if b.OfflineMode != nil {
		in.OfflineMode = *b.OfflineMode
	}
	if !oneOf(in.OfflineMode, "collect", "hidden") {
		f.add("offlineMode", "must be collect or hidden")
	}
	if b.PrivacyURL != nil {
		in.PrivacyURL = strings.TrimSpace(*b.PrivacyURL)
	}
	if in.PrivacyURL != "" {
		u, err := url.Parse(in.PrivacyURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || len(in.PrivacyURL) > 500 || !printable(in.PrivacyURL, 500, false) {
			f.add("privacyUrl", "must be an http or https URL of at most 500 characters")
		}
	}
	if b.Challenge != nil {
		in.Challenge = *b.Challenge
	}
	if !oneOf(in.Challenge, store.ChallengePoW, store.ChallengeTurnstile, store.ChallengeNone) {
		f.add("challenge", "must be pow, turnstile or none")
	}
	if b.PowBits != nil {
		in.PowBits = *b.PowBits
	}
	if in.PowBits < 14 || in.PowBits > 22 {
		f.add("powBits", "must be between 14 and 22")
	}
	if b.TurnstileSiteKey != nil {
		in.TurnstileSiteKey = strings.TrimSpace(*b.TurnstileSiteKey)
	}
	if !printable(in.TurnstileSiteKey, 100, false) {
		f.add("turnstileSiteKey", "must be at most 100 printable characters without spaces")
	}
	if b.TurnstileSecret != nil {
		sec := strings.TrimSpace(*b.TurnstileSecret)
		if !printable(sec, 200, false) {
			f.add("turnstileSecret", "must be at most 200 printable characters without spaces")
		}
		in.TurnstileSecret = &sec
	}
	if b.StripLinks != nil {
		in.StripLinks = *b.StripLinks
	}
	if b.MaxOpenUnassigned != nil {
		in.MaxOpenUnassigned = *b.MaxOpenUnassigned
	}
	if in.MaxOpenUnassigned < 1 || in.MaxOpenUnassigned > 100000 {
		f.add("maxOpenUnassigned", "must be between 1 and 100000")
	}
	if b.Transcripts != nil {
		in.Transcripts = *b.Transcripts
	}
	if b.RetentionDays != nil {
		switch d := *b.RetentionDays; {
		case d == 0:
			in.RetentionDays = nil // follow the messaging settings
		case d < 1 || d > 3650:
			f.add("retentionDays", "must be 0 (follow the settings) or between 1 and 3650")
		default:
			in.RetentionDays = &d
		}
	}
	return in, f
}

func (s *server) listChatSites(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.chatStore(w)
	if !ok {
		return
	}
	sites, err := cs.ListChatSites(r.Context())
	if err != nil {
		s.internal(w, "list chat sites", err)
		return
	}
	out := make([]chatSiteView, 0, len(sites))
	for _, c := range sites {
		out = append(out, viewChatSite(c))
	}
	writeJSON(w, http.StatusOK, items(out))
}

func (s *server) createChatSite(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.chatStore(w)
	if !ok {
		return
	}
	var b chatSiteBody
	if !decode(w, r, &b) {
		return
	}
	in, f := b.input(true, store.ChatSite{})
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	c, err := cs.CreateChatSite(r.Context(), actor(r).String(), in)
	if err != nil {
		s.inboxError(w, "chat site", err)
		return
	}
	writeJSON(w, http.StatusCreated, viewChatSite(c))
}

func (s *server) getChatSite(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.chatStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := cs.GetChatSite(r.Context(), id)
	if err != nil {
		s.storeError(w, "chat site", err)
		return
	}
	writeJSON(w, http.StatusOK, viewChatSite(c))
}

func (s *server) putChatSite(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.chatStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b chatSiteBody
	if !decode(w, r, &b) {
		return
	}
	cur, err := cs.GetChatSite(r.Context(), id)
	if err != nil {
		s.storeError(w, "chat site", err)
		return
	}
	in, f := b.input(false, cur)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	c, err := cs.UpdateChatSite(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.inboxError(w, "chat site", err)
		return
	}
	writeJSON(w, http.StatusOK, viewChatSite(c))
}

func (s *server) deleteChatSite(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.chatStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := cs.DeleteChatSite(r.Context(), actor(r).String(), id); err != nil {
		s.inboxError(w, "chat site", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listChatBlocks(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.chatStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	bs, err := cs.ListChatBlocks(r.Context(), id)
	if err != nil {
		s.storeError(w, "chat site", err)
		return
	}
	writeJSON(w, http.StatusOK, items(bs))
}

func (s *server) deleteChatBlock(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.chatStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := cs.DeleteChatBlock(r.Context(), actor(r).String(), id); err != nil {
		s.storeError(w, "chat block", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The agent's chat actions.

// chatCaller is inboxCaller for the chat actions.
func (s *server) chatCaller(w http.ResponseWriter, r *http.Request, all bool) (ChatStore, store.InboxAccess, bool) {
	_, acc, ok := s.inboxCaller(w, r, all, true)
	if !ok {
		return nil, acc, false
	}
	cs, ok := s.chatStore(w)
	return cs, acc, ok
}

type chatBlockBody struct {
	// Hours is how long the block holds (default 24, at most 720: 30 days).
	Hours int `json:"hours"`
}

func (s *server) blockChatVisitorCommon(w http.ResponseWriter, r *http.Request, all bool) {
	cs, acc, ok := s.chatCaller(w, r, all)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	var b chatBlockBody
	if !decode(w, r, &b) {
		return
	}
	if b.Hours == 0 {
		b.Hours = 24
	}
	if b.Hours < 1 || b.Hours > 720 {
		writeFields(w, fieldErrs{{Path: "hours", Message: "must be between 1 and 720"}})
		return
	}
	blk, err := cs.BlockChatVisitor(r.Context(), acc, id, time.Duration(b.Hours)*time.Hour)
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, blk)
}

func (s *server) chatTypingCommon(w http.ResponseWriter, r *http.Request, all bool) {
	cs, acc, ok := s.chatCaller(w, r, all)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	if err := cs.ChatAgentTyping(r.Context(), acc, id); err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// emailSettings returns the SMTP settings in force; ok is false when email is
// not configured or unusable.
func (s *server) emailSettings(ctx context.Context) (mailer.Settings, bool, error) {
	st, ok := s.Store.(SMTPStore)
	if !ok {
		return mailer.Settings{}, false, nil
	}
	cfg, source, err := EffectiveSMTP(ctx, st, s.SMTPEnv)
	if err != nil {
		return mailer.Settings{}, false, err
	}
	if source == SMTPNone || cfg.Host == "" || cfg.Validate() != nil {
		return mailer.Settings{}, false, nil
	}
	return cfg, true, nil
}

type chatEmailBody struct {
	Body        string `json:"body"`
	ClientMsgID string `json:"clientMsgId"`
}

// emailChatReplyCommon answers an offline chat by email: the text goes to the
// address the visitor left and into the conversation, and the visitor sees it
// in the widget when they return. The email holds the reply and no link.
func (s *server) emailChatReplyCommon(w http.ResponseWriter, r *http.Request, all bool) {
	cs, acc, ok := s.chatCaller(w, r, all)
	if !ok {
		return
	}
	id, ok := convID(w, r)
	if !ok {
		return
	}
	var b chatEmailBody
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
	cfg, ready, err := s.emailSettings(r.Context())
	if err != nil {
		s.internal(w, "email reply: read settings", err)
		return
	}
	if !ready {
		writeError(w, http.StatusConflict, "not_configured", "email is not configured: set it up under Settings, Email")
		return
	}
	rep, created, err := cs.PrepareChatEmailReply(r.Context(), acc, store.SendInput{ConversationID: id, Body: body, ClientMsgID: b.ClientMsgID})
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	if !created {
		writeJSON(w, http.StatusOK, rep.Message)
		return
	}
	msg, err := mailer.BuildNotice(cfg.From, rep.To, "Re: your message to "+rep.SiteName,
		body+"\n\n--\n"+rep.SiteName+"\nThis is a reply to the message you left on our website.\n", time.Now())
	if err == nil {
		deliver := s.SMTPDeliver
		if deliver == nil {
			deliver = mailer.Deliver
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		err = deliver(ctx, cfg, rep.To, msg)
	}
	if ferr := cs.FinishChatEmailReply(context.WithoutCancel(r.Context()), rep.Message.ID, err == nil); ferr != nil {
		s.Log.Error("email reply: recording the result", "error", ferr)
	}
	if err != nil {
		// The error is not logged: an SMTP refusal can name the address.
		s.Log.Warn("email reply failed", "conversation", id)
		writeError(w, http.StatusBadGateway, "smtp_failed", "the email could not be sent; the reply was kept in the conversation as failed")
		return
	}
	rep.Message.Status = messaging.DeliverySent
	writeJSON(w, http.StatusCreated, rep.Message)
}

func (s *server) blockChatVisitor(w http.ResponseWriter, r *http.Request) {
	s.blockChatVisitorCommon(w, r, true)
}
func (s *server) blockMyChatVisitor(w http.ResponseWriter, r *http.Request) {
	s.blockChatVisitorCommon(w, r, false)
}
func (s *server) chatTyping(w http.ResponseWriter, r *http.Request) { s.chatTypingCommon(w, r, true) }
func (s *server) myChatTyping(w http.ResponseWriter, r *http.Request) {
	s.chatTypingCommon(w, r, false)
}
func (s *server) emailChatReply(w http.ResponseWriter, r *http.Request) {
	s.emailChatReplyCommon(w, r, true)
}
func (s *server) emailMyChatReply(w http.ResponseWriter, r *http.Request) {
	s.emailChatReplyCommon(w, r, false)
}
