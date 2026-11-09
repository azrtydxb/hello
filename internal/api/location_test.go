package api

import (
	"fmt"
	"net/http"
	"testing"
)

func site(name string) map[string]any {
	return map[string]any{"kind": "site", "name": name, "country": "NL", "state": "Noord-Holland", "city": "Amsterdam",
		"street": "Damrak", "houseNumber": "1", "postalCode": "1012LG", "latitude": 52.3, "longitude": 4.9, "radiusM": 50}
}

// TestLocationsAPI walks the locations API against PostgreSQL: a site with a
// floor and a room, the validation of each kind, the refusals to delete a
// location in use, the assignment of a phone group, an extension and a phone,
// and the order in which a phone's location is resolved. It fails if a
// floor can sit under a room, a floor carries a civic address, a location in
// use can be deleted, an unknown location can be assigned, or the phone's own
// assignment does not win over its extension's, and that over its group's.
func TestLocationsAPI(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c

	hq := c.must(http.StatusCreated, "POST", "/api/v1/locations", site("HQ")).json(t)
	if hq["kind"] != "site" || hq["parentId"] != nil || hq["city"] != "Amsterdam" || hq["radiusM"] != 50.0 {
		t.Fatalf("site = %v", hq)
	}
	hqPath := fmt.Sprintf("/api/v1/locations/%v", hq["id"])
	floor := c.must(http.StatusCreated, "POST", "/api/v1/locations", map[string]any{"kind": "floor", "name": "3", "parentId": hq["id"]}).json(t)
	room := c.must(http.StatusCreated, "POST", "/api/v1/locations", map[string]any{"kind": "room", "name": "Server room", "parentId": floor["id"],
		"latitude": 52.31, "longitude": 4.91}).json(t)
	if room["kind"] != "room" || room["latitude"] != 52.31 {
		t.Fatalf("room = %v", room)
	}

	for name, body := range map[string]map[string]any{
		"unknown kind":           {"kind": "wing", "name": "x"},
		"no name":                {"kind": "site", "name": "", "country": "NL"},
		"site with no place":     {"kind": "site", "name": "Empty"},
		"floor with no site":     {"kind": "floor", "name": "1"},
		"floor under a room":     {"kind": "floor", "name": "9", "parentId": room["id"]},
		"room under a room":      {"kind": "room", "name": "9", "parentId": room["id"]},
		"floor with address":     {"kind": "floor", "name": "2", "parentId": hq["id"], "city": "Utrecht"},
		"parent does not exist":  {"kind": "floor", "name": "2", "parentId": 999999},
		"bad country":            {"kind": "site", "name": "X", "country": "nl"},
		"latitude alone":         {"kind": "site", "name": "X", "country": "NL", "latitude": 1.0},
		"latitude out":           {"kind": "site", "name": "X", "latitude": 91.0, "longitude": 0.0},
		"radius without point":   {"kind": "site", "name": "X", "country": "NL", "radiusM": 5.0},
		"markup in name":         {"kind": "site", "name": "<b>", "country": "NL"},
		"same name, same parent": {"kind": "floor", "name": "3", "parentId": hq["id"]},
		"unknown field":          {"kind": "site", "name": "X", "country": "NL", "colour": "red"},
	} {
		if r := c.do("POST", "/api/v1/locations", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	if got := c.must(http.StatusOK, "GET", hqPath, nil).json(t); got["id"] != hq["id"] || got["street"] != "Damrak" {
		t.Fatalf("GET site = %v", got)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/locations/999999", nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/locations/abc", nil)

	// A replace clears what is left out, and cannot change the kind.
	got := c.must(http.StatusOK, "PUT", hqPath, map[string]any{"kind": "site", "name": "HQ", "country": "NL", "city": "Utrecht"}).json(t)
	if got["city"] != "Utrecht" || got["street"] != "" || got["latitude"] != nil {
		t.Fatalf("replaced site = %v", got)
	}
	c.must(http.StatusBadRequest, "PUT", hqPath, map[string]any{"kind": "floor", "name": "HQ", "parentId": floor["id"]})
	c.must(http.StatusNotFound, "PUT", "/api/v1/locations/999999", site("Ghost"))
	// A floor cannot be moved into its own room.
	c.must(http.StatusBadRequest, "PUT", fmt.Sprintf("/api/v1/locations/%v", floor["id"]),
		map[string]any{"kind": "floor", "name": "3", "parentId": room["id"]})
	c.must(http.StatusOK, "PUT", hqPath, site("HQ"))

	// Assignments.
	group := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Warehouse"}).json(t)
	phone := p.phone("80:5E:C0:AA:BB:07", nil)
	gAsg := fmt.Sprintf("/api/v1/location-assignments/group/%v", group["id"])
	eAsg := fmt.Sprintf("/api/v1/location-assignments/extension/%v", p.ext101["id"])
	pAsg := fmt.Sprintf("/api/v1/location-assignments/phone/%v", phone["id"])
	c.must(http.StatusNoContent, "PUT", gAsg, map[string]any{"locationId": hq["id"]})
	c.must(http.StatusBadRequest, "PUT", gAsg, map[string]any{"locationId": 999999})
	c.must(http.StatusBadRequest, "PUT", gAsg, map[string]any{"locationId": "x"})
	c.must(http.StatusBadRequest, "PUT", gAsg, map[string]any{})
	c.must(http.StatusNotFound, "PUT", "/api/v1/location-assignments/group/999999", map[string]any{"locationId": hq["id"]})
	c.must(http.StatusNotFound, "PUT", "/api/v1/location-assignments/wall/1", map[string]any{"locationId": hq["id"]})
	c.must(http.StatusNotFound, "PUT", "/api/v1/location-assignments/group/abc", map[string]any{"locationId": hq["id"]})

	list := c.must(http.StatusOK, "GET", "/api/v1/location-assignments", nil).json(t)["items"].([]any)
	assigned := map[string]any{}
	for _, it := range list {
		m := it.(map[string]any)
		assigned[fmt.Sprintf("%v/%v", m["kind"], m["id"])] = m["locationId"]
	}
	if assigned[fmt.Sprintf("group/%v", group["id"])] != hq["id"] || assigned[fmt.Sprintf("extension/%v", p.ext101["id"])] != nil {
		t.Fatalf("assignments = %v", assigned)
	}

	// Resolution: the phone's group, then its extension, then the phone.
	phoneLoc := fmt.Sprintf("/api/v1/phones/%v/location", phone["id"])
	if got := c.must(http.StatusOK, "GET", phoneLoc, nil).json(t); got["source"] != "" || got["place"] != nil {
		t.Fatalf("a phone in no group = %v", got)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/phones/999999/location", nil)
	c.must(http.StatusOK, "PATCH", fmt.Sprintf("/api/v1/phones/%v", phone["id"]), map[string]any{"groupId": group["id"]})
	got = c.must(http.StatusOK, "GET", phoneLoc, nil).json(t)
	if pl, _ := got["place"].(map[string]any); got["source"] != "group" || pl["city"] != "Amsterdam" || pl["floor"] != "" {
		t.Fatalf("group location = %v", got)
	}
	c.must(http.StatusNoContent, "PUT", eAsg, map[string]any{"locationId": floor["id"]})
	got = c.must(http.StatusOK, "GET", phoneLoc, nil).json(t)
	if pl, _ := got["place"].(map[string]any); got["source"] != "extension" || pl["floor"] != "3" || pl["city"] != "Amsterdam" || pl["path"] != "HQ / 3" {
		t.Fatalf("extension location = %v", got)
	}
	c.must(http.StatusNoContent, "PUT", pAsg, map[string]any{"locationId": room["id"]})
	got = c.must(http.StatusOK, "GET", phoneLoc, nil).json(t)
	if pl, _ := got["place"].(map[string]any); got["source"] != "phone" || pl["room"] != "Server room" || pl["latitude"] != 52.31 {
		t.Fatalf("phone location = %v", got)
	}

	// A location in use cannot be deleted; unassigned and emptied it can.
	for _, id := range []any{hq["id"], floor["id"], room["id"]} {
		c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/locations/%v", id), nil)
	}
	c.must(http.StatusNoContent, "PUT", pAsg, map[string]any{"locationId": nil})
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/locations/%v", room["id"]), nil)
	c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/locations/%v", floor["id"]), nil) // the extension
	c.must(http.StatusNoContent, "PUT", eAsg, map[string]any{"locationId": nil})
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/locations/%v", floor["id"]), nil)
	c.must(http.StatusConflict, "DELETE", hqPath, nil) // the group
	c.must(http.StatusNoContent, "PUT", gAsg, map[string]any{"locationId": nil})
	c.must(http.StatusNoContent, "DELETE", hqPath, nil)
	c.must(http.StatusNotFound, "DELETE", hqPath, nil)
}

// TestPhoneGroupEmergencyAPI covers the E911 switch and alarm destination of
// a phone group and the emergency settings. It fails if an alarm destination
// that is neither an extension nor a ring group is accepted, if a setting does
// not read back, or if a group's alarm destination is lost on an unrelated
// update.
func TestPhoneGroupEmergencyAPI(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	group := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Front"}).json(t)
	path := fmt.Sprintf("/api/v1/phone-groups/%v/emergency", group["id"])

	if got := c.must(http.StatusOK, "GET", path, nil).json(t); got["e911Enabled"] != false || got["alarmTarget"] != "" {
		t.Fatalf("new group = %v", got)
	}
	got := c.must(http.StatusOK, "PUT", path, map[string]any{"e911Enabled": true, "alarmTarget": "102"}).json(t)
	if got["e911Enabled"] != true || got["alarmTarget"] != "102" {
		t.Fatalf("after PUT = %v", got)
	}
	// A ring group is a destination too.
	c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", map[string]any{"name": "700", "strategy": "ring-all", "ringTimeout": 20,
		"failureKind": "none", "failureTarget": "", "members": []map[string]any{{"extensionId": p.ext101["id"], "position": 1}}})
	c.must(http.StatusOK, "PUT", path, map[string]any{"e911Enabled": false, "alarmTarget": "700"})
	c.must(http.StatusOK, "PUT", fmt.Sprintf("/api/v1/phone-groups/%v", group["id"]), map[string]any{"name": "Front desk"})
	if got := c.must(http.StatusOK, "GET", path, nil).json(t); got["alarmTarget"] != "700" {
		t.Fatalf("an unrelated update lost the alarm destination: %v", got)
	}
	for name, body := range map[string]map[string]any{
		"no such number": {"e911Enabled": false, "alarmTarget": "555"},
		"not a number":   {"e911Enabled": false, "alarmTarget": "a b"},
		"missing field":  {"e911Enabled": true},
		"extra field":    {"e911Enabled": true, "alarmTarget": "", "x": 1},
	} {
		if r := c.do("PUT", path, body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	c.must(http.StatusOK, "PUT", path, map[string]any{"e911Enabled": false, "alarmTarget": ""})
	c.must(http.StatusNotFound, "GET", "/api/v1/phone-groups/999999/emergency", nil)
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/999999/emergency", map[string]any{"e911Enabled": false, "alarmTarget": ""})

	if got := c.must(http.StatusOK, "GET", "/api/v1/emergency", nil).json(t); got["notifyGroup"] != "" {
		t.Fatalf("settings = %v", got)
	}
	if got := c.must(http.StatusOK, "PUT", "/api/v1/emergency", map[string]any{"notifyGroup": "700"}).json(t); got["notifyGroup"] != "700" {
		t.Fatalf("after PUT = %v", got)
	}
	for name, body := range map[string]map[string]any{
		"an extension, not a ring group": {"notifyGroup": "101"},
		"no such group":                  {"notifyGroup": "555"},
		"not a number":                   {"notifyGroup": "a b"},
		"missing":                        {},
	} {
		if r := c.do("PUT", "/api/v1/emergency", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}
	c.must(http.StatusOK, "PUT", "/api/v1/emergency", map[string]any{"notifyGroup": ""})
}

// TestTrunkGeolocationFlag fails if the geolocation flag of a trunk is not
// stored, is on by default, or is lost by an update that does not mention it.
func TestTrunkGeolocationFlag(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	created := c.must(http.StatusCreated, "POST", "/api/v1/trunks", trunkWith(map[string]any{"mode": "off"})).json(t)
	if created["geolocation"] != false {
		t.Fatalf("default = %v", created["geolocation"])
	}
	path := fmt.Sprintf("/api/v1/trunks/%v", created["id"])
	if got := c.must(http.StatusOK, "PATCH", path, map[string]any{"geolocation": true}).json(t); got["geolocation"] != true {
		t.Fatalf("after PATCH = %v", got["geolocation"])
	}
	if got := c.must(http.StatusOK, "PATCH", path, map[string]any{"maxCalls": 5}).json(t); got["geolocation"] != true {
		t.Fatalf("an unrelated PATCH reset the flag: %v", got["geolocation"])
	}
}
