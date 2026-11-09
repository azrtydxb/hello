package api

import (
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestWebDevicesDatabase fails if, against PostgreSQL, enrolling a web device
// does not store a derived device without any HA1 on the caller's extension
// (and move the configuration revision, so hello-sip learns of it), if the
// per-user cap can be exceeded by concurrent enrolments, if revoking does
// not disable the device and move the revision, if unlinking the extension,
// disabling or deleting the user does not revoke their devices, if an admin
// can rotate a web device's secret or move it to another extension, or if the
// admin list cannot filter by kind.
func TestWebDevicesDatabase(t *testing.T) {
	e := newEnv(t, noLive{})
	admin := e.login()
	alice, aliceID := e.asRole("alice", auth.RoleUser)
	bob, bobID := e.asRole("bob", auth.RoleUser)
	e101 := admin.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	e102 := admin.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Support"}).json(t)
	link := func(uid int64, ext map[string]any) {
		admin.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(uid, 10), map[string]any{"extensionId": ext["id"]})
	}
	link(aliceID, e101)
	link(bobID, e102)
	revision := func() int64 {
		t.Helper()
		rev, err := e.st.ConfigRevision(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return rev
	}

	before := revision()
	made := alice.must(http.StatusCreated, "POST", "/api/v1/me/devices", map[string]any{"label": "Chrome", "platform": "Chrome 130"}).json(t)
	idNum := int64(made["deviceId"].(float64))
	id := strconv.FormatInt(idNum, 10)
	if revision() != before+1 {
		t.Fatalf("revision after enrolling = %d, want %d", revision(), before+1)
	}
	var kind, mode, user string
	var noHA1 bool
	var owner int64
	if err := e.db.QueryRow(`SELECT kind, auth_mode, sip_username, ha1_md5 IS NULL AND ha1_sha256 IS NULL, user_id FROM devices WHERE id = $1`, idNum).
		Scan(&kind, &mode, &user, &noHA1, &owner); err != nil {
		t.Fatal(err)
	}
	if kind != "web" || mode != "derived" || user != "w"+id || !noHA1 || owner != aliceID {
		t.Fatalf("stored device = %s %s %s noHA1=%v owner=%d", kind, mode, user, noHA1, owner)
	}
	if got := made["device"].(map[string]any)["extensionNumber"]; got != "101" {
		t.Fatalf("device on extension %v, want 101", got)
	}

	t.Run("another user cannot reach it", func(t *testing.T) {
		bob.must(http.StatusNotFound, "POST", "/api/v1/me/devices/"+id+"/credentials", nil)
		bob.must(http.StatusNotFound, "DELETE", "/api/v1/me/devices/"+id, nil)
		bob.must(http.StatusNotFound, "POST", "/api/v1/me/devices", map[string]any{"label": "x", "extensionId": e101["id"]})
		if got := listItems(t, bob.must(http.StatusOK, "GET", "/api/v1/me/devices", nil)); len(got) != 0 {
			t.Fatalf("bob sees %v", got)
		}
		alice.must(http.StatusOK, "POST", "/api/v1/me/devices/"+id+"/credentials", nil)
	})

	t.Run("the cap holds under concurrent enrolments", func(t *testing.T) {
		var wg sync.WaitGroup
		codes := make(chan int, 10)
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes <- bob.do("POST", "/api/v1/me/devices", map[string]any{"label": "tab"}).code
			}()
		}
		wg.Wait()
		close(codes)
		created, refused := 0, 0
		for c := range codes {
			switch c {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				refused++
			default:
				t.Errorf("enrolment answered %d", c)
			}
		}
		if created != testClientAuth().MaxDevices || refused != 10-created {
			t.Fatalf("created %d, refused %d; want exactly %d created", created, refused, testClientAuth().MaxDevices)
		}
	})

	t.Run("admin view", func(t *testing.T) {
		web := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/devices?kind=web", nil))
		found := false
		for _, d := range web {
			m := d.(map[string]any)
			if m["kind"] != "web" {
				t.Errorf("kind=web listed %v", m)
			}
			if rowID(m) == id {
				found = true
				if m["userId"] != float64(aliceID) || m["authMode"] != "derived" || m["label"] != "Chrome" {
					t.Errorf("admin view of the device = %v", m)
				}
			}
		}
		if !found {
			t.Fatal("the device is not in the admin list")
		}
		if desk := listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/devices?kind=desk", nil)); len(desk) != 0 {
			t.Fatalf("kind=desk lists %v", desk)
		}
		admin.must(http.StatusBadRequest, "GET", "/api/v1/devices?kind=bogus", nil)
		admin.must(http.StatusConflict, "POST", "/api/v1/devices/"+id+"/rotate-secret", nil)
		admin.must(http.StatusConflict, "PATCH", "/api/v1/devices/"+id, map[string]any{"extensionId": e102["id"]})
	})

	t.Run("revoking disables and moves the revision", func(t *testing.T) {
		before := revision()
		alice.must(http.StatusNoContent, "DELETE", "/api/v1/me/devices/"+id, nil)
		if revision() != before+1 {
			t.Fatalf("revision after revoking = %d, want %d", revision(), before+1)
		}
		var enabled bool
		if err := e.db.QueryRow(`SELECT enabled FROM devices WHERE id = $1`, idNum).Scan(&enabled); err != nil || enabled {
			t.Fatalf("enabled = %v, %v", enabled, err)
		}
		alice.must(http.StatusNotFound, "POST", "/api/v1/me/devices/"+id+"/credentials", nil)
		alice.must(http.StatusNotFound, "DELETE", "/api/v1/me/devices/"+id, nil)
		if got := listItems(t, alice.must(http.StatusOK, "GET", "/api/v1/me/devices", nil)); len(got) != 0 {
			t.Fatalf("a revoked device is listed: %v", got)
		}
	})

	t.Run("losing the extension, being disabled or deleted revokes", func(t *testing.T) {
		active := func(uid int64) int {
			var n int
			if err := e.db.QueryRow(`SELECT count(*) FROM devices WHERE user_id = $1 AND enabled`, uid).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		alice.must(http.StatusCreated, "POST", "/api/v1/me/devices", map[string]any{"label": "again"})
		if active(aliceID) != 1 {
			t.Fatal("setup: no active device")
		}
		// A change that keeps the extension keeps the device.
		admin.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(aliceID, 10), map[string]any{"displayName": "Alice"})
		if active(aliceID) != 1 {
			t.Fatal("an unrelated account change revoked the device")
		}
		before := revision()
		admin.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(aliceID, 10), map[string]any{"extensionIds": []int64{}})
		if active(aliceID) != 0 || revision() <= before {
			t.Fatalf("unlinking the extension: %d active devices, revision %d -> %d", active(aliceID), before, revision())
		}
		admin.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(bobID, 10), map[string]any{"enabled": false})
		if active(bobID) != 0 {
			t.Fatalf("a disabled user keeps %d devices", active(bobID))
		}
		admin.must(http.StatusNoContent, "DELETE", "/api/v1/users/"+strconv.FormatInt(bobID, 10), nil)
		var n int
		if err := e.db.QueryRow(`SELECT count(*) FROM devices WHERE user_id = $1`, bobID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a deleted user keeps %d device rows (%v)", n, err)
		}
	})

	t.Run("ring preferences move the revision", func(t *testing.T) {
		before := revision()
		alice.must(http.StatusOK, "PATCH", "/api/v1/me/profile", map[string]any{"ringWeb": false})
		if revision() != before+1 {
			t.Fatalf("revision after ringWeb = %d, want %d", revision(), before+1)
		}
		me := alice.must(http.StatusOK, "GET", "/api/v1/auth/me", nil).json(t)
		if me["ringWeb"] != false || me["ringMobile"] != true {
			t.Fatalf("me = %v", me)
		}
		// A name change alone does not.
		before = revision()
		alice.must(http.StatusOK, "PATCH", "/api/v1/me/profile", map[string]any{"displayName": "A"})
		if revision() != before {
			t.Fatalf("a display name moved the revision")
		}
	})
}
