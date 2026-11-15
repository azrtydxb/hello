package messaging_test

// What a redaction, an erase and an export do to the inbox AI's output
// (spec messaging-channels M-20, M-21 with M-30). Skipped without
// HELLO_TEST_DATABASE_URL.

import (
	"context"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging/msgai"
	"github.com/azrtydxb/hello/test/fakellm"
)

// secretTriage is a triage whose summary repeats the customer's text, as a
// model's summary does.
func secretTriage(secret string) fakellm.Reply {
	return fakellm.JSON(map[string]any{"language": "en", "intent": "question", "urgency": "normal", "sentiment": "neutral",
		"spam": false, "sensitive": false, "confidence": 0.9, "summary": "The customer says " + secret + ".",
		"suggestedQueue": "", "suggestedTags": []string{"order"}, "handoffRecommended": false})
}

// aiCopies counts the rows the AI keeps that still hold s.
func (r *aiRig) aiCopies(conv int64, s string) int {
	r.t.Helper()
	return r.count(`SELECT (SELECT count(*) FROM ai_message_runs WHERE conversation_id = $1 AND result::text LIKE '%' || $2 || '%')
		+ (SELECT count(*) FROM conversations WHERE id = $1 AND ai_triage::text LIKE '%' || $2 || '%')`, conv, s)
}

