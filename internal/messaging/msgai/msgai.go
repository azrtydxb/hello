// Package msgai is the AI of the unified inbox (spec messaging-channels M-30,
// M-30a): triage (classify a customer's text and suggest tags, priority and
// a queue), reply drafts an agent must click to use, and auto-replies from a
// number's FAQ behind guardrails.
//
// Every model call is ai.Generate (internal/ai, go-ai-sdk): this package
// builds no model and no HTTP client. The model sees one conversation (its
// last messages, capped), the number's FAQ, the business name and the
// labels it may pick from, as untrusted data; it has two tools, both
// without effect outside the call: lookup_faq reads the number's FAQ and
// handoff asks for a person.
//
// Message text is customer data: nothing here logs it.
package msgai

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/messaging"
)

// Modes of a number.
const (
	ModeOff    = "off"
	ModeTriage = "triage"
	ModeDraft  = "draft"
	ModeAuto   = "auto"
)

// Modes lists the modes in the order a console shows them.
var Modes = []string{ModeOff, ModeTriage, ModeDraft, ModeAuto}

// ValidMode reports whether m is a mode.
func ValidMode(m string) bool { return slices.Contains(Modes, m) }

// Kinds of run, and the feature names Generate reports them under.
const (
	KindTriage    = "triage"
	KindDraft     = "draft"
	KindAutoReply = "autoreply"

	FeatureTriage    = "message_triage"
	FeatureDraft     = "message_draft"
	FeatureAutoReply = "message_autoreply"
)

// Run states.
const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
	StateSkipped = "skipped"
	StateStale   = "stale"
)

// Limits.
const (
	// MaxFAQ is the most FAQ snippets a number holds, MaxFAQBody and
	// MaxFAQTitle their sizes.
	MaxFAQ      = 50
	MaxFAQBody  = 4096
	MaxFAQTitle = 200
	// MaxContextMessages and MaxContextBytes bound the conversation the
	// model sees: its last messages, the oldest dropped first.
	MaxContextMessages = 20
	MaxContextBytes    = 8192
	// MaxSummary is the longest triage summary.
	MaxSummary = 280
	// MaxReply is the longest draft or auto-reply.
	MaxReply = 1000
	// MaxSuggestedTags is the most tags one triage suggests.
	MaxSuggestedTags = 5
	// HumanQuiet is how long after a person's reply the AI stays quiet.
	HumanQuiet = 10 * time.Minute
	// WhatsAppWindow is the customer-care window of WhatsApp: no reply,
	// AI or person, after it closes.
	WhatsAppWindow = 24 * time.Hour
	// MaxLabels and MaxLabelLen bound the lists an administrator writes
	// (intents, sensitive topics, handoff words, allowed intents).
	MaxLabels   = 30
	MaxLabelLen = 40
	// MaxDisclosure and MaxHolding bound the two automatic texts.
	MaxDisclosure = 160
	MaxHolding    = 480
)

// Triage values.
const (
	IntentOther = "other"

	UrgencyLow    = "low"
	UrgencyNormal = "normal"
	UrgencyHigh   = "high"

	SentimentPositive = "positive"
	SentimentNeutral  = "neutral"
	SentimentNegative = "negative"
)

// Defaults of the lists an administrator may change.
var (
	DefaultHandoffWords    = []string{"agent", "human", "person", "representative"}
	DefaultIntents         = []string{"question", "sales", "support", "billing", "appointment", "complaint"}
	DefaultSensitiveTopics = []string{"medical", "legal", "emergency", "self-harm", "harassment", "payment dispute", "personal data"}
)

