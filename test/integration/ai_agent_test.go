package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/test/fakellm"
)

// labLLMAddr is where the lab's hello-control expects the scripted model
// (compose.yaml, x-ai-env); the containers reach it on the edge gateway.
const labLLMAddr = "0.0.0.0:18088"

// aiTask is the part of a task the test reads.
type aiTask struct {
	Status       string         `json:"status"`
	ErrorCode    *string        `json:"errorCode"`
	ErrorMessage *string        `json:"errorMessage"`
	Result       map[string]any `json:"result"`
}

// askAssistant posts content to a new session and returns the session id
// once its task succeeded.
func (lc *labClient) askAssistant(content string) string {
	lc.t.Helper()
	var sess struct{ ID string }
	lc.must("POST", "/api/v1/ai/sessions", map[string]any{}, &sess, 201)
	var acc struct{ TaskID string }
	lc.t.Logf("asking %q", content[:min(len(content), 30)])
	lc.must("POST", "/api/v1/ai/sessions/"+sess.ID+"/messages", map[string]any{"content": content}, &acc, 202)
	eventually(lc.t, 90*time.Second, "the assistant task to succeed", func() error {
		var task aiTask
		if err := lc.do("GET", "/api/v1/ai/tasks/"+acc.TaskID, nil, &task, 200); err != nil {
			return err
		}
		switch task.Status {
		case "succeeded":
			return nil
		case "failed":
			lc.t.Fatalf("assistant task failed: %v %v", deref(task.ErrorCode), deref(task.ErrorMessage))
		}
		return fmt.Errorf("task %s", task.Status)
	})
	return sess.ID
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type aiProposal struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	HasDelete bool   `json:"hasDelete"`
	Actions   []struct {
		OperationID string `json:"operationId"`
		Destructive bool   `json:"destructive"`
		Before      any    `json:"before"`
		After       any    `json:"after"`
	} `json:"actions"`
}

func (lc *labClient) lastAssistantMessage(sessionID string) (content string, calls []string, proposalID string) {
	lc.t.Helper()
	var d struct {
		Messages []struct {
			Role       string  `json:"role"`
			Content    string  `json:"content"`
			ProposalID *string `json:"proposalId"`
			ToolCalls  []struct {
				OperationID string `json:"operationId"`
				Status      int    `json:"status"`
			} `json:"toolCalls"`
		} `json:"messages"`
	}
	lc.must("GET", "/api/v1/ai/sessions/"+sessionID, nil, &d, 200)
	for _, m := range d.Messages {
		if m.Role != "assistant" {
			continue
		}
		content, calls, proposalID = m.Content, nil, deref(m.ProposalID)
		for _, c := range m.ToolCalls {
			if c.Status != 200 {
				lc.t.Fatalf("tool call %s answered %d", c.OperationID, c.Status)
			}
			calls = append(calls, c.OperationID)
		}
	}
	return content, calls, proposalID
}

func (lc *labClient) auditCount(t *testing.T, via string) int {
	t.Helper()
	out, err := compose("exec", "-T", "postgres", "psql", "-U", "hello", "-d", "hello", "-tAc",
		fmt.Sprintf("SELECT count(*) FROM audit_events WHERE via = '%s'", via)).CombinedOutput()
	if err != nil {
		t.Fatalf("psql: %v\n%s", err, out)
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n); err != nil {
		t.Fatalf("audit count %q: %v", out, err)
	}
	return n
}

