package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/store"
)

// TestPhoneLines fails if a further line of a phone cannot be bound to a
// device of another extension, if the bound device's secret is not sealed or
// is returned, if a line is accepted that the model does not have, a device
// is bound to two lines, or a phone gets two lines on one extension, if two
// phones cannot share an extension (a shared line appearance), if a device
// bound as a line can be deleted from under the phone, if a rotated secret is
// not re-sealed, or if the preview omits the line or shows its secret
// (phone-feature-parity S-10).
func TestPhoneLines(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	ext103 := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "103", "name": "Sales 2"}).json(t)

	first := p.phone("0004f2000010", nil)
	id := fmt.Sprint(first["id"])
	lines := "/api/v1/phones/" + id + "/lines/"
	if ls, _ := first["lines"].([]any); len(ls) != 1 || first["maxLines"] != float64(6) {
		t.Fatalf("a new phone has lines %v, maxLines %v, want line 1 only and 6", first["lines"], first["maxLines"])
	}

	// Line 2 on extension 102.
	got := c.must(http.StatusOK, "PUT", lines+"2", map[string]any{"extensionId": p.ext102["id"]}).json(t)
	ls, _ := got["lines"].([]any)
	if len(ls) != 2 || got["secretRotated"] == true {
		t.Fatalf("after binding line 2: lines %v", got["lines"])
	}
	l2 := ls[1].(map[string]any)
	if l2["line"] != float64(2) || l2["extensionNumber"] != "102" || l2["sipUsername"] != "102-000010" || l2["provisioned"] != true {
		t.Fatalf("line 2 = %v", l2)
	}
	devID := int64(l2["deviceId"].(float64))
	var enc []byte
	if err := p.db.QueryRow(`SELECT secret_enc FROM devices WHERE id = $1`, devID).Scan(&enc); err != nil || enc == nil {
		t.Fatalf("line 2's device has no sealed secret: %v", err)
	}
	sec, err := p.box.Open(enc, prov.DeviceSecretAAD(devID))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/phones", "/api/v1/phones/" + id, "/api/v1/devices"} {
		if r := c.must(http.StatusOK, "GET", path, nil); bytes.Contains(r.body, []byte(sec)) {
			t.Fatalf("GET %s reveals line 2's secret", path)
		}
	}
	if gj, _ := got["lines"].([]any); len(gj) != 2 {
		t.Fatal("unexpected")
	}

	// The preview writes the line, with its secret masked.
	pv := c.must(http.StatusOK, "GET", "/api/v1/phones/"+id+"/preview?file=0004f2000010.cfg", nil)
	body := string(pv.body)
	for _, w := range []string{"account.2.user_name = 102-000010", "account.2.password = " + mask, "account.2.label = 102 (Support)"} {
		if !strings.Contains(body, w) {
			t.Errorf("preview lacks %q", w)
		}
	}
	if strings.Contains(body, sec) {
		t.Fatal("the preview shows line 2's secret")
	}

	// Refusals.
	c.must(http.StatusBadRequest, "PUT", lines+"1", map[string]any{"extensionId": p.ext102["id"]})
	c.must(http.StatusBadRequest, "PUT", lines+"7", map[string]any{"extensionId": p.ext102["id"]})
	c.must(http.StatusBadRequest, "PUT", lines+"x", map[string]any{"extensionId": p.ext102["id"]})
	c.must(http.StatusBadRequest, "PUT", lines+"3", map[string]any{})
	c.must(http.StatusBadRequest, "PUT", lines+"3", map[string]any{"extensionId": ext103["id"], "deviceId": 0})
	c.must(http.StatusBadRequest, "PUT", lines+"3", map[string]any{"extensionId": 999999})
	c.must(http.StatusBadRequest, "PUT", lines+"3", map[string]any{"extensionId": p.ext101["id"]}) // line 1 is on 101
	c.must(http.StatusBadRequest, "PUT", lines+"3", map[string]any{"extensionId": p.ext102["id"]}) // line 2 is on 102
	c.must(http.StatusNotFound, "PUT", "/api/v1/phones/999999/lines/2", map[string]any{"extensionId": p.ext102["id"]})
	c.must(http.StatusNotFound, "DELETE", "/api/v1/phones/999999/lines/2", nil)
	c.must(http.StatusNotFound, "DELETE", lines+"5", nil)
	c.must(http.StatusBadRequest, "DELETE", lines+"1", nil)

	// A device is one line of one phone.
	other := p.phone("0004f2000011", map[string]any{"extensionId": ext103["id"]})
	otherLines := "/api/v1/phones/" + fmt.Sprint(other["id"]) + "/lines/"
	c.must(http.StatusConflict, "PUT", otherLines+"2", map[string]any{"extensionId": p.ext102["id"], "deviceId": devID})
	// A device that is line 1 of a phone is not a second phone's line.
	c.must(http.StatusConflict, "PUT", otherLines+"2", map[string]any{"extensionId": p.ext101["id"], "deviceId": int64(first["deviceId"].(float64))})

	// Two phones may each have a line on extension 102: a shared line
	// appearance, with a device each.
	shared := c.must(http.StatusOK, "PUT", otherLines+"2", map[string]any{"extensionId": p.ext102["id"]}).json(t)
	sl := shared["lines"].([]any)[1].(map[string]any)
	if sl["extensionNumber"] != "102" || sl["deviceId"] == l2["deviceId"] || sl["sipUsername"] != "102-000011" {
		t.Fatalf("shared line = %v, want its own device on 102", sl)
	}
	if n := p.scalar(`SELECT count(*) FROM devices WHERE extension_id = $1 AND secret_enc IS NOT NULL`, fmt.Sprint(p.ext102["id"])); n != "2" {
		t.Fatalf("%s sealed devices on 102, want 2", n)
	}

	// A bound line's device is not deleted from under the phone, and a
	// rotated secret is sealed again.
	r := c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/devices/%d", devID), nil)
	if !bytes.Contains(r.body, []byte("00:04:f2:00:00:10")) {
		t.Fatalf("409 does not name the phone: %s", r.body)
	}
	c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/extensions/%v", p.ext102["id"]), nil)
	rot := c.must(http.StatusOK, "POST", fmt.Sprintf("/api/v1/devices/%d/rotate-secret", devID), nil).json(t)
	if err := p.db.QueryRow(`SELECT secret_enc FROM devices WHERE id = $1`, devID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if s2, err := p.box.Open(enc, prov.DeviceSecretAAD(devID)); err != nil || s2 != rot["secret"] {
		t.Fatalf("rotate-secret did not re-seal line 2's device: %v", err)
	}

	// A model with one line takes no second.
	one := p.phone("0004f2000012", map[string]any{"model": "T19P", "extensionId": ext103["id"]})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/phones/"+fmt.Sprint(one["id"])+"/lines/2", map[string]any{"extensionId": p.ext101["id"]})
	// A phone with lines does not change to a model with fewer.
	c.must(http.StatusConflict, "PATCH", "/api/v1/phones/"+id, map[string]any{"model": "T19P"})
	// A WP822 takes two lines, not three, and writes the second.
	wp := p.phone("0004f2000013", map[string]any{"vendor": "grandstream", "model": "WP822"})
	wpLines := "/api/v1/phones/" + fmt.Sprint(wp["id"]) + "/lines/"
	w2 := c.must(http.StatusOK, "PUT", wpLines+"2", map[string]any{"extensionId": p.ext102["id"]}).json(t)
	if wl := w2["lines"].([]any)[1].(map[string]any); wl["provisioned"] != true {
		t.Fatalf("a Grandstream line 2 is provisioned = %v, want true: the WP822 template has the account 2 keys", wl["provisioned"])
	}
	c.must(http.StatusBadRequest, "PUT", wpLines+"3", map[string]any{"extensionId": ext103["id"]})

	// Clearing a line drops the device's sealed secret and the line.
	cleared := c.must(http.StatusOK, "DELETE", lines+"2", nil).json(t)
	if len(cleared["lines"].([]any)) != 1 {
		t.Fatalf("after clearing: %v", cleared["lines"])
	}
	if n := p.scalar(`SELECT count(*) FROM devices WHERE id = $1 AND secret_enc IS NULL AND ha1_md5 <> ''`, fmt.Sprint(devID)); n != "1" {
		t.Fatal("clearing the line left the sealed secret or dropped the HA1")
	}
	if pv := c.must(http.StatusOK, "GET", "/api/v1/phones/"+id+"/preview?file=0004f2000010.cfg", nil); strings.Contains(string(pv.body), "account.2.") {
		t.Fatal("the preview still writes the cleared line")
	}
	// The device is free again, and deleting a phone frees its lines' devices.
	c.must(http.StatusOK, "PUT", lines+"2", map[string]any{"extensionId": p.ext102["id"], "deviceId": devID})
	c.must(http.StatusNoContent, "DELETE", "/api/v1/phones/"+id, nil)
	if n := p.scalar(`SELECT count(*) FROM phone_lines WHERE device_id = $1`, fmt.Sprint(devID)); n != "0" {
		t.Fatal("deleting the phone left its line")
	}
	if n := p.scalar(`SELECT count(*) FROM devices WHERE id = $1 AND secret_enc IS NULL`, fmt.Sprint(devID)); n != "1" {
		t.Fatal("deleting the phone left line 2's sealed secret")
	}
	if n := p.scalar(`SELECT count(*) FROM audit_events WHERE action IN ('set-line', 'clear-line')`); n != "5" {
		t.Fatalf("%s line audit rows, want 5: four binds and one clear", n)
	}
}