// Triage is the model's reading of a customer's text (spec M-30).
type Triage struct {
	Language           string   `json:"language" jsonschema:"the language of the customer's last text, as an ISO 639-1 code such as en, nl or fr"`
	Intent             string   `json:"intent" jsonschema:"one of the intents in the data block, or other"`
	Urgency            string   `json:"urgency" jsonschema:"low, normal or high"`
	Sentiment          string   `json:"sentiment" jsonschema:"positive, neutral or negative"`
	Spam               bool     `json:"spam" jsonschema:"true only for unsolicited bulk or fraudulent text"`
	Sensitive          bool     `json:"sensitive" jsonschema:"true when the text touches one of the sensitive topics in the data block"`
	Confidence         float64  `json:"confidence" jsonschema:"how sure you are of this reading, 0 to 1"`
	Summary            string   `json:"summary" jsonschema:"one sentence for the agent, at most 280 characters"`
	SuggestedQueue     string   `json:"suggestedQueue" jsonschema:"the name of one queue in the data block that fits, or an empty string"`
	SuggestedTags      []string `json:"suggestedTags" jsonschema:"at most 5 short lower-case tags"`
	HandoffRecommended bool     `json:"handoffRecommended" jsonschema:"true when a person should answer"`
}

// Draft is a suggested reply for an agent.
type Draft struct {
	Reply string `json:"reply" jsonschema:"the reply text the agent may send, in the customer's language"`
}

// AutoReply is the model's answer for an auto-reply.
type AutoReply struct {
	Reply         string  `json:"reply" jsonschema:"the reply to send, in the customer's language; empty when handing off"`
	FAQIDs        []int64 `json:"faqIds" jsonschema:"the ids of the FAQ snippets the reply is based on"`
	Handoff       bool    `json:"handoff" jsonschema:"true when a person must answer"`
	HandoffReason string  `json:"handoffReason" jsonschema:"why a person must answer, a few words; empty otherwise"`
	Confidence    float64 `json:"confidence" jsonschema:"how sure you are that the snippets answer the question, 0 to 1"`
}

// FAQ is one snippet of a number's FAQ.
type FAQ struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Line is one message of the conversation as the model sees it.
type Line struct {
	// From is customer, agent or assistant (an earlier AI reply).
	From string `json:"from"`
	Text string `json:"text"`
}

// Settings are the inbox-wide AI settings.
type Settings struct {
	// Enabled is the global kill switch of the inbox AI, AutoEnabled that of
	// auto-replies.
	Enabled         bool
	AutoEnabled     bool
	BusinessName    string
	Intents         []string
	SensitiveTopics []string
}

// AddressAI is the AI of one number or chat site.
type AddressAI struct {
	Mode           string
	Redact         bool
	Disclosure     bool
	DisclosureText string
	HoldingText    string
	MaxReplies     int
	HourlyCap      int
	MinConfidence  float64
	AutoIntents    []string
	HandoffWords   []string
	OptOutWords    []string
}

// Job is a claimed run.
type Job struct {
	RunID          int64
	MessageID      int64
	ConversationID int64
	Kind           string
}

// Context is everything a run needs, read by the store when it starts.
type Context struct {
	Job
	Settings  Settings
	Address   AddressAI
	ChannelOn bool
	// ChannelKind is sms, whatsapp or webchat.
	ChannelKind string
	// Fresh is false once a newer customer text or a person's reply follows
	// the message: the run's result would be stale.
	Fresh bool
	// Body is the customer's text the run is for.
	Body string
	// FirstName is the customer's first name from a shared book or the chat
	// visitor's name; empty when unknown.
	FirstName string
	// Lines are the conversation's last messages, oldest first, as stored.
	Lines  []Line
	FAQ    []FAQ
	Queues []string
	// Triage is this message's triage, nil when it failed or was not run.
	Triage *Triage
	// Guard facts for auto-replies.
	OptedOut             bool
	Spam                 bool
	LastHumanReplyAt     *time.Time
	LastInboundAt        *time.Time
	AIRepliesSinceHuman  int
	AIRepliesTotal       int
	AISentLastHour       int
	HoldingSinceHuman    bool
	ConversationAssigned bool
}

// Result is what a run produced.
type Result struct {
	State string
	// Error is a code (ai.CodeOf, or a reason of this package), never text.
	Error  string
	Triage *Triage
	Draft  *Draft
	Reply  *Decision
}

// Decision is what an auto-reply run decided.
type Decision struct {
	// Send is true when Text goes to the customer.
	Send bool
	Text string
	// FAQIDs are the snippets the reply rests on.
	FAQIDs []int64
	// Handoff flags the conversation for a person with Reason (a code from
	// the list below).
	Handoff bool
	Reason  string
	// Skip is set when the AI stays quiet without a handoff (a person
	// answers already, the customer opted out, spam, the window closed, a
	// switch is off); it is the reason.
	Skip string
}

