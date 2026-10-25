package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// TestMessageBodyValidation fails if a send request with a bad target, an
// empty or oversize body, or an unconfirmed broadcast is accepted, or if a
// user's own send may broadcast.
func TestMessageBodyValidation(t *testing.T) {
	ok := messageBody{TargetKind: "extension", Target: "101", Body: "hi"}
	if f := ok.validate(true); len(f) != 0 {
		t.Fatalf("valid body refused: %v", f)
	}
	for name, c := range map[string]struct {
		b              messageBody
		allowBroadcast bool
		path           string
	}{
		"empty body":        {messageBody{TargetKind: "extension", Target: "101", Body: "  "}, true, "body"},
		"big body":          {messageBody{TargetKind: "extension", Target: "101", Body: strings.Repeat("x", store.MaxMessageBytes+1)}, true, "body"},
		"no target":         {messageBody{TargetKind: "extension", Body: "x"}, true, "target"},
		"bad number":        {messageBody{TargetKind: "external", Target: "12 34", Body: "x"}, true, "target"},
		"unknown kind":      {messageBody{TargetKind: "everyone", Body: "x"}, true, "targetKind"},
		"unconfirmed":       {messageBody{TargetKind: "broadcast", Body: "x"}, true, "confirm"},
		"broadcast target":  {messageBody{TargetKind: "broadcast", Target: "101", Body: "x", Confirm: true}, true, "target"},
		"user broadcast":    {messageBody{TargetKind: "broadcast", Body: "x", Confirm: true}, false, "targetKind"},
		"control character": {messageBody{TargetKind: "extension", Target: "101", Body: "a\x00b"}, true, "body"},
	} {
		f := c.b.validate(c.allowBroadcast)
		if len(f) == 0 || f[0].Path != c.path {
			t.Errorf("%s: fields = %v, want a refusal of %s", name, f, c.path)
		}
	}
	if f := (messageBody{TargetKind: "broadcast", Body: "Fire drill", Confirm: true}).validate(true); len(f) != 0 {
		t.Errorf("confirmed broadcast refused: %v", f)
	}
}