// TestPhoneCheckSyncLines fails if the push job of a phone omits the devices
// of its further lines.
func TestPhoneCheckSyncLines(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	ph := p.phone("0004f2000020", nil)
	c.must(http.StatusOK, "PUT", "/api/v1/phones/"+fmt.Sprint(ph["id"])+"/lines/2", map[string]any{"extensionId": p.ext102["id"]})
	c.must(http.StatusAccepted, "POST", "/api/v1/phones/"+fmt.Sprint(ph["id"])+"/resync", nil)
	jobs, err := p.st.ClaimPushes(context.Background(), "sip-1", 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim = %v, %v", jobs, err)
	}
	if jobs[0].Device != "101-000020" || len(jobs[0].Lines) != 1 || jobs[0].Lines[0] != "102-000020" {
		t.Fatalf("job = %+v, want device 101-000020 and line 102-000020", jobs[0])
	}
}

// TestAccountSharing fails if a phone group's account sharing cannot be
// switched, if the setting does not reach a WP8x6's preview as P22521 or
// P22522 by its role, if it reaches a phone of a group that did not enable
// it, or if a bad role is accepted (phone-feature-parity S-11).
func TestAccountSharing(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	ctx := context.Background()
	grp, err := p.st.CreatePhoneGroup(ctx, "test", store.GroupInput{Name: "Warehouse"})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/phone-groups/%d/account-sharing", grp.ID)
	if grp.AccountSharing {
		t.Fatal("a new group has account sharing on")
	}
	ph := p.phone("0015651234ab", map[string]any{"vendor": "grandstream", "model": "WP836", "groupId": grp.ID})
	if ph["sharingRole"] != "host" {
		t.Fatalf("default role = %v, want host", ph["sharingRole"])
	}
	pid := fmt.Sprint(ph["id"])
	preview := func() string {
		return string(c.must(http.StatusOK, "GET", "/api/v1/phones/"+pid+"/preview?file=cfg0015651234ab.xml", nil).body)
	}
	if b := preview(); !strings.Contains(b, "<P22521>0</P22521>") || strings.Contains(b, "P22522") {
		t.Fatal("a group that did not enable account sharing renders anything but P22521 = 0")
	}

	c.must(http.StatusBadRequest, "PUT", path, map[string]any{})
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/999999/account-sharing", map[string]any{"enabled": true})
	on := c.must(http.StatusOK, "PUT", path, map[string]any{"enabled": true}).json(t)
	if on["accountSharing"] != true {
		t.Fatalf("group = %v", on)
	}
	if b := preview(); !strings.Contains(b, "<P22521>1</P22521>") || !strings.Contains(b, "<P22522>0</P22522>") {
		t.Fatal("the host does not render P22521 = 1 and P22522 = 0")
	}
	c.must(http.StatusBadRequest, "PATCH", "/api/v1/phones/"+pid, map[string]any{"sharingRole": "owner"})
	if g := c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+pid, map[string]any{"sharingRole": "guest"}).json(t); g["sharingRole"] != "guest" {
		t.Fatalf("role = %v", g["sharingRole"])
	}
	if b := preview(); !strings.Contains(b, "<P22521>1</P22521>") || !strings.Contains(b, "<P22522>1</P22522>") {
		t.Fatal("the guest does not render P22521 = 1 and P22522 = 1")
	}

	// A WP822 and a phone of another vendor in the group get neither key.
	wp822 := p.phone("0015651234ac", map[string]any{"vendor": "grandstream", "model": "WP822", "groupId": grp.ID})
	if b := string(c.must(http.StatusOK, "GET", "/api/v1/phones/"+fmt.Sprint(wp822["id"])+"/preview?file=cfg0015651234ac.xml", nil).body); strings.Contains(b, "P22521") {
		t.Fatal("a WP822 renders account sharing")
	}
	off := c.must(http.StatusOK, "PUT", path, map[string]any{"enabled": false}).json(t)
	if off["accountSharing"] != false {
		t.Fatalf("group = %v", off)
	}
	if b := preview(); !strings.Contains(b, "<P22521>0</P22521>") || strings.Contains(b, "P22522") {
		t.Fatal("switching account sharing off left it on in the configuration")
	}
}

