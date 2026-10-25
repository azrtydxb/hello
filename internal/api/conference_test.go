package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/secret"
)

// fakeConfLive is the live side of the conference routes: published room
// states and the queued control commands.
type fakeConfLive struct {
	mu     sync.Mutex
	states map[string]livestate.ConfRoomState
	cmds   []livestate.ConfCommand
	nodes  []string
}

func (f *fakeConfLive) ConfStates(_ context.Context, numbers []string) (map[string]livestate.ConfRoomState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]livestate.ConfRoomState{}
	for _, n := range numbers {
		if st, ok := f.states[n]; ok {
			out[n] = st
		}
	}
	return out, nil
}

func (f *fakeConfLive) EnqueueConfCommand(_ context.Context, node string, c livestate.ConfCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, c)
	f.nodes = append(f.nodes, node)
	return nil
}

// TestConferenceRooms: create, read, replace and delete a room; the PINs are
// write-only; numbers, caps and PINs are validated; a room an inbound route
// targets cannot be deleted or renumbered; live state and control commands
// reach the right node. It fails if a PIN appears in any response, a DID
// route to a missing room is accepted, or a command is queued for an empty
// room or an unknown participant.
func TestConferenceRooms(t *testing.T) {
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	live := &fakeConfLive{states: map[string]livestate.ConfRoomState{}}
	e := newEnvConfig(t, Config{Objects: newMemObjects(), ConferenceLive: live}, box)
	c := e.login()

	good := map[string]any{"name": "Weekly", "number": "800", "userPin": "1234", "leaderPin": "987654", "maxParticipants": 8, "record": true}
	r := c.must(http.StatusCreated, "POST", "/api/v1/conferences", good)
	created := r.json(t)
	if strings.Contains(string(r.body), "1234") || strings.Contains(string(r.body), "987654") {
		t.Fatalf("a PIN was returned: %s", r.body)
	}
	if created["hasUserPin"] != true || created["hasLeaderPin"] != true || created["maxParticipants"] != float64(8) || created["entryExit"] != "tone" || created["enabled"] != true {
		t.Fatalf("created room = %v", created)
	}
	id := created["id"].(float64)
	idPath := fmt.Sprintf("/api/v1/conferences/%d", int64(id))

	c.must(http.StatusConflict, "POST", "/api/v1/conferences", good) // name and number are unique
	for name, body := range map[string]map[string]any{
		"short number": {"name": "a", "number": "8"},
		"letters":      {"name": "a", "number": "80a"},
		"cap low":      {"name": "a", "number": "801", "maxParticipants": 1},
		"cap high":     {"name": "a", "number": "801", "maxParticipants": 51},
		"short pin":    {"name": "a", "number": "801", "userPin": "12"},
		"same pins":    {"name": "a", "number": "801", "userPin": "1234", "leaderPin": "1234"},
		"bad sound":    {"name": "a", "number": "801", "entryExit": "music"},
		"no announce":  {"name": "a", "number": "801", "entryExit": "announcement"},
		"unknown":      {"name": "a", "number": "801", "extra": true},
		"no number":    {"name": "a"},
	} {
		if r := c.do("POST", "/api/v1/conferences", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	// A number an extension answers to is refused.
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]any{"number": "101", "name": "Alice"})
	c.must(http.StatusBadRequest, "POST", "/api/v1/conferences", map[string]any{"name": "clash", "number": "101"})

	// A route can target the room; a missing room is refused.
	route := map[string]any{"name": "meet", "didKind": "exact", "did": "5551234", "destinationKind": "conference", "destination": "800"}
	routeID := c.must(http.StatusCreated, "POST", "/api/v1/inbound-routes", route).json(t)["id"].(float64)
	route["name"], route["did"], route["destination"] = "ghost", "5559999", "899"
	c.must(http.StatusBadRequest, "POST", "/api/v1/inbound-routes", route)
	// The room cannot be deleted or renumbered while the route targets it.
	c.must(http.StatusConflict, "DELETE", idPath, nil)
	c.must(http.StatusConflict, "PUT", idPath, map[string]any{"name": "Weekly", "number": "802"})

	// Replace keeps a PIN that is left out and clears one set to "".
	put := c.must(http.StatusOK, "PUT", idPath, map[string]any{"name": "Weekly", "number": "800", "leaderPin": "", "muteOnEntry": true}).json(t)
	if put["hasUserPin"] != true || put["hasLeaderPin"] != false || put["muteOnEntry"] != true || put["maxParticipants"] != float64(10) {
		t.Fatalf("replaced room = %v", put)
	}

	// Live state and control.
	c.must(http.StatusConflict, "POST", idPath+"/control", map[string]any{"action": "lock"}) // empty room
	live.mu.Lock()
	live.states["800"] = livestate.ConfRoomState{Room: "800", Node: "sip-2", Participants: []livestate.ConfParticipant{{ID: "p1", Number: "101"}}}
	live.mu.Unlock()
	got := c.must(http.StatusOK, "GET", idPath, nil).json(t)
	if lv, _ := got["live"].(map[string]any); lv == nil || lv["node"] != "sip-2" {
		t.Fatalf("live state missing: %v", got["live"])
	}
	for name, body := range map[string]map[string]any{
		"bad action":   {"action": "explode"},
		"no target":    {"action": "kick"},
		"stray target": {"action": "lock", "participant": "p1"},
	} {
		if r := c.do("POST", idPath+"/control", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	c.must(http.StatusConflict, "POST", idPath+"/control", map[string]any{"action": "kick", "participant": "gone"})
	acc := c.must(http.StatusAccepted, "POST", idPath+"/control", map[string]any{"action": "kick", "participant": "p1"}).json(t)
	if acc["node"] != "sip-2" || len(live.cmds) != 1 || live.cmds[0].Action != "kick" || live.cmds[0].Participant != "p1" || live.nodes[0] != "sip-2" {
		t.Fatalf("command = %v / %+v to %v", acc, live.cmds, live.nodes)
	}
	var audited int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'conference.kick p1' AND resource = 'conference_room'`).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit rows = %d, %v", audited, err)
	}

	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/inbound-routes/%d", int64(routeID)), nil)
	c.must(http.StatusNoContent, "DELETE", idPath, nil)
	c.must(http.StatusNotFound, "GET", idPath, nil)
}
