package messaging_test

// The inbox AI against PostgreSQL and the scripted model endpoint (spec
// messaging-channels M-30, M-30a): what is queued, what the model sees,
// what is applied and what is sent. Every model call goes through the real
// provider path of internal/ai to test/fakellm; nothing reaches a real
// provider. Skipped without HELLO_TEST_DATABASE_URL.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/msgai"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/test/fakellm"
)

// aiRig is the inbox rig with a model endpoint and the AI worker.
type aiRig struct {
	*rig
	llm *fakellm.Server
	w   *msgai.Worker
}

func newAIRig(t *testing.T) *aiRig {
	t.Helper()
	r := &aiRig{rig: newRig(t), llm: fakellm.New(t)}
	cfg := aifake.Config()
	cfg.BaseURL = r.llm.URL
	cfg.ValidationAttempts = 1
	svc, err := ai.New(cfg, aifake.NewStore(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.w = &msgai.Worker{Store: r.st.MessageAI(), AI: svc, AutoAllowed: true, Metrics: msgai.NewMetrics(nil)}
	return r
}

// setAI sets the number's AI mode with the default guardrails.
func (r *aiRig) setAI(mode string, mut ...func(*store.MsgAddressAI)) {
	r.t.Helper()
	m := store.MsgAddressAI{AddressID: r.addr.ID, Mode: mode, Redact: true, Disclosure: true, DisclosureText: "Automated reply.",
		HoldingText: "A colleague will answer soon.", MaxReplies: 3, HourlyCap: 60, MinConfidence: 0.8,
		AutoIntents: []string{}, HandoffWords: msgai.DefaultHandoffWords}
	for _, f := range mut {
		f(&m)
	}
	if _, err := r.st.PutMsgAddressAI(context.Background(), "test", m); err != nil {
		r.t.Fatal(err)
	}
}

// drain runs the worker until nothing is queued.
func (r *aiRig) drain() {
	r.t.Helper()
	for i := 0; i < 50; i++ {
		ran, err := r.w.Once(context.Background())
		if err != nil {
			r.t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	r.t.Fatal("the AI queue does not drain")
}

func triageReply(intent, urgency, sentiment string, tags []string, queue string) fakellm.Reply {
	return fakellm.JSON(map[string]any{"language": "en", "intent": intent, "urgency": urgency, "sentiment": sentiment,
		"spam": false, "sensitive": false, "confidence": 0.9, "summary": "The customer asks something.",
		"suggestedQueue": queue, "suggestedTags": tags, "handoffRecommended": false})
}

func autoAnswer(reply string, faq int64) []fakellm.Reply {
	return []fakellm.Reply{
		{Content: "Snippet " + strconv.FormatInt(faq, 10) + " answers it."},
		fakellm.JSON(map[string]any{"reply": reply, "faqIds": []int64{faq}, "handoff": false, "handoffReason": "", "confidence": 0.95}),
	}
}

func (r *aiRig) runs(conv int64, kind string) []string {
	r.t.Helper()
	rows, err := r.db.QueryContext(context.Background(), `SELECT state || ':' || error FROM ai_message_runs WHERE conversation_id = $1 AND kind = $2 ORDER BY id`, conv, kind)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

var aiPeer int

func nextPeer() string {
	aiPeer++
	return fmt.Sprintf("+3247599%04d", aiPeer)
}

// TestMessagingAIUsesSDK (database half; the static half is in msgai) fails
// if triage runs with ai_mode=off, if the model receives more than the
// current conversation, if a message is delayed by an AI failure, or if a
// stale AI result is applied (spec M-30).
func TestMessagingAIUsesSDK(t *testing.T) {
	r := newAIRig(t)
	ctx := context.Background()

	t.Run("off queues nothing", func(t *testing.T) {
		conv := r.ingest(nextPeer(), "hello").ConversationID
		r.drain()
		if n := r.count(`SELECT count(*) FROM ai_message_runs WHERE conversation_id = $1`, conv); n != 0 {
			t.Fatalf("%d runs with ai_mode off", n)
		}
		if len(r.llm.Requests()) != 0 {
			t.Fatal("the model was called with ai_mode off")
		}
	})

	r.setAI(msgai.ModeDraft)
	other := r.ingest(nextPeer(), "SECRET-OTHER-CUSTOMER").ConversationID
	r.llm.Queue(msgai.FeatureTriage, fakellm.Reply{Status: 500}) // the other customer's run fails
	r.llm.Queue(msgai.FeatureDraft, fakellm.Reply{Status: 500})
	r.drain()

	t.Run("an AI failure delays nothing", func(t *testing.T) {
		got := r.runs(other, msgai.KindTriage)
		if len(got) != 1 || !strings.HasPrefix(got[0], "failed:") {
			t.Fatalf("triage runs = %v", got)
		}
		// The text is in the inbox, unread, whatever the AI did.
		c, err := r.st.GetConversation(ctx, r.admAcc(), other)
		if err != nil || c.Unread != 1 || c.AI.Triage != nil {
			t.Fatalf("conversation = %+v, %v", c, err)
		}
	})

	t.Run("triage and draft", func(t *testing.T) {
		before := len(r.llm.Requests())
		peer := nextPeer()
		conv := r.ingest(peer, "When are you open? My email is jan@example.be").ConversationID
		r.llm.Queue(msgai.FeatureTriage, triageReply("question", "high", "neutral", []string{"hours"}, ""))
		r.llm.Queue(msgai.FeatureDraft, fakellm.JSON(map[string]any{"reply": "We are open 9 to 5."}))
		r.drain()
		reqs := r.llm.Requests()[before:]
		if len(reqs) != 2 || reqs[0].Feature != msgai.FeatureTriage || reqs[1].Feature != msgai.FeatureDraft {
			t.Fatalf("requests = %d", len(reqs))
		}
		for _, q := range reqs {
			b, _ := json.Marshal(q.Body)
			if strings.Contains(string(b), "SECRET-OTHER-CUSTOMER") {
				t.Fatal("the model saw another customer's conversation")
			}
			if strings.Contains(string(b), "jan@example.be") {
				t.Fatal("an email address reached the model with redaction on")
			}
		}
		c, err := r.st.GetConversation(ctx, r.admAcc(), conv)
		if err != nil {
			t.Fatal(err)
		}
		if c.AI.Mode != msgai.ModeDraft || c.AI.Triage == nil || c.AI.Triage.Intent != "question" || c.Priority != "high" ||
			len(c.Tags) != 1 || c.Tags[0] != "hours" || c.AI.Applied.Priority != "normal" {
			t.Fatalf("triage not applied: %+v %+v", c.AI, c)
		}
		if c.AI.Draft == nil || c.AI.Draft.Text != "We are open 9 to 5." {
			t.Fatalf("draft = %+v", c.AI.Draft)
		}
		// Nothing went to the customer: a draft waits for an agent's click.
		if n := r.count(`SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND direction = 'out' AND kind = 'message'`, conv); n != 0 {
			t.Fatalf("%d messages sent in draft mode", n)
		}
		// An agent undoes the triage.
		c, err = r.st.RevertConversationAI(ctx, r.admAcc(), conv)
		if err != nil || c.Priority != "normal" || len(c.Tags) != 0 || !c.AI.Triage.Reverted {
			t.Fatalf("after revert = %+v, %v", c, err)
		}
		// A reply by the agent retires the draft.
		if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "Hi, 9 to 5."}); err != nil {
			t.Fatal(err)
		}
		if c, _ = r.st.GetConversation(ctx, r.admAcc(), conv); c.AI.Draft != nil {
			t.Fatal("the draft outlived the agent's reply")
		}
	})

	t.Run("a stale result is not applied", func(t *testing.T) {
		peer := nextPeer()
		conv := r.ingest(peer, "first question").ConversationID
		job, ok, err := r.st.MessageAI().ClaimAIRun(ctx)
		if err != nil || !ok || job.ConversationID != conv {
			t.Fatalf("claim = %+v, %v, %v", job, ok, err)
		}
		r.ingest(peer, "a newer question") // the conversation moves on
		tr := msgai.Triage{Language: "en", Intent: "question", Urgency: "high", Sentiment: "neutral", Confidence: 0.9,
			Summary: "old", SuggestedTags: []string{"old"}}
		if err := r.st.MessageAI().FinishAIRun(ctx, job, msgai.Result{State: msgai.StateDone, Triage: &tr}); err != nil {
			t.Fatal(err)
		}
		if got := r.runs(conv, msgai.KindTriage); len(got) != 2 || got[0] != "stale:" {
			t.Fatalf("runs = %v", got)
		}
		c, _ := r.st.GetConversation(ctx, r.admAcc(), conv)
		if c.AI.Triage != nil || c.Priority != "normal" || len(c.Tags) != 0 {
			t.Fatalf("a stale triage was applied: %+v", c)
		}
		// The newer text's runs go on.
		r.llm.Queue(msgai.FeatureTriage, triageReply("question", "normal", "neutral", nil, ""))
		r.llm.Queue(msgai.FeatureDraft, fakellm.JSON(map[string]any{"reply": "Hello."}))
		r.drain()
	})

	t.Run("kill switches", func(t *testing.T) {
		if _, err := r.st.PutMessagingAI(ctx, "test", store.MessagingAIInput{Enabled: true, AutoEnabled: true,
			Intents: msgai.DefaultIntents, SensitiveTopics: msgai.DefaultSensitiveTopics, Channels: map[int64]bool{r.channel.ID: false}}); err != nil {
			t.Fatal(err)
		}
		conv := r.ingest(nextPeer(), "hello").ConversationID
		if n := r.count(`SELECT count(*) FROM ai_message_runs WHERE conversation_id = $1`, conv); n != 0 {
			t.Fatal("a run was queued on a channel with its AI off")
		}
		if n := r.count(`SELECT count(*) FROM audit_events WHERE action = 'ai_disabled' AND resource = 'msg_channel'`); n != 1 {
			t.Fatalf("%d audit rows for the channel switch", n)
		}
		if _, err := r.st.PutMessagingAI(ctx, "test", store.MessagingAIInput{Enabled: false, AutoEnabled: true,
			Intents: msgai.DefaultIntents, SensitiveTopics: msgai.DefaultSensitiveTopics, Channels: map[int64]bool{r.channel.ID: true}}); err != nil {
			t.Fatal(err)
		}
		conv = r.ingest(nextPeer(), "hello").ConversationID
		if n := r.count(`SELECT count(*) FROM ai_message_runs WHERE conversation_id = $1`, conv); n != 0 {
			t.Fatal("a run was queued with the global switch off")
		}
	})
}

// TestAutoReplyGuardrails (database half; the decision tables are in msgai)
// fails if an auto-reply is sent without a supporting FAQ snippet, after the
// cap, after a handoff word, to an opted-out peer, within 10 minutes of a
// human reply, without the disclosure line on the first reply, without an
// audit row, or if a handoff does not flag the conversation and send the
// holding line once.
func TestAutoReplyGuardrails(t *testing.T) {
	r := newAIRig(t)
	ctx := context.Background()
	r.setAI(msgai.ModeAuto, func(m *store.MsgAddressAI) { m.MaxReplies = 2 })
	faq, err := r.st.CreateMsgFAQ(ctx, "test", store.MsgFAQInput{AddressID: r.addr.ID, Title: "Opening hours", Body: "Open 9 to 5 on weekdays.", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	aiSent := func(conv int64) []string {
		rows, err := r.db.QueryContext(ctx, `SELECT body FROM conversation_messages WHERE conversation_id = $1 AND sender_kind = 'ai' ORDER BY id`, conv)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var b string
			_ = rows.Scan(&b)
			out = append(out, b)
		}
		return out
	}
	holding := func(conv int64) int {
		return r.count(`SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND sender_kind = 'system' AND kind = 'message' AND ai_run_id IS NOT NULL`, conv)
	}
	ask := func(peer, body string, replies ...fakellm.Reply) int64 {
		t.Helper()
		conv := r.ingest(peer, body).ConversationID
		r.llm.Queue(msgai.FeatureTriage, triageReply("question", "normal", "neutral", nil, ""))
		for _, rep := range replies {
			r.llm.Queue(msgai.FeatureAutoReply, rep)
		}
		r.drain()
		return conv
	}

	peer := nextPeer()
	conv := ask(peer, "When are you open?", autoAnswer("We are open 9 to 5 on weekdays.", faq.ID)...)
	if got := aiSent(conv); len(got) != 1 || got[0] != "Automated reply.\nWe are open 9 to 5 on weekdays." {
		t.Fatalf("first AI reply = %q", got)
	}
	if n := r.count(`SELECT count(*) FROM messages WHERE conversation_message_id IN (SELECT id FROM conversation_messages WHERE conversation_id = $1 AND sender_kind = 'ai')`, conv); n != 1 {
		t.Fatalf("the AI reply was not queued to the trunk (%d)", n)
	}
	if n := r.count(`SELECT count(*) FROM audit_events WHERE actor = 'system:ai' AND action = 'ai_autoreply' AND resource = 'conversation_message'`); n != 1 {
		t.Fatalf("%d audit rows for the AI reply", n)
	}

	// The second reply has no disclosure; the third is past the cap of 2.
	ask(peer, "And on Saturday?", autoAnswer("We are open 9 to 5 on weekdays.", faq.ID)...)
	if got := aiSent(conv); len(got) != 2 || strings.HasPrefix(got[1], "Automated reply.") {
		t.Fatalf("second AI reply = %q", got)
	}
	ask(peer, "And on Sunday?")
	c, _ := r.st.GetConversation(ctx, r.admAcc(), conv)
	if len(aiSent(conv)) != 2 || !c.AI.Handoff || c.AI.HandoffReason != msgai.ReasonCap || holding(conv) != 1 {
		t.Fatalf("past the cap: %d sent, handoff %v %q, %d holding lines", len(aiSent(conv)), c.AI.Handoff, c.AI.HandoffReason, holding(conv))
	}
	// The holding line goes once until a person replies.
	ask(peer, "Hello?")
	if holding(conv) != 1 {
		t.Fatalf("%d holding lines", holding(conv))
	}

	// A person replies: the AI keeps quiet for 10 minutes, and a draft helps.
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "Hi, I am Anna."}); err != nil {
		t.Fatal(err)
	}
	r.llm.Queue(msgai.FeatureDraft, fakellm.JSON(map[string]any{"reply": "Happy to help."}))
	ask(peer, "Thanks Anna, one more question")
	if got := r.runs(conv, msgai.KindAutoReply); !strings.HasPrefix(got[len(got)-1], "skipped:"+msgai.ReasonHumanActive) {
		t.Fatalf("autoreply runs = %v", got)
	}
	if c, _ = r.st.GetConversation(ctx, r.admAcc(), conv); c.AI.Handoff || c.AI.Draft == nil {
		t.Fatalf("after a person's reply: handoff %v, draft %+v", c.AI.Handoff, c.AI.Draft)
	}

	// A handoff word goes to a person without a model call.
	before := len(r.llm.Requests())
	conv2 := ask(nextPeer(), "Can I talk to a human please")
	reqs := r.llm.Requests()[before:]
	for _, q := range reqs {
		if q.Feature == msgai.FeatureAutoReply {
			t.Fatal("a handoff word reached the auto-reply model")
		}
	}
	if c, _ := r.st.GetConversation(ctx, r.admAcc(), conv2); !c.AI.Handoff || c.AI.HandoffReason != msgai.ReasonHandoffWord || len(aiSent(conv2)) != 0 {
		t.Fatalf("handoff word: %+v", c.AI)
	}

	// A reply that rests on no snippet, or links outside the FAQ, is not sent.
	conv3 := ask(nextPeer(), "Where are you?",
		fakellm.Reply{Content: "done"},
		fakellm.JSON(map[string]any{"reply": "See https://evil.example", "faqIds": []int64{faq.ID}, "handoff": false, "handoffReason": "", "confidence": 0.95}))
	if c, _ := r.st.GetConversation(ctx, r.admAcc(), conv3); len(aiSent(conv3)) != 0 || c.AI.HandoffReason != msgai.ReasonURL {
		t.Fatalf("a link outside the FAQ: %d sent, %+v", len(aiSent(conv3)), c.AI)
	}

	// An opted-out customer: STOP queues nothing, and nothing is sent after.
	out := nextPeer()
	conv4 := r.ingest(out, "STOP").ConversationID
	r.drain()
	ask(out, "When are you open?", autoAnswer("We are open 9 to 5 on weekdays.", faq.ID)...)
	if len(aiSent(conv4)) != 0 {
		t.Fatal("an auto-reply went to a customer who opted out")
	}

	// HELLO_MSG_AI_AUTO=false: no auto-reply, but a draft.
	r.w.AutoAllowed = false
	r.llm.Queue(msgai.FeatureDraft, fakellm.JSON(map[string]any{"reply": "Open 9 to 5."}))
	conv5 := ask(nextPeer(), "When are you open?")
	if len(aiSent(conv5)) != 0 {
		t.Fatal("an auto-reply with HELLO_MSG_AI_AUTO=false")
	}
	if c, _ := r.st.GetConversation(ctx, r.admAcc(), conv5); c.AI.Draft == nil || c.AI.Draft.Text != "Open 9 to 5." {
		t.Fatalf("no draft with auto-replies off: %+v", c.AI.Draft)
	}
	r.w.AutoAllowed = true

	st, err := r.st.GetInboxAIStats(ctx)
	if err != nil || st.AutoReplies != 2 || st.Handoffs < 3 || st.Modes.Auto != 1 {
		t.Fatalf("stats = %+v, %v", st, err)
	}
}

// TestAITriageRoute fails if a number that routes through the AI places a
// conversation before its triage (within the wait), does not use the queue
// the triage suggests, or does not fall back to its own queue.
func TestAITriageRoute(t *testing.T) {
	rr := newRouteRig(t, 1, queue.Linear, 5)
	ctx := context.Background()
	hang := store.IVRDestination{Kind: store.IVRHangup}
	billing, err := rr.st.CreateQueue(ctx, "test", store.QueueInput{
		Name: "Billing", Enabled: true, Strategy: queue.Linear, AgentTimeout: 20, RetryDelay: 5, MaxWait: 600, JoinEmpty: true,
		Timeout: hang, Full: hang, NoAgents: hang, TextEnabled: true, TextCapacity: 3, TextReassignSeconds: 60,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rr.route(messaging.RouteAITriage, strconv.FormatInt(rr.queue.ID, 10))
	r := &aiRig{rig: rr.rig, llm: fakellm.New(t)}
	cfg := aifake.Config()
	cfg.BaseURL = r.llm.URL
	svc, err := ai.New(cfg, aifake.NewStore(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.w = &msgai.Worker{Store: r.st.MessageAI(), AI: svc, AutoAllowed: true}
	r.setAI(msgai.ModeTriage)
	queueOf := func(conv int64) int64 {
		var q *int64
		if err := r.db.QueryRowContext(ctx, `SELECT queue_id FROM conversations WHERE id = $1`, conv).Scan(&q); err != nil {
			t.Fatal(err)
		}
		if q == nil {
			return 0
		}
		return *q
	}
	routed := func(conv int64) bool {
		return r.count(`SELECT count(*) FROM conversations WHERE id = $1 AND routed_at IS NOT NULL`, conv) == 1
	}

	conv := rr.customerText("I was charged twice")
	rr.tick(time.Now())
	if routed(conv) {
		t.Fatal("placed before its triage")
	}
	r.llm.Queue(msgai.FeatureTriage, triageReply("billing", "normal", "negative", []string{"refund"}, "billing"))
	r.drain()
	rr.tick(time.Now())
	if !routed(conv) || queueOf(conv) != billing.ID {
		t.Fatalf("routed %v to queue %d, want the suggested %d", routed(conv), queueOf(conv), billing.ID)
	}
	if n := r.count(`SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND kind = 'system' AND body LIKE '%suggested by the AI triage%'`, conv); n != 1 {
		t.Fatal("the routing line does not say the AI chose the queue")
	}

	// No suggestion: the route's own queue.
	conv = rr.customerText("hello")
	r.llm.Queue(msgai.FeatureTriage, triageReply("question", "normal", "neutral", nil, ""))
	r.drain()
	rr.tick(time.Now())
	if queueOf(conv) != rr.queue.ID && rr.assignee(conv) == 0 {
		t.Fatalf("not placed in the route's queue: %d", queueOf(conv))
	}

	// The AI off: no wait at all.
	r.setAI(msgai.ModeOff)
	conv = rr.customerText("hello again")
	rr.tick(time.Now())
	if !routed(conv) {
		t.Fatal("a number with its AI off waited for a triage")
	}
}
