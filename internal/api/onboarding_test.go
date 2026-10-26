package api

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/prov"
)

// queue records an unadopted phone in the onboarding queue, as the
// provisioning endpoint does.
func (p *provEnv) queue(mac, vendor, model string) {
	p.t.Helper()
	_, err := p.st.CaptureCandidate(context.Background(), prov.Candidate{
		MAC: mac, Vendor: prov.Vendor(vendor), Model: model, Firmware: "1.0.9.69",
		IP: netip.MustParseAddr("192.0.2.20"), Path: "/p/boot/cfg" + mac + ".xml",
	})
	if err != nil {
		p.t.Fatal(err)
	}
}

func (p *provEnv) auditN(action, resource, id string) string {
	return p.scalar(`SELECT count(*) FROM audit_events WHERE action = $1 AND resource = $2 AND resource_id = $3`, action, resource, id)
}

func candidateMACs(t *testing.T, c *client) []string {
	t.Helper()
	var out []string
	for _, it := range listItems(t, c.must(http.StatusOK, "GET", "/api/v1/onboarding/candidates", nil)) {
		out = append(out, it.(map[string]any)["mac"].(string))
	}
	return out
}

// TestOnboardingRoles fails if the onboarding endpoints answer anyone below
// operator, or an unknown MAC path is not a 404.
func TestOnboardingRoles(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	p.queue("000b82000001", "grandstream", "WP822")
	viewer, _ := p.asRole("vera", auth.RoleViewer)
	operator, _ := p.asRole("olga", auth.RoleOperator)
	for _, r := range [][2]string{
		{"GET", "/api/v1/onboarding/candidates"}, {"GET", "/api/v1/onboarding/ignored"},
		{"POST", "/api/v1/onboarding/candidates/000b82000001/adopt"}, {"POST", "/api/v1/onboarding/candidates/000b82000001/dismiss"},
		{"POST", "/api/v1/onboarding/adopt"}, {"DELETE", "/api/v1/onboarding/ignored/000b82000001"},
	} {
		viewer.must(http.StatusForbidden, r[0], r[1], nil)
	}
	if got := candidateMACs(t, operator); len(got) != 1 || got[0] != "000b82000001" {
		t.Fatalf("operator list = %v", got)
	}
	operator.must(http.StatusNotFound, "POST", "/api/v1/onboarding/candidates/nonsense/dismiss", nil)
	operator.must(http.StatusNotFound, "DELETE", "/api/v1/onboarding/ignored/000b82999999", nil)
}

