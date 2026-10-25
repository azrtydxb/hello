package api

import (
	"fmt"
	"net/http"
	"testing"
)

// TestMOHClasses: create, read, replace and delete a music on hold class,
// the single global default, the per extension, ring group and inbound
// route picks, and the validation around them. It fails if a second class
// can stay default, an unknown announcement or class id is stored, a bad
// mode or name is accepted, a duplicate name is accepted, or deleting a
// class leaves a pick behind.
func TestMOHClasses(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	c := e.login()

	c.header.Set("Content-Type", "multipart/form-data; boundary=BND")
	ann := c.must(http.StatusCreated, "POST", "/api/v1/announcements", annMultipart("track1", wav)).json(t)
	ann2 := c.must(http.StatusCreated, "POST", "/api/v1/announcements", annMultipart("track2", wav)).json(t)
	c.header.Del("Content-Type")
	a1, a2 := ann["id"], ann2["id"]

	created := c.must(http.StatusCreated, "POST", "/api/v1/moh-classes", map[string]any{
		"name": "lobby", "mode": "shuffle", "isDefault": true, "announcementIds": []any{a1, a2},
	}).json(t)
	id := fmt.Sprint(created["id"])
	if created["mode"] != "shuffle" || created["isDefault"] != true {
		t.Fatalf("created = %v", created)
	}
	tracks := created["tracks"].([]any)
	if len(tracks) != 2 || tracks[0].(map[string]any)["name"] != "track1" {
		t.Fatalf("tracks = %v, want track1 then track2", tracks)
	}

	// Defaults: loop, not default, no tracks (the built-in tone).
	plain := c.must(http.StatusCreated, "POST", "/api/v1/moh-classes", map[string]any{"name": "plain"}).json(t)
	if plain["mode"] != "loop" || plain["isDefault"] != false || len(plain["tracks"].([]any)) != 0 {
		t.Fatalf("plain = %v", plain)
	}
	plainID := fmt.Sprint(plain["id"])

	// Making another class default clears the first.
	c.must(http.StatusOK, "PUT", "/api/v1/moh-classes/"+plainID, map[string]any{"name": "plain", "isDefault": true})
	if got := c.must(http.StatusOK, "GET", "/api/v1/moh-classes/"+id, nil).json(t); got["isDefault"] != false {
		t.Fatalf("the old default is still default: %v", got)
	}

	// Replace swaps the whole playlist.
	rep := c.must(http.StatusOK, "PUT", "/api/v1/moh-classes/"+id, map[string]any{
		"name": "lobby", "announcementIds": []any{a2},
	}).json(t)
	if tr := rep["tracks"].([]any); len(tr) != 1 || tr[0].(map[string]any)["name"] != "track2" || rep["mode"] != "loop" {
		t.Fatalf("replaced = %v", rep)
	}

	list := c.must(http.StatusOK, "GET", "/api/v1/moh-classes", nil).json(t)
	if len(list["items"].([]any)) != 2 {
		t.Fatalf("list = %v", list)
	}

	// Validation and uniqueness.
	c.must(http.StatusConflict, "POST", "/api/v1/moh-classes", map[string]any{"name": "lobby"})
	c.must(http.StatusConflict, "PUT", "/api/v1/moh-classes/"+plainID, map[string]any{"name": "lobby"})
	for name, body := range map[string]map[string]any{
		"no name":          {"mode": "loop"},
		"empty name":       {"name": ""},
		"bad mode":         {"name": "x", "mode": "random"},
		"unknown track":    {"name": "x", "announcementIds": []int{999999}},
		"unknown property": {"name": "x", "extra": 1},
	} {
		if r := c.do("POST", "/api/v1/moh-classes", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	c.must(http.StatusBadRequest, "PUT", "/api/v1/moh-classes/"+id, map[string]any{"mode": "loop"})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/moh-classes/"+id, map[string]any{"name": "lobby", "mode": "nope"})
	c.must(http.StatusNotFound, "GET", "/api/v1/moh-classes/999999", nil)
	c.must(http.StatusNotFound, "PUT", "/api/v1/moh-classes/999999", map[string]any{"name": "ghost"})
	c.must(http.StatusNotFound, "DELETE", "/api/v1/moh-classes/999999", nil)

	// Picks: an extension, a ring group and an inbound route name a class;
	// an unknown class is a field error; null clears it.
	extPath := fmt.Sprintf("/api/v1/extensions/%v", e.ext101["id"])
	got := c.must(http.StatusOK, "PATCH", extPath, map[string]any{"mohClassId": idNum(id)}).json(t)
	if fmt.Sprint(got["mohClassId"]) != id {
		t.Fatalf("extension mohClassId = %v, want %s", got["mohClassId"], id)
	}
	c.must(http.StatusBadRequest, "PATCH", extPath, map[string]any{"mohClassId": 999999})

	group := c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", map[string]any{
		"name": "700", "members": []map[string]any{{"extensionId": e.ext101["id"], "position": 1}},
		"mohClassId": idNum(id), "mohRingback": true,
	}).json(t)
	if fmt.Sprint(group["mohClassId"]) != id || group["mohRingback"] != true {
		t.Fatalf("ring group = %v", group)
	}
	gPath := fmt.Sprintf("/api/v1/ring-groups/%v", group["id"])
	c.must(http.StatusBadRequest, "PATCH", gPath, map[string]any{"mohClassId": 999999})
	if g := c.must(http.StatusOK, "PATCH", gPath, map[string]any{"name": "701"}).json(t); fmt.Sprint(g["mohClassId"]) != id || g["mohRingback"] != true {
		t.Fatalf("an unrelated patch dropped the music settings: %v", g)
	}

	route := c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "main", "destinationKind": "extension", "destination": "101",
		"mohClassId": idNum(id), "mohRingback": true,
	}).json(t)
	if fmt.Sprint(route["mohClassId"]) != id || route["mohRingback"] != true {
		t.Fatalf("inbound route = %v", route)
	}
	rPath := fmt.Sprintf("/api/v1/routes/inbound/%v", route["id"])
	c.must(http.StatusBadRequest, "PATCH", rPath, map[string]any{"mohClassId": 999999})

	// Deleting the class clears every pick.
	c.must(http.StatusNoContent, "DELETE", "/api/v1/moh-classes/"+id, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/moh-classes/"+id, nil)
	if x := c.must(http.StatusOK, "GET", extPath, nil).json(t); x["mohClassId"] != nil {
		t.Errorf("extension still names the deleted class: %v", x["mohClassId"])
	}
	if g := c.must(http.StatusOK, "GET", gPath, nil).json(t); g["mohClassId"] != nil {
		t.Errorf("ring group still names the deleted class: %v", g["mohClassId"])
	}
	if r := c.must(http.StatusOK, "GET", rPath, nil).json(t); r["mohClassId"] != nil {
		t.Errorf("inbound route still names the deleted class: %v", r["mohClassId"])
	}

	// null clears a pick.
	c.must(http.StatusOK, "PATCH", extPath, map[string]any{"mohClassId": idNum(plainID)})
	if x := c.must(http.StatusOK, "PATCH", extPath, map[string]any{"mohClassId": nil}).json(t); x["mohClassId"] != nil {
		t.Errorf("null did not clear the extension's class: %v", x["mohClassId"])
	}
}

// idNum turns a decimal id string back into a number for a request body.
func idNum(id string) any {
	var n int64
	_, _ = fmt.Sscan(id, &n)
	return n
}
