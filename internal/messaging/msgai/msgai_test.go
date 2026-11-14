package msgai

import (
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
)

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"call me on +32 475 12 34 56 please":  "call me on [phone] please",
		"my number is 0475/123.456":           "my number is [phone]",
		"write to jan.peeters@example.be now": "write to [email] now",
		"order 123456 is late":                "order 123456 is late", // six digits stay
		"order 1234567 is late":               "order [phone] is late",
		"(02) 555-1234":                       "([phone]",
		"no digits here":                      "no digits here",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWindow(t *testing.T) {
	var lines []Line
	for i := 0; i < 30; i++ {
		lines = append(lines, Line{From: "customer", Text: strings.Repeat("x", 10)})
	}
	if got := Window(lines); len(got) != MaxContextMessages {
		t.Fatalf("%d lines, want %d", len(got), MaxContextMessages)
	}
	big := []Line{{From: "customer", Text: strings.Repeat("a", 5000)}, {From: "agent", Text: strings.Repeat("b", 5000)}}
	got := Window(big)
	if len(got) != 1 || got[0].From != "agent" {
		t.Fatalf("the oldest line was not dropped: %d lines", len(got))
	}
	huge := Window([]Line{{From: "customer", Text: strings.Repeat("é", 6000)}})
	if len(huge[0].Text) > MaxContextBytes || !strings.HasPrefix(huge[0].Text, "é") {
		t.Fatalf("a single long line was not cut on a rune: %d bytes", len(huge[0].Text))
	}
	// The input is not changed.
	if len(big[0].Text) != 5000 {
		t.Fatal("Window changed its input")
	}
}

func TestHandoffWords(t *testing.T) {
	for body, want := range map[string]bool{
		"Can I talk to a human?":          true,
		"AGENT":                           true,
		"I want a real person please":     true,
		"my agency sent me":               false,
		"what are your opening hours":     false,
		"I'd like a sales representative": true,
	} {
		if got := HasHandoffWord(body, nil); got != want {
			t.Errorf("HasHandoffWord(%q) = %v", body, got)
		}
	}
	if !HasHandoffWord("put me through to customer care", []string{"customer care"}) {
		t.Error("a configured phrase was missed")
	}
}

func TestURLsInFAQ(t *testing.T) {
	faq := []FAQ{{ID: 1, Title: "Hours", Body: "We are open 9 to 5. See https://example.com/hours for holidays."}}
	if !URLsInFAQ("Open 9 to 5, see https://example.com/hours.", faq) {
		t.Error("a link from the FAQ was refused")
	}
	if URLsInFAQ("See https://evil.example/phish", faq) {
		t.Error("a link not in the FAQ was accepted")
	}
	if URLsInFAQ("See https://example.com/hours?x=1", faq) {
		t.Error("a changed link was accepted")
	}
	if !URLsInFAQ("no links", faq) {
		t.Error("a reply with no link was refused")
	}
}

