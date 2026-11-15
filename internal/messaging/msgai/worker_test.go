package msgai_test

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/msgai"
)

// fakeStore is the worker's persistence in memory: one queue of jobs, a
// context per job, and the results.
type fakeStore struct {
	mu      sync.Mutex
	jobs    []msgai.Job
	ctx     map[int64]msgai.Context
	results map[int64]msgai.Result
}

func (f *fakeStore) ClaimAIRun(context.Context) (msgai.Job, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.jobs) == 0 {
		return msgai.Job{}, false, nil
	}
	j := f.jobs[0]
	f.jobs = f.jobs[1:]
	return j, true, nil
}

func (f *fakeStore) AIRunContext(_ context.Context, j msgai.Job) (msgai.Context, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.ctx[j.RunID]
	if !ok {
		return c, errors.New("no context")
	}
	c.Job = j
	return c, nil
}

func (f *fakeStore) FinishAIRun(_ context.Context, j msgai.Job, r msgai.Result) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[j.RunID] = r
	return nil
}

func newFake() *fakeStore {
	return &fakeStore{ctx: map[int64]msgai.Context{}, results: map[int64]msgai.Result{}}
}

func (f *fakeStore) add(kind string, c msgai.Context) int64 {
	id := int64(len(f.ctx) + 1)
	f.jobs = append(f.jobs, msgai.Job{RunID: id, MessageID: 100 + id, ConversationID: 1, Kind: kind})
	f.ctx[id] = c
	return id
}

func baseCtx(mode string) msgai.Context {
	return msgai.Context{
		Settings: msgai.Settings{Enabled: true, AutoEnabled: true, BusinessName: "Acme", Intents: msgai.DefaultIntents,
			SensitiveTopics: msgai.DefaultSensitiveTopics},
		Address: msgai.AddressAI{Mode: mode, Redact: true, MaxReplies: 3, HourlyCap: 60, MinConfidence: 0.8,
			Disclosure: true, DisclosureText: "Automated reply."},
		ChannelOn: true, ChannelKind: messaging.KindSMS, Fresh: true,
		Body: "When are you open? Call me on +32 475 12 34 56",
		Lines: []msgai.Line{
			{From: "customer", Text: "Hello"},
			{From: "agent", Text: "Hi, how can we help?"},
			{From: "customer", Text: "When are you open? Call me on +32 475 12 34 56"},
		},
		FAQ:    []msgai.FAQ{{ID: 7, Title: "Hours", Body: "Open 9 to 5 on weekdays."}},
		Queues: []string{"Support", "Sales"},
	}
}

func triageJSON(intent string) *provider.Response {
	return aifake.JSON(map[string]any{"language": "en", "intent": intent, "urgency": "normal", "sentiment": "neutral",
		"spam": false, "sensitive": false, "confidence": 0.9, "summary": "Asks for the opening hours.",
		"suggestedQueue": "support", "suggestedTags": []string{"Hours"}, "handoffRecommended": false})
}