// TestAIAgentEndToEnd (spec ai-agent S-6, S-13, S-18) drives the in-product
// agent in the lab against test/fakellm: a chat question makes a tool call
// replayed as the user and a proposal with a diff, including a marked
// delete, whose application changes the configuration with audit rows; a
// seeded REGISTER flood becomes an explained auth_bruteforce finding; a
// dismissed proposal cannot be applied.
func TestAIAgentEndToEnd(t *testing.T) {
	lc := newLabClient(t)
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := compose("logs", "--no-color", "--tail", "60", "hello-control-1", "hello-control-2").CombinedOutput()
			t.Logf("control logs:\n%s", logs)
		}
	})
	llm := fakellm.NewOn(t, labLLMAddr)
	marker := "PROMPT-MARKER-" + randDigits(12)
	remember(marker, labAIKey)

	var status struct {
		Enabled bool    `json:"enabled"`
		Reason  *string `json:"reason"`
		Model   string  `json:"model"`
	}
	lc.must("GET", "/api/v1/ai/status", nil, &status, 200)
	if !status.Enabled || status.Model != "lab-fake-model" {
		t.Fatalf("AI status = %+v (reason %v), want enabled with the lab model", status, deref(status.Reason))
	}

	// A read question: the model calls listExtensions, the call is replayed
	// as the asking user and the answer cites it.
	d := lc.createExtension("desk")[0]
	remember(d.Secret)
	llm.Queue("assistant",
		fakellm.Reply{ToolCalls: []fakellm.ToolCall{{ID: "call_1", Name: "listExtensions", Args: map[string]any{}}}},
		fakellm.JSON(map[string]any{"answer": "Extension " + d.Extension + " exists.", "citations": []int{0}}))
	sid := lc.askAssistant("Which extensions exist? " + marker)
	answer, calls, _ := lc.lastAssistantMessage(sid)
	if len(calls) != 1 || calls[0] != "listExtensions" || !strings.Contains(answer, d.Extension) {
		t.Fatalf("answer %q with tool calls %v, want one listExtensions call and the extension in the answer", answer, calls)
	}

	// A change: update one ring group and delete another in one proposal.
	ext := lc.extensionID(d.Extension)
	member := []map[string]any{{"extensionId": mustInt(t, ext), "position": 1, "weight": 1, "delay": 0}}
	var keep, drop struct{ ID int64 }
	lc.must("POST", "/api/v1/ring-groups", map[string]any{"name": "ai-keep-" + randDigits(6), "strategy": "ring-all", "members": member}, &keep, 201)
	lc.must("POST", "/api/v1/ring-groups", map[string]any{"name": "ai-drop-" + randDigits(6), "strategy": "ring-all", "members": member}, &drop, 201)
	propose := func(actions []map[string]any) aiProposal {
		llm.Queue("assistant", fakellm.JSON(map[string]any{
			"answer": "Here is the change.", "citations": []int{},
			"proposal": map[string]any{"title": "Tidy ring groups", "rationale": "One group is unused.", "actions": actions},
		}))
		_, _, pid := lc.lastAssistantMessage(lc.askAssistant("Please tidy the ring groups"))
		if pid == "" {
			t.Fatal("the answer made no proposal")
		}
		var p aiProposal
		lc.must("GET", "/api/v1/ai/proposals/"+pid, nil, &p, 200)
		return p
	}
	actions := []map[string]any{
		{"operationId": "updateRingGroup", "pathParams": map[string]string{"id": fmt.Sprint(keep.ID)}, "body": map[string]any{"strategy": "sequential"}},
		{"operationId": "deleteRingGroup", "pathParams": map[string]string{"id": fmt.Sprint(drop.ID)}},
	}
	p := propose(actions)
	if p.Status != "open" || !p.HasDelete || len(p.Actions) != 2 || !p.Actions[1].Destructive || p.Actions[0].Before == nil || p.Actions[0].After == nil {
		t.Fatalf("proposal = %+v, want open with a diff and the delete marked", p)
	}
	// Nothing changes before a human applies it.
	lc.must("GET", fmt.Sprintf("/api/v1/ring-groups/%d", drop.ID), nil, nil, 200)

	// A dismissed proposal cannot be applied. It changes a different ring
	// group: an equal draft would refresh the open proposal above (same
	// fingerprint, same row) and dismissing it would close that one.
	var gone struct{ ID int64 }
	lc.must("POST", "/api/v1/ring-groups", map[string]any{"name": "ai-gone-" + randDigits(6), "strategy": "ring-all", "members": member}, &gone, 201)
	dismissed := propose([]map[string]any{
		{"operationId": "deleteRingGroup", "pathParams": map[string]string{"id": fmt.Sprint(gone.ID)}},
	})
	if dismissed.ID == p.ID {
		t.Fatal("the second draft refreshed the first proposal instead of making a new one")
	}
	lc.must("POST", "/api/v1/ai/proposals/"+dismissed.ID+"/dismiss", map[string]any{"reason": "not_needed"}, nil, 200)
	if err := lc.do("POST", "/api/v1/ai/proposals/"+dismissed.ID+"/apply", nil, nil, 409); err != nil {
		t.Fatalf("applying a dismissed proposal: %v", err)
	}
	lc.must("GET", fmt.Sprintf("/api/v1/ring-groups/%d", drop.ID), nil, nil, 200)

	// Applying changes the configuration, with audit rows naming the proposal.
	before := lc.configRevision()
	lc.must("POST", "/api/v1/ai/proposals/"+p.ID+"/apply", nil, nil, 200)
	var rg struct{ Strategy string }
	lc.must("GET", fmt.Sprintf("/api/v1/ring-groups/%d", keep.ID), nil, &rg, 200)
	if rg.Strategy != "sequential" {
		t.Fatalf("ring group strategy = %q after apply, want sequential", rg.Strategy)
	}
	lc.must("GET", fmt.Sprintf("/api/v1/ring-groups/%d", drop.ID), nil, nil, 404)
	if after := lc.configRevision(); after <= before {
		t.Fatalf("config revision %d -> %d, want it raised by the applied changes", before, after)
	}
	if n := lc.auditCount(t, "ai-proposal:"+p.ID); n < 2 {
		t.Fatalf("%d audit rows via ai-proposal:%s, want one per action", n, p.ID)
	}

	// A REGISTER flood from one source: the detector finds it, the model explains it.
	flood := lc.devices("desk")[0]
	t.Cleanup(func() { clearThrottle(t) })
	clearThrottle(t)
	ctx := t.Context()
	nodes := []string{labSIP1, labSIP2}
	for i := range 10 {
		res, err := phone(t, flood, nodes[i%2]).RegisterWithPassword(ctx, time.Hour, "wrong-"+randDigits(6))
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode == 200 {
			t.Fatalf("attempt %d with a wrong password succeeded", i+1)
		}
	}
	runAIOps := func() {
		lc.must("POST", "/api/v1/ai/agents/aiops/run", nil, nil, 202)
	}
	type finding struct {
		ID          string `json:"id"`
		CandidateID string `json:"candidateId"`
		Type        string `json:"type"`
		Status      string `json:"status"`
		Explained   bool   `json:"explained"`
	}
	findings := func() []finding {
		var out struct{ Items []finding }
		lc.must("GET", "/api/v1/ai/findings", nil, &out, 200)
		return out.Items
	}
	var brute finding
	runAIOps()
	eventually(t, 60*time.Second, "an auth_bruteforce finding", func() error {
		for _, f := range findings() {
			if f.Type == "auth_bruteforce" && f.Status == "open" {
				brute = f
				return nil
			}
		}
		return fmt.Errorf("no open auth_bruteforce finding in %v", findings())
	})
	// Explain the whole open set, the flood first.
	var entries []map[string]any
	entries = append(entries, map[string]any{"id": brute.CandidateID, "rank": 1, "summary": "Someone is guessing REGISTER credentials.",
		"likelyCause": "A scanner from one source.", "nextStep": "Block the source at the firewall."})
	rank := 2
	for _, f := range findings() {
		if f.Status == "open" && f.CandidateID != brute.CandidateID {
			entries = append(entries, map[string]any{"id": f.CandidateID, "rank": rank, "summary": "See the evidence.", "likelyCause": "Unknown.", "nextStep": "Review it."})
			rank++
		}
	}
	llm.Queue("aiops_explain", fakellm.JSON(map[string]any{"findings": entries}))
	time.Sleep(2 * time.Second) // the explain minimum interval
	runAIOps()
	eventually(t, 90*time.Second, "the finding to be explained", func() error {
		for _, f := range findings() {
			if f.ID == brute.ID && f.Explained {
				return nil
			}
		}
		return fmt.Errorf("finding %s not explained yet (%d replies pending)", brute.ID, llm.Pending("aiops_explain"))
	})
}
