package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

// TestBossLinks: the link list is replaced as a whole, validated, kept when an
// extension goes away only as far as the extension does, and the divert that
// *64 sets (hello-sip's feature update) shows in the list. It fails if a rule
// of the list is not enforced, a boss that is no longer in the list keeps its
// link, or the divert update does not reach the stored link.
func TestBossLinks(t *testing.T) {
	e := newEnvConfig(t, Config{}, nil)
	c := e.login()
	ctx := context.Background()
	mk := func(number, name string) float64 {
		return c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": number, "name": name}).json(t)["id"].(float64)
	}
	boss, assistant, other := mk("200", "Boss"), mk("300", "Assistant"), mk("210", "Other")

	if l := c.must(http.StatusOK, "GET", "/api/v1/boss-links", nil).json(t)["items"].([]any); len(l) != 0 {
		t.Fatalf("a fresh install has links: %v", l)
	}
	link := func(b, a float64, extra map[string]any) map[string]any {
		m := map[string]any{"bossExtensionId": b, "assistantExtensionId": a, "ringMode": "both"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	put := func(want int, items ...map[string]any) response {
		return c.must(want, "PUT", "/api/v1/boss-links", map[string]any{"items": items})
	}

	// Refused: the rules of the list.
	for name, items := range map[string][]map[string]any{
		"boss is its own assistant": {link(boss, boss, nil)},
		"a boss has one assistant":  {link(boss, assistant, nil), link(boss, other, nil)},
		"unknown extension":         {link(99999, assistant, nil)},
		"unknown assistant":         {link(boss, 99999, nil)},
		"bad ring mode":             {link(boss, assistant, map[string]any{"ringMode": "random"})},
		"missing boss":              {{"assistantExtensionId": assistant, "ringMode": "both"}},
		"two outbound identities": {
			link(boss, assistant, map[string]any{"outboundAsBoss": true}),
			link(other, assistant, map[string]any{"outboundAsBoss": true}),
		},
	} {
		if r := c.do("PUT", "/api/v1/boss-links", map[string]any{"items": items}); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	c.must(http.StatusBadRequest, "PUT", "/api/v1/boss-links", map[string]any{"items": []map[string]any{link(boss, assistant, map[string]any{"extra": 1})}})

	// Stored: defaults, the display fields, and an assistant for two bosses.
	got := put(http.StatusOK,
		link(boss, assistant, map[string]any{"screening": true, "ringMode": "assistant_first"}),
		link(other, assistant, map[string]any{"outboundAsBoss": true}),
	).json(t)["items"].([]any)
	if len(got) != 2 {
		t.Fatalf("stored %d links, want 2: %v", len(got), got)
	}
	var l200 map[string]any
	for _, g := range got {
		if m := g.(map[string]any); m["bossNumber"] == "200" {
			l200 = m
		}
	}
	if l200["assistantNumber"] != "300" || l200["ringMode"] != "assistant_first" || l200["screening"] != true ||
		l200["enabled"] != true || l200["divertOn"] != false || l200["outboundAsBoss"] != false || l200["bossName"] != "Boss" {
		t.Fatalf("link of 200 = %v", l200)
	}

	// *64 from hello-sip flips the divert of the boss's link.
	on := true
	if err := e.st.ApplyFeatureUpdate(ctx, "hello-sip", store.FeatureUpdate{Number: "200", BossDivert: &on}); err != nil {
		t.Fatalf("divert on: %v", err)
	}
	for _, g := range c.must(http.StatusOK, "GET", "/api/v1/boss-links", nil).json(t)["items"].([]any) {
		m := g.(map[string]any)
		if (m["bossNumber"] == "200") != (m["divertOn"] == true) {
			t.Fatalf("divert after *64: %v", m)
		}
	}
	// An extension that is no boss has no divert to set.
	if err := e.st.ApplyFeatureUpdate(ctx, "hello-sip", store.FeatureUpdate{Number: "300", BossDivert: &on}); err == nil {
		t.Fatal("a divert for an extension that is no boss was accepted")
	}

	// Replacing the list removes the links whose boss is absent.
	left := put(http.StatusOK, link(other, assistant, nil)).json(t)["items"].([]any)
	if len(left) != 1 || left[0].(map[string]any)["bossNumber"] != "210" {
		t.Fatalf("after the replace: %v", left)
	}
	// Deleting an extension takes its links with it.
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/extensions/%v", other), nil)
	if l := c.must(http.StatusOK, "GET", "/api/v1/boss-links", nil).json(t)["items"].([]any); len(l) != 0 {
		t.Fatalf("a link outlived its boss: %v", l)
	}
	put(http.StatusOK)

	// The *64 code is a feature code action.
	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*64", "action": "boss_divert"},
	}})
}
