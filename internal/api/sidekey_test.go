package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestSideKeyAndSpeedDialsAPI fails if a phone group's side key is not
// stored or validated, a phone's own side key does not win over its group's
// (or a choice its model lacks is accepted), a Grandstream WP refuses its BLF
// entries or takes more than its speed-dial slots, or the rendered file
// does not carry the side key and the speed dials (phone-feature-parity
// S-12).
func TestSideKeyAndSpeedDialsAPI(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	g := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "ward"}).json(t)
	gid := fmt.Sprint(g["id"])
	if g["sideKey"] != "ptt" {
		t.Errorf("a new group's side key = %v, want ptt", g["sideKey"])
	}
	gpath := "/api/v1/phone-groups/" + gid + "/side-key"
	if got := c.must(http.StatusOK, "PUT", gpath, map[string]any{"sideKey": "none"}).json(t); got["sideKey"] != "none" {
		t.Errorf("group side key = %v", got["sideKey"])
	}
	c.must(http.StatusBadRequest, "PUT", gpath, map[string]any{"sideKey": "panic"})
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/999999/side-key", map[string]any{"sideKey": "ptt"})
	op, _ := p.asRole("sidekey-operator", auth.RoleOperator)
	op.must(http.StatusForbidden, "PUT", gpath, map[string]any{"sideKey": "ptt"})

	// A WP takes its BLF entries as speed dials, up to its slots.
	ph := p.phone("80:5E:C0:AA:BB:41", map[string]any{"vendor": "grandstream", "model": "WP836", "groupId": g["id"], "blf": []string{"102"}})
	pid := fmt.Sprint(ph["id"])
	if ph["speedDialSlots"] != float64(9) || ph["sideKey"] != nil {
		t.Errorf("created WP836 = slots %v, side key %v", ph["speedDialSlots"], ph["sideKey"])
	}
	if ch, _ := ph["sideKeyChoices"].([]any); len(ch) != 3 {
		t.Errorf("WP836 side key choices = %v", ph["sideKeyChoices"])
	}
	ten := make([]string, 10)
	for i := range ten {
		ten[i] = "102"
	}
	c.must(http.StatusBadRequest, "PATCH", "/api/v1/phones/"+pid, map[string]any{"blf": ten})

	preview := func() string {
		return string(c.must(http.StatusOK, "GET", "/api/v1/phones/"+pid+"/preview?file=cfg805ec0aabb41.xml", nil).body)
	}
	body := preview()
	for _, want := range []string{"<P82317>4</P82317>", "<P8690>0</P8690>", "<P8693>102</P8693>"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview lacks %s (group side key none, speed dial 102)", want)
		}
	}
	if strings.Contains(body, "<P323>") {
		t.Error("a WP got a BLF key")
	}

	// The phone's own side key wins; null returns it to the group's.
	if got := c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+pid, map[string]any{"sideKey": "ptt"}).json(t); got["sideKey"] != "ptt" {
		t.Errorf("phone side key = %v", got["sideKey"])
	}
	if !strings.Contains(preview(), "<P82317>0</P82317>") {
		t.Error("the phone's side key does not win over its group's")
	}
	c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+pid, map[string]any{"sideKey": nil})
	if !strings.Contains(preview(), "<P82317>4</P82317>") {
		t.Error("null does not return the phone to its group's side key")
	}
	c.must(http.StatusBadRequest, "PATCH", "/api/v1/phones/"+pid, map[string]any{"sideKey": "panic"})

	// A model without the choice, or without a side key, refuses it.
	wp856 := p.phone("80:5E:C0:AA:BB:42", map[string]any{"vendor": "grandstream", "model": "WP856"})
	c.must(http.StatusBadRequest, "PATCH", "/api/v1/phones/"+fmt.Sprint(wp856["id"]), map[string]any{"sideKey": "alarm"})
	c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+fmt.Sprint(wp856["id"]), map[string]any{"sideKey": "none"})
	yl := p.phone("80:5E:C0:AA:BB:43", nil)
	c.must(http.StatusBadRequest, "PATCH", "/api/v1/phones/"+fmt.Sprint(yl["id"]), map[string]any{"sideKey": "none"})
	c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+fmt.Sprint(yl["id"]), map[string]any{"sideKey": nil})
}