// Reasons an auto-reply hands off or stays quiet.
const (
	ReasonHandoffWord  = "handoff_word"
	ReasonKeyword      = "keyword"
	ReasonUrgent       = "urgent"
	ReasonNegative     = "negative"
	ReasonSensitive    = "sensitive"
	ReasonIntent       = "intent"
	ReasonConfidence   = "low_confidence"
	ReasonNoFAQ        = "no_faq"
	ReasonNotGrounded  = "not_grounded"
	ReasonURL          = "url_not_in_faq"
	ReasonPlaceholder  = "placeholder"
	ReasonInvalid      = "invalid_reply"
	ReasonModel        = "model_handoff"
	ReasonCap          = "reply_cap"
	ReasonRate         = "hourly_cap"
	ReasonNoTriage     = "no_triage"
	ReasonError        = "ai_error"
	ReasonOff          = "off"
	ReasonSpam         = "spam"
	ReasonOptedOut     = "opted_out"
	ReasonHumanActive  = "human_active"
	ReasonWindowClosed = "window_closed"
	ReasonStale        = "stale"
	ReasonAIOff        = "ai_off"
)

// ReasonText is how the inbox names a handoff reason in the thread.
func ReasonText(r string) string {
	switch r {
	case ReasonHandoffWord:
		return "the customer asked for a person"
	case ReasonUrgent:
		return "the text is urgent"
	case ReasonNegative:
		return "the customer is unhappy"
	case ReasonSensitive:
		return "a sensitive topic"
	case ReasonIntent:
		return "the AI may not answer this kind of question"
	case ReasonConfidence:
		return "the AI is not sure enough"
	case ReasonNoFAQ:
		return "the number has no FAQ"
	case ReasonNotGrounded, ReasonURL, ReasonPlaceholder, ReasonInvalid:
		return "the FAQ does not answer it"
	case ReasonModel:
		return "the AI asked for a person"
	case ReasonCap:
		return "the AI reply limit is reached"
	case ReasonRate:
		return "the hourly AI limit is reached"
	case ReasonNoTriage, ReasonError:
		return "the AI is unavailable"
	}
	return "a person should answer"
}

var (
	// emailRE finds email addresses.
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// phoneRE finds runs of at least 7 digits, allowing the separators
	// people type in numbers (space, dot, dash, slash, brackets) and a
	// leading +.
	phoneRE = regexp.MustCompile(`\+?\d(?:[\s.\-/()]*\d){6,}`)
	// urlRE finds the links in a reply.
	urlRE = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>"'()]+`)
)

// Placeholders Redact writes.
const (
	PlaceholderPhone = "[phone]"
	PlaceholderEmail = "[email]"
)

// Redact replaces email addresses and phone-number-like digit runs of 7 or
// more digits with placeholders (spec M-30, ai_redact).
func Redact(s string) string {
	s = emailRE.ReplaceAllString(s, PlaceholderEmail)
	return phoneRE.ReplaceAllString(s, PlaceholderPhone)
}

// Window cuts lines to what the model may see: the last
// MaxContextMessages, then the oldest dropped until the texts fit in
// MaxContextBytes; a single line longer than that is cut to fit.
func Window(lines []Line) []Line {
	if len(lines) > MaxContextMessages {
		lines = lines[len(lines)-MaxContextMessages:]
	}
	total := 0
	for _, l := range lines {
		total += len(l.Text)
	}
	for len(lines) > 1 && total > MaxContextBytes {
		total -= len(lines[0].Text)
		lines = lines[1:]
	}
	out := slices.Clone(lines)
	if len(out) == 1 && len(out[0].Text) > MaxContextBytes {
		out[0].Text = cut(out[0].Text, MaxContextBytes)
	}
	return out
}

// cut shortens s to at most n bytes on a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// words splits a text into lower-case words.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
}