// calls renders the model calls as text, to search what the model saw.
func calls(t *testing.T, m interface{ RecordedCalls() []provider.Call }) string {
	t.Helper()
	b, err := json.Marshal(m.RecordedCalls())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestWorkerTriage fails if a triage is not made through ai.Generate with
// the message_triage feature, shows the model more than the conversation's
// window, sends a phone number the number redacts, or accepts an intent or
// queue not in the lists.
func TestWorkerTriage(t *testing.T) {
	svc, model, _ := aifake.Service(t, aifake.Config(), triageJSON("question"), triageJSON("weather"), triageJSON("weather"), triageJSON("weather"))
	f := newFake()
	id := f.add(msgai.KindTriage, baseCtx(msgai.ModeTriage))
	bad := f.add(msgai.KindTriage, baseCtx(msgai.ModeTriage))
	w := &msgai.Worker{Store: f, AI: svc, Metrics: msgai.NewMetrics(nil)}
	for range 2 {
		if ran, err := w.Once(context.Background()); !ran || err != nil {
			t.Fatalf("Once = %v, %v", ran, err)
		}
	}
	r := f.results[id]
	if r.State != msgai.StateDone || r.Triage == nil || r.Triage.Intent != "question" || r.Triage.SuggestedQueue != "Support" ||
		len(r.Triage.SuggestedTags) != 1 || r.Triage.SuggestedTags[0] != "hours" {
		t.Fatalf("triage = %+v %+v", r, r.Triage)
	}
	seen := calls(t, model)
	if !strings.Contains(seen, "hello-feature: message_triage") {
		t.Error("the call was not made as message_triage")
	}
	if strings.Contains(seen, "475 12 34 56") || !strings.Contains(seen, "[phone]") {
		t.Error("the customer's number reached the model")
	}
	if b := f.results[bad]; b.State != msgai.StateFailed || b.Error != ai.CodeInvalidOutput {
		t.Fatalf("an intent outside the list = %+v", b)
	}
}

// TestWorkerDraft fails if a draft is not made as message_draft, or is made
// for a number in triage mode.
func TestWorkerDraft(t *testing.T) {
	svc, model, _ := aifake.Service(t, aifake.Config(), aifake.JSON(map[string]any{"reply": "We are open 9 to 5 on weekdays."}))
	f := newFake()
	id := f.add(msgai.KindDraft, baseCtx(msgai.ModeDraft))
	off := f.add(msgai.KindDraft, baseCtx(msgai.ModeTriage))
	w := &msgai.Worker{Store: f, AI: svc}
	for range 2 {
		if _, err := w.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if r := f.results[id]; r.State != msgai.StateDone || r.Draft == nil || !strings.Contains(r.Draft.Reply, "9 to 5") {
		t.Fatalf("draft = %+v", r)
	}
	if r := f.results[off]; r.State != msgai.StateSkipped {
		t.Fatalf("a draft in triage mode = %+v", r)
	}
	if !strings.Contains(calls(t, model), "hello-feature: message_draft") || len(model.RecordedCalls()) != 1 {
		t.Fatalf("calls = %d", len(model.RecordedCalls()))
	}
}

// TestWorkerAutoReply fails if an auto-reply is sent without the tools'
// grounding rules, if a model failure does not hand the conversation to a
// person, if a guardrail that needs no model makes a call, or if the AI
// off leaves a run waiting.
func TestWorkerAutoReply(t *testing.T) {
	triage := &msgai.Triage{Language: "en", Intent: "question", Urgency: "normal", Sentiment: "neutral", Confidence: 0.9, Summary: "hours"}
	auto := func() msgai.Context {
		c := baseCtx(msgai.ModeAuto)
		c.Triage = triage
		return c
	}

	t.Run("answers from the FAQ", func(t *testing.T) {
		svc, model, _ := aifake.Service(t, aifake.Config(),
			aifake.ToolCall("c1", "lookup_faq", map[string]any{"query": "opening hours"}),
			aifake.Text("Snippet 7 answers it."),
			aifake.JSON(map[string]any{"reply": "We are open 9 to 5 on weekdays.", "faqIds": []int64{7}, "handoff": false, "handoffReason": "", "confidence": 0.95}))
		f := newFake()
		id := f.add(msgai.KindAutoReply, auto())
		w := &msgai.Worker{Store: f, AI: svc, AutoAllowed: true}
		if _, err := w.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := f.results[id]
		if r.Reply == nil || !r.Reply.Send || r.Reply.Text != "Automated reply.\nWe are open 9 to 5 on weekdays." {
			t.Fatalf("reply = %+v", r.Reply)
		}
		seen := calls(t, model)
		if !strings.Contains(seen, "hello-feature: message_autoreply") || !strings.Contains(seen, "Open 9 to 5 on weekdays") {
			t.Error("the FAQ did not reach the model through the call")
		}
	})

	t.Run("the handoff tool", func(t *testing.T) {
		svc, _, _ := aifake.Service(t, aifake.Config(),
			aifake.ToolCall("c1", "handoff", map[string]any{"reason": "asks about a refund"}),
			aifake.Text("Handed off."),
			aifake.JSON(map[string]any{"reply": "", "faqIds": []int64{}, "handoff": true, "handoffReason": "refund", "confidence": 0.9}))
		f := newFake()
		id := f.add(msgai.KindAutoReply, auto())
		w := &msgai.Worker{Store: f, AI: svc, AutoAllowed: true}
		if _, err := w.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
		if r := f.results[id].Reply; r == nil || r.Send || !r.Handoff || r.Reason != msgai.ReasonModel {
			t.Fatalf("decision = %+v", r)
		}
	})

	t.Run("a model failure hands off", func(t *testing.T) {
		svc, model, _ := aifake.Service(t, aifake.Config())
		model.Err = errors.New("connection refused")
		f := newFake()
		id := f.add(msgai.KindAutoReply, auto())
		w := &msgai.Worker{Store: f, AI: svc, AutoAllowed: true}
		if _, err := w.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := f.results[id]
		if r.State != msgai.StateFailed || r.Reply == nil || !r.Reply.Handoff || r.Reply.Reason != msgai.ReasonError {
			t.Fatalf("result = %+v %+v", r, r.Reply)
		}
	})

	t.Run("guardrails need no model", func(t *testing.T) {
		svc, model, _ := aifake.Service(t, aifake.Config())
		f := newFake()
		c := auto()
		c.Body = "Let me speak to a human"
		human := f.add(msgai.KindAutoReply, c)
		off := f.add(msgai.KindAutoReply, auto())
		w := &msgai.Worker{Store: f, AI: svc, AutoAllowed: false}
		for range 2 {
			if _, err := w.Once(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		if len(model.RecordedCalls()) != 0 {
			t.Fatal("a guardrail let a model call through")
		}
		if r := f.results[human].Reply; r == nil || r.Send {
			t.Fatalf("handoff word with HELLO_MSG_AI_AUTO=false = %+v", r)
		}
		if r := f.results[off]; r.State != msgai.StateSkipped || r.Error != msgai.ReasonOff {
			t.Fatalf("HELLO_MSG_AI_AUTO=false = %+v", r)
		}
	})

	t.Run("the AI off skips", func(t *testing.T) {
		f := newFake()
		tr := f.add(msgai.KindTriage, baseCtx(msgai.ModeTriage))
		stale := baseCtx(msgai.ModeTriage)
		stale.Fresh = false
		st := f.add(msgai.KindTriage, stale)
		w := &msgai.Worker{Store: f}
		for range 2 {
			if _, err := w.Once(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		if r := f.results[tr]; r.State != msgai.StateSkipped || r.Error != msgai.ReasonAIOff {
			t.Fatalf("with no AI = %+v", r)
		}
		if r := f.results[st]; r.State != msgai.StateStale {
			t.Fatalf("a stale run = %+v", r)
		}
	})
}

// TestMessagingAIUsesSDK (static half; the database half is in
// internal/messaging) fails if any messaging code builds a model, imports a
// model provider or an HTTP client for one, or calls a model other than
// through ai.Generate (spec M-30): only internal/ai/provider.go may.
func TestMessagingAIUsesSDK(t *testing.T) {
	root := filepath.Join("..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		inAI := strings.Contains(filepath.ToSlash(path), "/msgai/")
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			switch {
			case strings.HasPrefix(p, "github.com/azrtydxb/go-ai-sdk") && (!inAI || p != "github.com/azrtydxb/go-ai-sdk/ai"):
				t.Errorf("%s imports %s: messaging may only use go-ai-sdk's tool type, through internal/ai", path, p)
			case inAI && (p == "net/http" || strings.Contains(p, "openai") || strings.Contains(p, "anthropic")):
				t.Errorf("%s imports %s: the inbox AI must not reach a model by itself", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
