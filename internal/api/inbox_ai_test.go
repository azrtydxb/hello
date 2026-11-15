package api

// The inbox AI over the API (spec messaging-channels M-30, M-30a): the
// settings and kill switches, a number's mode and guardrails, the FAQ, undoing
// a triage and the counts. Every documented status of each new operation is
// produced here or by TestMessagingRBAC (403).

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestInboxAIAPI fails if the AI settings, a number's AI, the FAQ, the
// revert or the counts do not answer each documented status, store a
// setting outside its bounds, or let a number hold more than 50 snippets.
func TestInboxAIAPI(t *testing.T) {
	r := newInboxRig(t)
	ctx := context.Background()
	admin := r.admin

	t.Run("inbox-wide settings", func(t *testing.T) {
		got := admin.must(http.StatusOK, "GET", "/api/v1/messaging/ai", nil).json(t)
		st := got["status"].(map[string]any)
		if got["enabled"] != true || got["autoEnabled"] != true || len(got["intents"].([]any)) == 0 || st["available"] != false {
			t.Fatalf("defaults = %v", got)
		}
		if a := got["addresses"].([]any); len(a) != 1 || a[0].(map[string]any)["mode"] != "off" {
			t.Fatalf("addresses = %v", a)
		}
		for name, body := range map[string]map[string]any{
			"no intents":      {"intents": []string{}},
			"other listed":    {"intents": []string{"sales", "other"}},
			"long label":      {"sensitiveTopics": []string{strings.Repeat("x", 41)}},
			"control char":    {"businessName": "Acme\x01"},
			"bad channel":     {"channels": []map[string]any{{"id": 0, "aiEnabled": false}}},
			"unknown field":   {"surprise": true},
			"no such channel": {"channels": []map[string]any{{"id": 99999, "aiEnabled": false}}},
		} {
			if got := admin.do("PUT", "/api/v1/messaging/ai", body); got.code != http.StatusBadRequest {
				t.Errorf("%s: %d %s, want 400", name, got.code, got.body)
			}
		}
		got = admin.must(http.StatusOK, "PUT", "/api/v1/messaging/ai", map[string]any{
			"autoEnabled": false, "businessName": "Acme", "intents": []string{"Sales", "support"},
			"channels": []map[string]any{{"id": r.channel, "aiEnabled": false}},
		}).json(t)
		ch := got["channels"].([]any)[0].(map[string]any)
		if got["autoEnabled"] != false || got["enabled"] != true || got["businessName"] != "Acme" || got["intents"].([]any)[0] != "sales" || ch["aiEnabled"] != false {
			t.Fatalf("after PUT = %v", got)
		}
		admin.must(http.StatusOK, "PUT", "/api/v1/messaging/ai", map[string]any{"autoEnabled": true, "channels": []map[string]any{{"id": r.channel, "aiEnabled": true}}})
	})

	addrAI := idPath("/api/v1/messaging/addresses", r.address, "/ai")
	t.Run("a number's AI", func(t *testing.T) {
		got := admin.must(http.StatusOK, "GET", addrAI, nil).json(t)
		if got["mode"] != "off" || got["maxReplies"] != float64(3) || got["hourlyCap"] != float64(60) || got["redact"] != true ||
			got["disclosure"] != true || len(got["handoffWords"].([]any)) != 4 {
			t.Fatalf("defaults = %v", got)
		}
		admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/addresses/99999/ai", nil)
		admin.must(http.StatusNotFound, "PUT", "/api/v1/messaging/addresses/99999/ai", map[string]any{"mode": "triage"})
		for name, body := range map[string]map[string]any{
			"mode":            {"mode": "always"},
			"replies":         {"maxReplies": 11},
			"cap":             {"hourlyCap": 0},
			"confidence":      {"minConfidence": 1.5},
			"no handoff word": {"handoffWords": []string{}},
			"no disclosure":   {"disclosure": true, "disclosureText": ""},
			"long holding":    {"holdingText": strings.Repeat("x", 481)},
		} {
			if got := admin.do("PUT", addrAI, body); got.code != http.StatusBadRequest {
				t.Errorf("%s: %d %s, want 400", name, got.code, got.body)
			}
		}
		got = admin.must(http.StatusOK, "PUT", addrAI, map[string]any{"mode": "auto", "maxReplies": 2, "minConfidence": 0.7,
			"autoIntents": []string{"Question"}, "holdingText": "A colleague will answer soon."}).json(t)
		if got["mode"] != "auto" || got["maxReplies"] != float64(2) || got["autoIntents"].([]any)[0] != "question" || got["redact"] != true {
			t.Fatalf("after PUT = %v", got)
		}
		var n int
		if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE action = 'ai_autoreply_enabled' AND resource_id = $1`,
			strconv.FormatInt(r.address, 10)).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%d audit rows for turning auto-reply on, %v", n, err)
		}
	})

	t.Run("the FAQ", func(t *testing.T) {
		admin.must(http.StatusBadRequest, "GET", "/api/v1/messaging/faq?addressId=x", nil)
		for name, body := range map[string]map[string]any{
			"no number":   {"title": "Hours", "body": "9 to 5"},
			"no title":    {"addressId": r.address, "body": "9 to 5"},
			"long body":   {"addressId": r.address, "title": "Hours", "body": strings.Repeat("x", 4097)},
			"two lines":   {"addressId": r.address, "title": "a\nb", "body": "x"},
			"bad number":  {"addressId": 99999, "title": "Hours", "body": "9 to 5"},
			"unknown key": {"addressId": r.address, "title": "Hours", "body": "9 to 5", "x": 1},
		} {
			if got := admin.do("POST", "/api/v1/messaging/faq", body); got.code != http.StatusBadRequest {
				t.Errorf("%s: %d %s, want 400", name, got.code, got.body)
			}
		}
		f := admin.must(http.StatusCreated, "POST", "/api/v1/messaging/faq", map[string]any{"addressId": r.address, "title": "Hours", "body": "Open 9 to 5. https://example.com/hours"}).json(t)
		id := int64(f["id"].(float64))
		if f["enabled"] != true {
			t.Fatalf("snippet = %v", f)
		}
		path := idPath("/api/v1/messaging/faq", id, "")
		admin.must(http.StatusOK, "GET", path, nil)
		admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/faq/99999", nil)
		got := admin.must(http.StatusOK, "PUT", path, map[string]any{"body": "Open 8 to 6.", "enabled": false}).json(t)
		if got["body"] != "Open 8 to 6." || got["title"] != "Hours" || got["enabled"] != false {
			t.Fatalf("after PUT = %v", got)
		}
		admin.must(http.StatusBadRequest, "PUT", path, map[string]any{"addressId": r.address + 1})
		admin.must(http.StatusBadRequest, "PUT", path, map[string]any{"title": ""})
		admin.must(http.StatusNotFound, "PUT", "/api/v1/messaging/faq/99999", map[string]any{"title": "x"})
		if items := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/faq?addressId="+strconv.FormatInt(r.address, 10), nil)); len(items) != 1 {
			t.Fatalf("list = %v", items)
		}
		for i := 1; i < 50; i++ {
			admin.must(http.StatusCreated, "POST", "/api/v1/messaging/faq", map[string]any{"addressId": r.address, "title": "Q" + strconv.Itoa(i), "body": "A"})
		}
		admin.must(http.StatusConflict, "POST", "/api/v1/messaging/faq", map[string]any{"addressId": r.address, "title": "One too many", "body": "A"})
		if items := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/faq", nil)); len(items) != 50 {
			t.Fatalf("%d snippets", len(items))
		}
		admin.must(http.StatusNoContent, "DELETE", path, nil)
		admin.must(http.StatusNotFound, "DELETE", path, nil)
	})

	t.Run("revert and counts", func(t *testing.T) {
		conv := r.text(peerNumber(), "I was charged twice!")
		if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET tags = '{billing,refund}', priority = 'high', ai_spam = TRUE,
			ai_triage = '{"language":"en","intent":"billing","urgency":"high","sentiment":"negative","spam":true,"sensitive":false,
				"confidence":0.9,"summary":"Charged twice.","suggestedQueue":"","suggestedTags":["refund"],"handoffRecommended":true,"messageId":1}',
			ai_applied = '{"tags":["refund"],"priority":"normal"}' WHERE id = $1`, conv); err != nil {
			t.Fatal(err)
		}
		got := r.operator.must(http.StatusOK, "GET", idPath("/api/v1/inbox/conversations", conv, ""), nil).json(t)
		ai := got["ai"].(map[string]any)
		if ai["mode"] != "auto" || ai["spam"] != true || ai["triage"].(map[string]any)["intent"] != "billing" || ai["draft"] != nil {
			t.Fatalf("ai = %v", ai)
		}
		got = r.operator.must(http.StatusOK, "POST", idPath("/api/v1/inbox/conversations", conv, "/ai/revert"), nil).json(t)
		ai = got["ai"].(map[string]any)
		if got["priority"] != "normal" || len(got["tags"].([]any)) != 1 || got["tags"].([]any)[0] != "billing" || ai["spam"] != false ||
			ai["triage"].(map[string]any)["reverted"] != true {
			t.Fatalf("after revert = %v", got)
		}
		r.operator.must(http.StatusNotFound, "POST", "/api/v1/inbox/conversations/99999/ai/revert", nil)
		r.agent.must(http.StatusNotFound, "POST", idPath("/api/v1/me/inbox/conversations", conv, "/ai/revert"), nil)
		r.assign(conv, r.agentID)
		r.agent.must(http.StatusOK, "POST", idPath("/api/v1/me/inbox/conversations", conv, "/ai/revert"), nil)

		st := r.operator.must(http.StatusOK, "GET", "/api/v1/inbox/ai/stats", nil).json(t)
		if st["modes"].(map[string]any)["auto"] != float64(1) || st["autoReplies"] != float64(0) {
			t.Fatalf("stats = %v", st)
		}
	})
}