// HasHandoffWord reports whether the text has one of the handoff words (or
// phrases) as a whole word.
func HasHandoffWord(body string, list []string) bool {
	if len(list) == 0 {
		list = DefaultHandoffWords
	}
	ws := " " + strings.Join(words(body), " ") + " "
	for _, h := range list {
		h = strings.Join(words(h), " ")
		if h != "" && strings.Contains(ws, " "+h+" ") {
			return true
		}
	}
	return false
}

// URLs returns the links in a text.
func URLs(s string) []string {
	out := urlRE.FindAllString(s, -1)
	for i, u := range out {
		out[i] = strings.TrimRight(u, ".,;:!?")
	}
	return out
}

// URLsInFAQ reports whether every link of reply appears, exactly, in one of
// the snippets (spec M-30a).
func URLsInFAQ(reply string, faq []FAQ) bool {
	for _, u := range URLs(reply) {
		found := false
		for _, f := range faq {
			if strings.Contains(f.Body, u) || strings.Contains(f.Title, u) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// CleanLabels normalises a list an administrator wrote: trimmed, lower-case,
// printable, no duplicates. ok is false for an empty label, one longer than
// MaxLabelLen or more than MaxLabels.
func CleanLabels(in []string) (out []string, ok bool) {
	out = []string{}
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" || utf8.RuneCountInString(l) > MaxLabelLen || !utf8.ValidString(l) {
			return nil, false
		}
		for _, r := range l {
			if !unicode.IsPrint(r) || r == '<' || r == '>' {
				return nil, false
			}
		}
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out, len(out) <= MaxLabels
}

// intentAllowed reports whether the AI may answer an intent.
func intentAllowed(intent string, allowed []string) bool {
	if intent == IntentOther || intent == "" {
		return false
	}
	return len(allowed) == 0 || slices.Contains(allowed, intent)
}

// PreCheck decides, before any model call, whether an auto-reply may be
// attempted (spec M-30a). It returns the decision to apply without a call
// (a handoff or a skip) and ok false, or ok true to ask the model.
// autoAllowed is HELLO_MSG_AI_AUTO.
func PreCheck(c Context, autoAllowed bool, now time.Time) (Decision, bool) {
	switch {
	case !autoAllowed || !c.Settings.Enabled || !c.Settings.AutoEnabled || !c.ChannelOn || c.Address.Mode != ModeAuto:
		return Decision{Skip: ReasonOff}, false
	case !c.Fresh:
		return Decision{Skip: ReasonStale}, false
	case messaging.ClassifyKeyword(c.Body, c.Address.OptOutWords) != messaging.KeywordNone:
		// STOP, START and HELP are answered by the opt-out engine.
		return Decision{Skip: ReasonKeyword}, false
	case c.OptedOut:
		return Decision{Skip: ReasonOptedOut}, false
	case c.Spam || (c.Triage != nil && c.Triage.Spam):
		return Decision{Skip: ReasonSpam}, false
	case c.ChannelKind == messaging.KindWhats && (c.LastInboundAt == nil || now.Sub(*c.LastInboundAt) >= WhatsAppWindow):
		return Decision{Skip: ReasonWindowClosed}, false
	case c.LastHumanReplyAt != nil && now.Sub(*c.LastHumanReplyAt) < HumanQuiet:
		return Decision{Skip: ReasonHumanActive}, false
	case HasHandoffWord(c.Body, c.Address.HandoffWords):
		return Decision{Handoff: true, Reason: ReasonHandoffWord}, false
	case c.AIRepliesSinceHuman >= max(c.Address.MaxReplies, 1):
		return Decision{Handoff: true, Reason: ReasonCap}, false
	case c.AISentLastHour >= max(c.Address.HourlyCap, 1):
		return Decision{Handoff: true, Reason: ReasonRate}, false
	case c.Triage == nil:
		return Decision{Handoff: true, Reason: ReasonNoTriage}, false
	case c.Triage.Urgency == UrgencyHigh:
		return Decision{Handoff: true, Reason: ReasonUrgent}, false
	case c.Triage.Sentiment == SentimentNegative:
		return Decision{Handoff: true, Reason: ReasonNegative}, false
	case c.Triage.Sensitive:
		return Decision{Handoff: true, Reason: ReasonSensitive}, false
	case !intentAllowed(c.Triage.Intent, c.Address.AutoIntents):
		return Decision{Handoff: true, Reason: ReasonIntent}, false
	case c.Triage.Confidence < c.Address.MinConfidence:
		return Decision{Handoff: true, Reason: ReasonConfidence}, false
	case len(c.FAQ) == 0:
		return Decision{Handoff: true, Reason: ReasonNoFAQ}, false
	}
	return Decision{}, true
}

// CheckReply turns the model's answer into a decision (spec M-30a): a
// handoff (the model's own, or the tool's) stays one; a reply is sent only
// when it is confident, cites at least one of the number's snippets, has
// no link that is not in a snippet, holds no redaction placeholder and is
// valid text. The disclosure line leads the conversation's first AI reply.
func CheckReply(c Context, out AutoReply, toolHandoff bool) Decision {
	if out.Handoff || toolHandoff {
		return Decision{Handoff: true, Reason: ReasonModel}
	}
	if out.Confidence < c.Address.MinConfidence {
		return Decision{Handoff: true, Reason: ReasonConfidence}
	}
	if len(out.FAQIDs) == 0 {
		return Decision{Handoff: true, Reason: ReasonNotGrounded}
	}
	for _, id := range out.FAQIDs {
		if !slices.ContainsFunc(c.FAQ, func(f FAQ) bool { return f.ID == id }) {
			return Decision{Handoff: true, Reason: ReasonNotGrounded}
		}
	}
	text, ok := messaging.CleanBody(out.Reply, MaxReply)
	if !ok {
		return Decision{Handoff: true, Reason: ReasonInvalid}
	}
	if strings.Contains(text, PlaceholderPhone) || strings.Contains(text, PlaceholderEmail) {
		return Decision{Handoff: true, Reason: ReasonPlaceholder}
	}
	if !URLsInFAQ(text, c.FAQ) {
		return Decision{Handoff: true, Reason: ReasonURL}
	}
	if c.Address.Disclosure && c.AIRepliesTotal == 0 && strings.TrimSpace(c.Address.DisclosureText) != "" {
		text = strings.TrimSpace(c.Address.DisclosureText) + "\n" + text
	}
	return Decision{Send: true, Text: text, FAQIDs: slices.Clone(out.FAQIDs)}
}

// LookupFAQ ranks the snippets for a query: the ones sharing the most
// words with it first, at most n, none that shares no word.
func LookupFAQ(faq []FAQ, query string, n int) []FAQ {
	q := words(query)
	type scored struct {
		f FAQ
		n int
	}
	var hits []scored
	for _, f := range faq {
		ws := words(f.Title + " " + f.Body)
		score := 0
		for _, w := range q {
			if len(w) > 2 && slices.Contains(ws, w) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{f, score})
		}
	}
	slices.SortStableFunc(hits, func(a, b scored) int { return b.n - a.n })
	out := []FAQ{}
	for i := 0; i < len(hits) && i < n; i++ {
		out = append(out, hits[i].f)
	}
	return out
}

// validURL is used by the FAQ editor: a link in a snippet must parse.
func validURL(u string) bool {
	if strings.HasPrefix(strings.ToLower(u), "www.") {
		u = "https://" + u
	}
	p, err := url.Parse(u)
	return err == nil && p.Host != ""
}

// CheckFAQ validates a snippet; it returns the field and the problem, or
// "" when it is fine.
func CheckFAQ(title, body string) (field, problem string) {
	t, ok := messaging.CleanBody(title, MaxFAQTitle*4)
	if !ok || utf8.RuneCountInString(t) > MaxFAQTitle || strings.Contains(t, "\n") {
		return "title", "must be one line of 1 to 200 printable characters"
	}
	b, ok := messaging.CleanBody(body, MaxFAQBody)
	if !ok {
		return "body", "must be 1 to 4096 bytes of printable text"
	}
	for _, u := range URLs(b) {
		if !validURL(u) {
			return "body", "has a link that is not a valid URL"
		}
	}
	return "", ""
}