// TestOnboardingAdopt fails if adopting does not create the phone like
// POST /phones (prefilled vendor and model, bound to the chosen extension,
// armed), remove the candidate, audit it, keep the one-time URL out of the
// answer, or if the three ways to name an extension and the refusals do not
// behave.
func TestOnboardingAdopt(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	p.queue("000b82000001", "grandstream", "WP822")
	p.queue("000b82000002", "yealink", "T54W")
	p.queue("000b82000003", "generic", "") // the phone did not report its model
	base := "/api/v1/onboarding/candidates/"

	got := c.must(http.StatusCreated, "POST", base+"000b82000001/adopt", map[string]any{
		"extensionId": p.ext101["id"], "label": "Warehouse"}).json(t)
	if got["mac"] != "000b82000001" || got["vendor"] != "grandstream" || got["model"] != "WP822" ||
		got["extensionNumber"] != "101" || got["label"] != "Warehouse" || got["bootArmed"] != true ||
		got["enabled"] != true || got["provisioningUrl"] != nil {
		t.Fatalf("adopted phone = %v", got)
	}
	if n := p.scalar(`SELECT count(*) FROM onboarding_candidates WHERE mac = '000b82000001'`); n != "0" {
		t.Fatal("the candidate stays in the queue")
	}
	if p.auditN("adopt", "onboarding_candidate", "000b82000001") != "1" || p.auditN("create", "phone", fmt.Sprint(got["id"])) != "1" {
		t.Fatal("adopt not audited")
	}
	// Same result as a phone created by hand: a device on the extension, a hand-off the phone can claim.
	if d := p.scalar(`SELECT d.sip_username FROM phones p JOIN devices d ON d.id = p.device_id WHERE p.mac = '000b82000001'`); d != "101-000001" {
		t.Fatalf("device %q", d)
	}

	// By extension number; the request may override the reported model.
	got = c.must(http.StatusCreated, "POST", base+"000b82000002/adopt", map[string]any{
		"extensionNumber": "102", "model": "T54W-X"}).json(t)
	if got["extensionNumber"] != "102" || got["model"] != "T54W-X" {
		t.Fatalf("by number = %v", got)
	}

	// A model is required when the phone reported none; one of the extension fields too.
	c.must(http.StatusBadRequest, "POST", base+"000b82000003/adopt", map[string]any{"extensionNumber": "101"})
	c.must(http.StatusBadRequest, "POST", base+"000b82000003/adopt", map[string]any{"model": "X1"})
	c.must(http.StatusBadRequest, "POST", base+"000b82000003/adopt", map[string]any{"model": "X1", "extensionNumber": "101", "extensionId": p.ext101["id"]})
	c.must(http.StatusBadRequest, "POST", base+"000b82000003/adopt", map[string]any{"model": "X1", "extensionNumber": "999"})
	c.must(http.StatusBadRequest, "POST", base+"000b82000003/adopt", map[string]any{"model": "X1", "vendor": "acme", "extensionNumber": "101"})
	c.must(http.StatusBadRequest, "POST", base+"000b82000003/adopt", map[string]any{"model": "X1", "extensionNumber": "101", "mac": "000b82000003"})
	// By user: the user's linked extension.
	_, uid := p.asRole("ursula", auth.RoleViewer)
	c.must(http.StatusBadRequest, "POST", base+"000b82000003/adopt", map[string]any{"model": "X1", "userId": uid})
	c.must(http.StatusOK, "PATCH", fmt.Sprintf("/api/v1/users/%d", uid), map[string]any{"extensionId": p.ext102["id"]})
	got = c.must(http.StatusCreated, "POST", base+"000b82000003/adopt", map[string]any{"model": "X1", "userId": uid}).json(t)
	if got["extensionNumber"] != "102" || got["model"] != "X1" {
		t.Fatalf("by user = %v", got)
	}
	// Gone from the queue: a second adopt is a 404.
	c.must(http.StatusNotFound, "POST", base+"000b82000003/adopt", map[string]any{"model": "X1", "extensionNumber": "101"})
	if left := candidateMACs(t, c); len(left) != 0 {
		t.Fatalf("queue = %v", left)
	}
}