// TestRedactionClearsAIOutput fails if a redacted message's text survives in
// what the inbox AI derived from it (a run's result: the triage summary, the
// reply draft, an auto-reply; the conversation's triage summary), if the
// draft built on it is still offered, if a run still working on the old
// text stores or applies its result after the redaction, or if redacting
// the AI's own message leaves the run that wrote it holding the text.
func TestRedactionClearsAIOutput(t *testing.T) {
	r := newAIRig(t)
	ctx := context.Background()
	r.setAI(msgai.ModeDraft)

	t.Run("results and the triage summary", func(t *testing.T) {
		in := r.ingest(nextPeer(), "my order 7731 never arrived")
		r.llm.Queue(msgai.FeatureTriage, secretTriage("SECRET-ORDER-7731"))
		r.llm.Queue(msgai.FeatureDraft, fakellm.JSON(map[string]any{"reply": "Sorry about SECRET-ORDER-7731."}))
		r.drain()
		if n := r.aiCopies(in.ConversationID, "SECRET-ORDER-7731"); n != 3 {
			t.Fatalf("%d AI copies before the redaction, want the triage, the draft and the summary", n)
		}
		// The AI wrote a message from the draft: redacting it clears the
		// run that wrote it, and only that.
		var draftRun, aiMsg int64
		if err := r.db.QueryRowContext(ctx, `SELECT id FROM ai_message_runs WHERE conversation_id = $1 AND kind = 'draft'`,
			in.ConversationID).Scan(&draftRun); err != nil {
			t.Fatal(err)
		}
		if err := r.db.QueryRowContext(ctx, `
			INSERT INTO conversation_messages (conversation_id, channel_id, direction, sender_kind, body, ai_run_id)
			SELECT id, channel_id, 'out', 'ai', 'Sorry about SECRET-ORDER-7731.', $2 FROM conversations WHERE id = $1 RETURNING id`,
			in.ConversationID, draftRun).Scan(&aiMsg); err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.RedactConversationMessage(ctx, "user:admin", in.ConversationID, aiMsg); err != nil {
			t.Fatal(err)
		}
		if n := r.count(`SELECT count(*) FROM ai_message_runs WHERE id = $1 AND result IS NOT NULL`, draftRun); n != 0 {
			t.Fatal("the run that wrote the redacted AI message keeps its text")
		}
		if n := r.aiCopies(in.ConversationID, "SECRET-ORDER-7731"); n != 2 {
			t.Fatalf("%d AI copies after redacting the AI message, want the triage and its summary", n)
		}

		if _, err := r.st.RedactConversationMessage(ctx, "user:admin", in.ConversationID, in.MessageID); err != nil {
			t.Fatal(err)
		}
		if n := r.aiCopies(in.ConversationID, "SECRET-ORDER-7731"); n != 0 {
			t.Fatalf("%d AI copies of the redacted text left", n)
		}
		if n := r.count(`SELECT count(*) FROM ai_message_runs WHERE conversation_id = $1 AND result IS NOT NULL`, in.ConversationID); n != 0 {
			t.Fatalf("%d run results left after the redaction", n)
		}
		c, err := r.st.GetConversation(ctx, r.admAcc(), in.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		if c.AI.Draft != nil {
			t.Fatalf("the draft built on the redacted text is still offered: %+v", c.AI.Draft)
		}
		// The classification stays: it holds no text of the customer's.
		if c.AI.Triage == nil || c.AI.Triage.Summary != "" || c.AI.Triage.Intent != "question" {
			t.Fatalf("triage after the redaction = %+v", c.AI.Triage)
		}
	})

	t.Run("a run working on the old text", func(t *testing.T) {
		in := r.ingest(nextPeer(), "SECRET-RUNNING")
		job, ok, err := r.st.MessageAI().ClaimAIRun(ctx)
		if err != nil || !ok || job.ConversationID != in.ConversationID {
			t.Fatalf("claim = %+v, %v, %v", job, ok, err)
		}
		if _, err := r.st.RedactConversationMessage(ctx, "user:admin", in.ConversationID, in.MessageID); err != nil {
			t.Fatal(err)
		}
		tr := msgai.Triage{Language: "en", Intent: "question", Urgency: "high", Sentiment: "neutral", Confidence: 0.9,
			Summary: "The customer says SECRET-RUNNING.", SuggestedTags: []string{"x"}}
		if err := r.st.MessageAI().FinishAIRun(ctx, job, msgai.Result{State: msgai.StateDone, Triage: &tr}); err != nil {
			t.Fatal(err)
		}
		if got := r.runs(in.ConversationID, msgai.KindTriage); len(got) != 1 || got[0] != "stale:redacted" {
			t.Fatalf("runs = %v", got)
		}
		if n := r.aiCopies(in.ConversationID, "SECRET-RUNNING"); n != 0 {
			t.Fatal("a run that read the text before the redaction stored it after")
		}
		if c, _ := r.st.GetConversation(ctx, r.admAcc(), in.ConversationID); c.AI.Triage != nil || c.Priority != "normal" {
			t.Fatalf("a run that read the redacted text was applied: %+v", c.AI)
		}
	})

	t.Run("a run queued while the redaction committed", func(t *testing.T) {
		in := r.ingest(nextPeer(), "SECRET-RACE")
		job, ok, err := r.st.MessageAI().ClaimAIRun(ctx)
		if err != nil || !ok || job.ConversationID != in.ConversationID {
			t.Fatalf("claim = %+v, %v, %v", job, ok, err)
		}
		// The redaction's own pass over the runs missed this one; only the
		// message says it was redacted.
		if _, err := r.db.ExecContext(ctx, `UPDATE conversation_messages SET body = '', redacted_at = now() WHERE id = $1`, in.MessageID); err != nil {
			t.Fatal(err)
		}
		tr := msgai.Triage{Language: "en", Intent: "question", Urgency: "normal", Sentiment: "neutral", Confidence: 0.9,
			Summary: "SECRET-RACE", SuggestedTags: []string{}}
		if err := r.st.MessageAI().FinishAIRun(ctx, job, msgai.Result{State: msgai.StateDone, Triage: &tr}); err != nil {
			t.Fatal(err)
		}
		if got := r.runs(in.ConversationID, msgai.KindTriage); len(got) != 1 || got[0] != "stale:redacted" {
			t.Fatalf("runs = %v", got)
		}
		if n := r.aiCopies(in.ConversationID, "SECRET-RACE"); n != 0 {
			t.Fatal("the result of a run that read a redacted text was stored")
		}
	})

	t.Run("a note is not the AI's", func(t *testing.T) {
		in := r.ingest(nextPeer(), "keep this")
		r.llm.Queue(msgai.FeatureTriage, secretTriage("KEPT-SUMMARY"))
		r.llm.Queue(msgai.FeatureDraft, fakellm.JSON(map[string]any{"reply": "Sure."}))
		r.drain()
		note, err := r.st.AddNote(ctx, r.admAcc(), in.ConversationID, "internal")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.RedactConversationMessage(ctx, "user:admin", in.ConversationID, note.ID); err != nil {
			t.Fatal(err)
		}
		if n := r.aiCopies(in.ConversationID, "KEPT-SUMMARY"); n != 2 {
			t.Fatalf("redacting a note cleared %d AI copies of the customer's text", 2-n)
		}
	})
}

// TestEraseAndExportAIOutput fails if the export of a customer leaves out
// what the inbox AI holds of them (the conversation's triage, each run's
// output), or if an erase leaves any of it.
func TestEraseAndExportAIOutput(t *testing.T) {
	r := newAIRig(t)
	ctx := context.Background()
	r.setAI(msgai.ModeDraft)
	peer := nextPeer()
	in := r.ingest(peer, "where is my parcel")
	r.llm.Queue(msgai.FeatureTriage, secretTriage("EXPORT-SUMMARY"))
	r.llm.Queue(msgai.FeatureDraft, fakellm.JSON(map[string]any{"reply": "EXPORT-DRAFT"}))
	r.drain()
	var peerKey string
	if err := r.db.QueryRowContext(ctx, `SELECT peer_key FROM conversations WHERE id = $1`, in.ConversationID).Scan(&peerKey); err != nil {
		t.Fatal(err)
	}

	exp, err := r.st.ExportPeer(ctx, "user:admin", peerKey, false)
	if err != nil || len(exp.Conversations) != 1 {
		t.Fatalf("export = %+v, %v", exp, err)
	}
	c := exp.Conversations[0]
	if !strings.Contains(string(c.AI.Triage), "EXPORT-SUMMARY") || !strings.Contains(string(c.AI.Applied), "order") {
		t.Fatalf("the conversation's AI in the export = %s %s", c.AI.Triage, c.AI.Applied)
	}
	var runs []string
	for _, m := range c.Messages {
		for _, run := range m.AIRuns {
			if m.ID != in.MessageID {
				t.Fatalf("run %d listed with message %d, not %d", run.ID, m.ID, in.MessageID)
			}
			runs = append(runs, run.Kind+":"+run.State+":"+string(run.Result))
		}
	}
	if len(runs) != 2 || !strings.Contains(runs[0], "triage:done:") || !strings.Contains(runs[0], "EXPORT-SUMMARY") ||
		!strings.Contains(runs[1], "draft:done:") || !strings.Contains(runs[1], "EXPORT-DRAFT") {
		t.Fatalf("exported runs = %v", runs)
	}
	var audit string
	if err := r.db.QueryRowContext(ctx, `SELECT action FROM audit_events WHERE action LIKE 'export %' ORDER BY id DESC LIMIT 1`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audit, " ai_runs=2 ") {
		t.Errorf("export audit = %q", audit)
	}

	if _, err := r.st.ErasePeer(ctx, "user:admin", peerKey); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT count(*) FROM ai_message_runs WHERE conversation_id = $1 OR result::text LIKE '%EXPORT-%'`, in.ConversationID); n != 0 {
		t.Fatalf("%d AI runs left after the erase", n)
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE ai_triage::text LIKE '%EXPORT-%'`); n != 0 {
		t.Fatal("the triage summary outlived the erase")
	}
}