// TestAccountSharingSecret fails if a phone group is created without its own
// sealed account sharing password, if the password is the shared default
// "AccountSharing", is returned by any response or preview, does not reach a
// WP8x6's configuration as P22523, does not change on regeneration, is not
// made for a group that predates it, or if regeneration of a missing group
// is not a 404.
func TestAccountSharingSecret(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	ctx := context.Background()
	mk := func(name string) int64 {
		g, err := p.st.CreatePhoneGroup(ctx, "test", store.GroupInput{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return g.ID
	}
	sealed := func(id int64) []byte {
		var b []byte
		if err := p.db.QueryRowContext(ctx, `SELECT account_sharing_secret_enc FROM phone_groups WHERE id = $1`, id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	open := func(id int64) string {
		b := sealed(id)
		if b == nil {
			t.Fatalf("group %d has no sealed account sharing secret", id)
		}
		pw, err := p.box.Open(b, prov.SharingSecretAAD(id))
		if err != nil {
			t.Fatalf("group %d: %v", id, err)
		}
		return pw
	}
	a, b := mk("North"), mk("South")
	pwA, pwB := open(a), open(b)
	if pwA == "" || pwA == "AccountSharing" || pwA == pwB {
		t.Fatalf("groups must have their own generated secrets, got the same or the default")
	}
	if bytes.Contains(sealed(a), []byte(pwA)) {
		t.Fatal("the secret is stored in the clear")
	}

	ph := p.phone("0015651234ba", map[string]any{"vendor": "grandstream", "model": "WP836", "groupId": a})
	pid := fmt.Sprint(ph["id"])
	preview := func() string {
		return string(c.must(http.StatusOK, "GET", "/api/v1/phones/"+pid+"/preview?file=cfg0015651234ba.xml", nil).body)
	}
	path := fmt.Sprintf("/api/v1/phone-groups/%d/account-sharing", a)
	on := c.must(http.StatusOK, "PUT", path, map[string]any{"enabled": true})
	if strings.Contains(string(on.body), pwA) || strings.Contains(strings.ToLower(string(on.body)), "secret") {
		t.Fatal("the group response shows the account sharing secret")
	}
	if pv := preview(); !strings.Contains(pv, "<P22523>********</P22523>") || strings.Contains(pv, pwA) {
		t.Fatal("the preview must carry P22523 with the secret masked")
	}
	if g := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/phone-groups/%d", a), nil); strings.Contains(string(g.body), pwA) {
		t.Fatal("the group shows the account sharing secret")
	}

	c.must(http.StatusNotFound, "POST", "/api/v1/phone-groups/999999/account-sharing/regenerate", nil)
	re := c.must(http.StatusOK, "POST", path+"/regenerate", nil)
	if strings.Contains(string(re.body), pwA) || strings.Contains(strings.ToLower(string(re.body)), "secret") {
		t.Fatal("the regenerate response shows a secret")
	}
	newA := open(a)
	if newA == pwA || newA == "AccountSharing" {
		t.Fatal("regeneration kept the old secret")
	}
	if open(b) != pwB {
		t.Fatal("regenerating one group changed another")
	}

	// A group that predates the secret is given one when a phone renders.
	if _, err := p.db.ExecContext(ctx, `UPDATE phone_groups SET account_sharing_secret_enc = NULL WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	if pv := preview(); !strings.Contains(pv, "<P22523>") {
		t.Fatal("a group without a secret renders no P22523")
	}
	if open(a) == "" {
		t.Fatal("no secret was made for the older group")
	}
}