// TestMessagesAPI fails if the roles do not hold (a viewer cannot send, an
// operator cannot read the history, only an admin reads or changes
// settings), if a send does not reach the history with the right scope, if a
// user can see or send as anyone but their own extension, or if retention
// settings are not validated and kept.
func TestMessagesAPI(t *testing.T) {
	e := newEnv(t, noLive{})
	admin := e.login()
	viewer, viewerID := e.asRole("vera", auth.RoleViewer)
	operator, _ := e.asRole("otto", auth.RoleOperator)
	other, otherID := e.asRole("olga", auth.RoleViewer)
	unlinked, _ := e.asRole("uma", auth.RoleViewer)

	e101 := admin.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	e102 := admin.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Support"}).json(t)
	for _, n := range []map[string]any{e101, e102} {
		admin.must(http.StatusCreated, "POST", "/api/v1/devices", map[string]any{"extensionId": n["id"], "sipUsername": "dev" + n["number"].(string)})
	}
	admin.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(viewerID, 10), map[string]any{"extensionId": e101["id"]})
	admin.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(otherID, 10), map[string]any{"extensionId": e102["id"]})

	t.Run("roles", func(t *testing.T) {
		send := map[string]any{"targetKind": "extension", "target": "101", "body": "hello"}
		viewer.must(http.StatusForbidden, "POST", "/api/v1/messages", send)
		operator.must(http.StatusAccepted, "POST", "/api/v1/messages", send)
		operator.must(http.StatusForbidden, "GET", "/api/v1/messages", nil)
		operator.must(http.StatusForbidden, "GET", "/api/v1/messages/conversations", nil)
		operator.must(http.StatusForbidden, "GET", "/api/v1/messages/settings", nil)
		operator.must(http.StatusForbidden, "POST", "/api/v1/messages/purge", map[string]any{"olderThanDays": 0})
		viewer.must(http.StatusForbidden, "GET", "/api/v1/messages", nil)
		items := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messages", nil))
		if len(items) != 1 || items[0].(map[string]any)["body"] != "hello" || items[0].(map[string]any)["status"] != "pending" {
			t.Fatalf("history = %v", items)
		}
	})

	t.Run("send targets", func(t *testing.T) {
		admin.must(http.StatusNotFound, "POST", "/api/v1/messages", map[string]any{"targetKind": "extension", "target": "999", "body": "x"})
		admin.must(http.StatusNotFound, "POST", "/api/v1/messages", map[string]any{"targetKind": "ring_group", "target": "nobody", "body": "x"})
		admin.must(http.StatusBadRequest, "POST", "/api/v1/messages", map[string]any{"targetKind": "broadcast", "body": "x"})
		// No trunk has messaging on.
		admin.must(http.StatusConflict, "POST", "/api/v1/messages", map[string]any{"targetKind": "external", "target": "+32475000000", "body": "x"})
		got := admin.must(http.StatusAccepted, "POST", "/api/v1/messages", map[string]any{"targetKind": "broadcast", "body": "Fire drill at 3pm", "confirm": true}).json(t)
		if got["scope"] != "broadcast" || got["recipients"] != float64(2) {
			t.Fatalf("broadcast = %v, want 2 recipients", got)
		}
		convs := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messages/conversations", nil))
		found := false
		for _, c := range convs {
			if c.(map[string]any)["key"] == "broadcast" {
				found = true
			}
		}
		if !found {
			t.Fatalf("conversations = %v, want the broadcast thread", convs)
		}
		hits := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messages?q=fire+drill", nil))
		if len(hits) != 1 {
			t.Fatalf("search = %v, want the one broadcast (one entry per send)", hits)
		}
		if hit := hits[0].(map[string]any); hit["recipients"] != float64(2) || hit["scope"] != "broadcast" {
			t.Fatalf("broadcast entry = %v", hit)
		}
	})

	t.Run("my messages", func(t *testing.T) {
		unlinked.must(http.StatusForbidden, "GET", "/api/v1/me/messages", nil)
		unlinked.must(http.StatusForbidden, "POST", "/api/v1/me/messages", map[string]any{"targetKind": "extension", "target": "102", "body": "x"})
		// A user cannot broadcast.
		viewer.must(http.StatusBadRequest, "POST", "/api/v1/me/messages", map[string]any{"targetKind": "broadcast", "body": "x", "confirm": true})
		// The sender is the user's own extension, never taken from the body.
		viewer.must(http.StatusBadRequest, "POST", "/api/v1/me/messages", map[string]any{"targetKind": "extension", "target": "102", "body": "from vera", "sender": "999"})
		viewer.must(http.StatusAccepted, "POST", "/api/v1/me/messages", map[string]any{"targetKind": "extension", "target": "102", "body": "from vera"})

		mine := listItems(t, viewer.must(http.StatusOK, "GET", "/api/v1/me/messages", nil))
		theirs := listItems(t, other.must(http.StatusOK, "GET", "/api/v1/me/messages", nil))
		for _, list := range [][]any{mine, theirs} {
			found := false
			for _, m := range list {
				if m.(map[string]any)["body"] == "from vera" {
					found = true
					if m.(map[string]any)["sender"] != "101" {
						t.Errorf("sender = %v, want the user's extension 101", m.(map[string]any)["sender"])
					}
				}
			}
			if !found {
				t.Errorf("a party of the conversation does not see it: %v", list)
			}
		}
		// The operator's message to 101 is visible to vera, and not to olga.
		for _, m := range theirs {
			if m.(map[string]any)["body"] == "hello" {
				t.Errorf("olga sees a message to 101: %v", m)
			}
		}
		// Another user's conversation key does not widen the view.
		leak := listItems(t, other.must(http.StatusOK, "GET", "/api/v1/me/messages?conversation=101%3E102", nil))
		for _, m := range leak {
			if m.(map[string]any)["sender"] == "system" {
				t.Errorf("leaked %v", m)
			}
		}
	})

	t.Run("settings", func(t *testing.T) {
		cur := admin.must(http.StatusOK, "GET", "/api/v1/messages/settings", nil).json(t)
		if cur["ttlHours"] != float64(24) || cur["retentionDays"] != float64(30) || cur["enabled"] != true {
			t.Fatalf("defaults = %v", cur)
		}
		bad := map[string]any{"enabled": true, "ttlHours": 0, "retentionDays": 30, "ratePerMinute": 30, "maxQueuedPerExtension": 100}
		admin.must(http.StatusBadRequest, "PUT", "/api/v1/messages/settings", bad)
		admin.must(http.StatusBadRequest, "PUT", "/api/v1/messages/settings", map[string]any{"enabled": true})
		next := map[string]any{"enabled": true, "ttlHours": 48, "retentionDays": 7, "ratePerMinute": 5, "maxQueuedPerExtension": 10}
		admin.must(http.StatusOK, "PUT", "/api/v1/messages/settings", next)
		if got := admin.must(http.StatusOK, "GET", "/api/v1/messages/settings", nil).json(t); got["retentionDays"] != float64(7) {
			t.Fatalf("settings = %v", got)
		}
		// Off refuses new messages.
		next["enabled"] = false
		admin.must(http.StatusOK, "PUT", "/api/v1/messages/settings", next)
		admin.must(http.StatusConflict, "POST", "/api/v1/messages", map[string]any{"targetKind": "extension", "target": "101", "body": "x"})
		next["enabled"] = true
		admin.must(http.StatusOK, "PUT", "/api/v1/messages/settings", next)
	})

	t.Run("rate limit and purge", func(t *testing.T) {
		// ratePerMinute is 5 now, but the system sender is not limited.
		for i := 0; i < 8; i++ {
			admin.must(http.StatusAccepted, "POST", "/api/v1/messages", map[string]any{"targetKind": "extension", "target": "101", "body": "n"})
		}
		// A user's own sends are limited: 1 already, 4 more fit, then 429.
		for i := 0; i < 4; i++ {
			viewer.must(http.StatusAccepted, "POST", "/api/v1/me/messages", map[string]any{"targetKind": "extension", "target": "102", "body": "n"})
		}
		viewer.must(http.StatusTooManyRequests, "POST", "/api/v1/me/messages", map[string]any{"targetKind": "extension", "target": "102", "body": "n"})
		// Pending messages survive a purge; the audit row names no text.
		got := admin.must(http.StatusOK, "POST", "/api/v1/messages/purge", map[string]any{"olderThanDays": 0}).json(t)
		if got["deleted"] != float64(0) {
			t.Fatalf("purge deleted %v waiting messages", got["deleted"])
		}
		var leaked int
		if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE resource_id LIKE '%drill%' OR action LIKE '%drill%'`).Scan(&leaked); err != nil || leaked != 0 {
			t.Fatalf("audit leaks the text: %d %v", leaked, err)
		}
	})
}