func TestCleanLabels(t *testing.T) {
	got, ok := CleanLabels([]string{" Billing ", "billing", "support"})
	if !ok || len(got) != 2 || got[0] != "billing" {
		t.Fatalf("CleanLabels = %v, %v", got, ok)
	}
	for _, bad := range [][]string{{""}, {"<script>"}, {strings.Repeat("x", 41)}, {"a\x01"}} {
		if _, ok := CleanLabels(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCheckFAQ(t *testing.T) {
	if f, _ := CheckFAQ("Opening hours", "9 to 5, https://example.com/x"); f != "" {
		t.Fatalf("a good snippet refused on %s", f)
	}
	if f, _ := CheckFAQ("", "x"); f != "title" {
		t.Error("an empty title passed")
	}
	if f, _ := CheckFAQ("two\nlines", "x"); f != "title" {
		t.Error("a two-line title passed")
	}
	if f, _ := CheckFAQ("t", strings.Repeat("x", MaxFAQBody+1)); f != "body" {
		t.Error("a long body passed")
	}
	if f, _ := CheckFAQ("t", "bad\x00body"); f != "body" {
		t.Error("a control character passed")
	}
}

func TestLookupFAQ(t *testing.T) {
	faq := []FAQ{
		{ID: 1, Title: "Opening hours", Body: "We are open Monday to Friday."},
		{ID: 2, Title: "Parking", Body: "Free parking behind the building."},
		{ID: 3, Title: "Holidays", Body: "Closed on public holidays; opening hours vary."},
	}
	got := LookupFAQ(faq, "what are your opening hours", 5)
	if len(got) != 2 || got[0].ID != 1 && got[0].ID != 3 {
		t.Fatalf("LookupFAQ = %+v", got)
	}
	if got := LookupFAQ(faq, "xyz", 5); len(got) != 0 {
		t.Fatalf("an unrelated query found %+v", got)
	}
}

// autoCtx is a context in which an auto-reply may be attempted.
func autoCtx() Context {
	return Context{
		Settings:  Settings{Enabled: true, AutoEnabled: true, Intents: DefaultIntents},
		Address:   AddressAI{Mode: ModeAuto, MaxReplies: 3, HourlyCap: 60, MinConfidence: 0.8, Disclosure: true, DisclosureText: "Automated reply."},
		ChannelOn: true, ChannelKind: messaging.KindSMS, Fresh: true,
		Body:   "When are you open?",
		FAQ:    []FAQ{{ID: 7, Title: "Hours", Body: "Open 9 to 5. https://example.com/hours"}},
		Triage: &Triage{Intent: "question", Urgency: UrgencyNormal, Sentiment: SentimentNeutral, Confidence: 0.9},
	}
}

// TestPreCheck is the deterministic half of TestAutoReplyGuardrails: it
// fails if an auto-reply is attempted after the cap, after a handoff word,
// to an opted-out peer, outside a WhatsApp window, within 10 minutes of a
// human reply, on spam, on a sensitive, urgent or negative text, on an
// intent the number does not allow, with low confidence, without a FAQ, or
// with any kill switch off.
func TestPreCheck(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-5 * time.Minute)
	old := now.Add(-25 * time.Hour)
	if _, ok := PreCheck(autoCtx(), true, now); !ok {
		t.Fatal("a clean context was refused")
	}
	cases := map[string]struct {
		mut     func(*Context)
		auto    bool
		handoff string
		skip    string
	}{
		"env switch":      {func(*Context) {}, false, "", ReasonOff},
		"global switch":   {func(c *Context) { c.Settings.Enabled = false }, true, "", ReasonOff},
		"auto switch":     {func(c *Context) { c.Settings.AutoEnabled = false }, true, "", ReasonOff},
		"channel switch":  {func(c *Context) { c.ChannelOn = false }, true, "", ReasonOff},
		"draft mode":      {func(c *Context) { c.Address.Mode = ModeDraft }, true, "", ReasonOff},
		"stale":           {func(c *Context) { c.Fresh = false }, true, "", ReasonStale},
		"stop":            {func(c *Context) { c.Body = "STOP" }, true, "", ReasonKeyword},
		"help":            {func(c *Context) { c.Body = "help" }, true, "", ReasonKeyword},
		"opted out":       {func(c *Context) { c.OptedOut = true }, true, "", ReasonOptedOut},
		"spam flag":       {func(c *Context) { c.Spam = true }, true, "", ReasonSpam},
		"spam triage":     {func(c *Context) { c.Triage.Spam = true }, true, "", ReasonSpam},
		"whatsapp closed": {func(c *Context) { c.ChannelKind = messaging.KindWhats; c.LastInboundAt = &old }, true, "", ReasonWindowClosed},
		"human replied":   {func(c *Context) { c.LastHumanReplyAt = &recent }, true, "", ReasonHumanActive},
		"handoff word":    {func(c *Context) { c.Body = "I want a human" }, true, ReasonHandoffWord, ""},
		"cap":             {func(c *Context) { c.AIRepliesSinceHuman = 3 }, true, ReasonCap, ""},
		"hourly cap":      {func(c *Context) { c.AISentLastHour = 60 }, true, ReasonRate, ""},
		"no triage":       {func(c *Context) { c.Triage = nil }, true, ReasonNoTriage, ""},
		"urgent":          {func(c *Context) { c.Triage.Urgency = UrgencyHigh }, true, ReasonUrgent, ""},
		"negative":        {func(c *Context) { c.Triage.Sentiment = SentimentNegative }, true, ReasonNegative, ""},
		"sensitive":       {func(c *Context) { c.Triage.Sensitive = true }, true, ReasonSensitive, ""},
		"other intent":    {func(c *Context) { c.Triage.Intent = IntentOther }, true, ReasonIntent, ""},
		"intent not on":   {func(c *Context) { c.Address.AutoIntents = []string{"sales"} }, true, ReasonIntent, ""},
		"low confidence":  {func(c *Context) { c.Triage.Confidence = 0.5 }, true, ReasonConfidence, ""},
		"no faq":          {func(c *Context) { c.FAQ = nil }, true, ReasonNoFAQ, ""},
	}
	for name, tc := range cases {
		c := autoCtx()
		tr := *c.Triage
		c.Triage = &tr
		tc.mut(&c)
		d, ok := PreCheck(c, tc.auto, now)
		if ok {
			t.Errorf("%s: attempted", name)
			continue
		}
		if d.Handoff != (tc.handoff != "") || d.Reason != tc.handoff || d.Skip != tc.skip || d.Send {
			t.Errorf("%s: decision %+v, want handoff %q skip %q", name, d, tc.handoff, tc.skip)
		}
	}
	// A WhatsApp customer who wrote within the window may be answered; an
	// old human reply does not keep the AI quiet.
	c := autoCtx()
	c.ChannelKind = messaging.KindWhats
	c.LastInboundAt = &recent
	long := now.Add(-11 * time.Minute)
	c.LastHumanReplyAt = &long
	if _, ok := PreCheck(c, true, now); !ok {
		t.Error("an open window or an old human reply refused the AI")
	}
}

// TestCheckReply is the half of TestAutoReplyGuardrails that judges the
// model's answer: it fails if a reply is sent without a supporting snippet,
// with a snippet of another number, with a URL not in a snippet, with a
// placeholder, with low confidence or after the model handed off, or if the
// disclosure line is missing from the first AI reply when it is on.
func TestCheckReply(t *testing.T) {
	c := autoCtx()
	good := AutoReply{Reply: "We are open 9 to 5: https://example.com/hours", FAQIDs: []int64{7}, Confidence: 0.95}
	d := CheckReply(c, good, false)
	if !d.Send || !strings.HasPrefix(d.Text, "Automated reply.\n") || !strings.Contains(d.Text, "9 to 5") {
		t.Fatalf("a good reply: %+v", d)
	}
	c.AIRepliesTotal = 1
	if d := CheckReply(c, good, false); !d.Send || strings.HasPrefix(d.Text, "Automated reply.") {
		t.Fatalf("the disclosure led a later reply: %+v", d)
	}
	c.AIRepliesTotal = 0
	c.Address.Disclosure = false
	if d := CheckReply(c, good, false); strings.HasPrefix(d.Text, "Automated reply.") {
		t.Fatal("the disclosure led a reply with disclosure off")
	}
	for name, tc := range map[string]struct {
		out    AutoReply
		tool   bool
		reason string
	}{
		"tool handoff":    {good, true, ReasonModel},
		"model handoff":   {AutoReply{Handoff: true, Confidence: 1}, false, ReasonModel},
		"no snippet":      {AutoReply{Reply: "Open 9 to 5", Confidence: 0.9}, false, ReasonNotGrounded},
		"foreign snippet": {AutoReply{Reply: "Open 9 to 5", FAQIDs: []int64{8}, Confidence: 0.9}, false, ReasonNotGrounded},
		"foreign url":     {AutoReply{Reply: "See https://evil.example", FAQIDs: []int64{7}, Confidence: 0.9}, false, ReasonURL},
		"placeholder":     {AutoReply{Reply: "We will call [phone]", FAQIDs: []int64{7}, Confidence: 0.9}, false, ReasonPlaceholder},
		"low confidence":  {AutoReply{Reply: "Open 9 to 5", FAQIDs: []int64{7}, Confidence: 0.3}, false, ReasonConfidence},
		"empty":           {AutoReply{Reply: "  ", FAQIDs: []int64{7}, Confidence: 0.9}, false, ReasonInvalid},
		"too long":        {AutoReply{Reply: strings.Repeat("x", MaxReply+1), FAQIDs: []int64{7}, Confidence: 0.9}, false, ReasonInvalid},
	} {
		d := CheckReply(autoCtx(), tc.out, tc.tool)
		if d.Send || !d.Handoff || d.Reason != tc.reason {
			t.Errorf("%s: %+v, want a handoff for %s", name, d, tc.reason)
		}
	}
}