// TestOnboardingBulkAdopt fails if consecutive extensions are not assigned
// from the start number in list order, a row that names its own extension is
// not honoured, a failing row does not refuse the whole batch with its
// reason, or the batch is not audited.
func TestOnboardingBulkAdopt(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	for _, n := range []string{"103", "104"} {
		c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": n, "name": "Ext " + n})
	}
	for i := 1; i <= 4; i++ {
		p.queue(fmt.Sprintf("805ec0000%03d", i), "yealink", "T54W")
	}
	row := func(i int, extra map[string]any) map[string]any {
		m := map[string]any{"mac": fmt.Sprintf("805ec0000%03d", i)}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	c.must(http.StatusBadRequest, "POST", "/api/v1/onboarding/adopt", map[string]any{"items": []any{}})
	c.must(http.StatusBadRequest, "POST", "/api/v1/onboarding/adopt", map[string]any{"items": []any{row(1, nil)}}) // no start number
	c.must(http.StatusBadRequest, "POST", "/api/v1/onboarding/adopt", map[string]any{"items": []any{row(1, nil)}, "startNumber": "abc"})

	// 105 does not exist: the whole batch is refused and nothing changes.
	bad := c.must(http.StatusBadRequest, "POST", "/api/v1/onboarding/adopt", map[string]any{
		"items": []any{row(1, nil), row(2, nil), row(3, nil), row(4, nil)}, "startNumber": "102"}).json(t)
	rows := bad["rows"].([]any)
	if bad["ok"] != false || len(rows) != 4 || len(rows[3].(map[string]any)["errors"].([]any)) == 0 ||
		len(rows[0].(map[string]any)["errors"].([]any)) != 0 || rows[0].(map[string]any)["extension"] != "102" {
		t.Fatalf("refused batch = %v", bad)
	}
	if p.scalar(`SELECT count(*) FROM phones`) != "0" || len(candidateMACs(t, c)) != 4 {
		t.Fatal("a refused batch changed something")
	}
	// A duplicate and an unqueued MAC are refused as well.
	c.must(http.StatusBadRequest, "POST", "/api/v1/onboarding/adopt", map[string]any{
		"items": []any{row(1, nil), row(1, nil)}, "startNumber": "101"})
	c.must(http.StatusBadRequest, "POST", "/api/v1/onboarding/adopt", map[string]any{
		"items": []any{row(1, nil), map[string]any{"mac": "805ec0009999"}}, "startNumber": "101"})

	// Row 2 names its own extension; the rest count up from 102 in order.
	ok := c.must(http.StatusOK, "POST", "/api/v1/onboarding/adopt", map[string]any{
		"items":       []any{row(1, map[string]any{"label": "A"}), row(2, map[string]any{"extensionNumber": "101"}), row(3, nil), row(4, nil)},
		"startNumber": "102"}).json(t)
	if ok["ok"] != true || ok["adopted"] != float64(4) {
		t.Fatalf("batch = %v", ok)
	}
	for mac, want := range map[string]string{"805ec0000001": "102", "805ec0000002": "101", "805ec0000003": "103", "805ec0000004": "104"} {
		if got := p.scalar(`SELECT e.number FROM phones p JOIN devices d ON d.id = p.device_id JOIN extensions e ON e.id = d.extension_id WHERE p.mac = $1`, mac); got != want {
			t.Errorf("%s on extension %s, want %s", mac, got, want)
		}
	}
	if len(candidateMACs(t, c)) != 0 || p.auditN("adopt-batch", "onboarding_candidate", "4") != "1" ||
		p.auditN("adopt", "onboarding_candidate", "805ec0000003") != "1" {
		t.Fatal("queue not emptied or batch not audited")
	}
}

// TestOnboardingDismissAndIgnore fails if dismissing does not remove a
// candidate (and audit it), ignoring does not keep the MAC from being queued
// again, or removing it from the ignore list does not let it back in.
func TestOnboardingDismissAndIgnore(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	p.queue("000b82000001", "grandstream", "WP822")
	p.queue("000b82000002", "yealink", "T54W")
	base := "/api/v1/onboarding/candidates/"
	c.must(http.StatusBadRequest, "POST", base+"000b82000001/dismiss", map[string]any{"ignore": "yes"})
	c.must(http.StatusNoContent, "POST", base+"00:0b:82:00:00:01/dismiss", nil)
	c.must(http.StatusNotFound, "POST", base+"000b82000001/dismiss", nil)
	c.must(http.StatusNoContent, "POST", base+"000b82000002/dismiss", map[string]any{"ignore": true})
	if p.auditN("dismiss", "onboarding_candidate", "000b82000001") != "1" || p.auditN("ignore", "onboarding_candidate", "000b82000002") != "1" {
		t.Fatal("not audited")
	}
	p.queue("000b82000002", "yealink", "T54W")
	if got := candidateMACs(t, c); len(got) != 0 {
		t.Fatalf("an ignored MAC is queued again: %v", got)
	}
	ig := listItems(t, c.must(http.StatusOK, "GET", "/api/v1/onboarding/ignored", nil))
	if len(ig) != 1 || ig[0].(map[string]any)["mac"] != "000b82000002" || !strings.HasPrefix(ig[0].(map[string]any)["createdBy"].(string), "user:") {
		t.Fatalf("ignored = %v", ig)
	}
	c.must(http.StatusNoContent, "DELETE", "/api/v1/onboarding/ignored/000b82000002", nil)
	p.queue("000b82000002", "yealink", "T54W")
	if got := candidateMACs(t, c); len(got) != 1 {
		t.Fatalf("after unignore: %v", got)
	}
}
