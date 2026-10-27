package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/azrtydxb/hello/internal/prov"
)

// TestFirmwareMinimum fails if a pin below the model minimum is accepted or
// rendered, if the minimum rules cannot be read back, if the drift fields
// of a phone are wrong, or if an out-of-range upgrade hour is accepted.
func TestFirmwareMinimum(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	ctx := context.Background()
	mk := func(version string) int64 {
		t.Helper()
		f, err := p.st.CreateFirmware(ctx, "test", prov.Firmware{Vendor: prov.Yealink, ModelGlob: "T5*", Version: version,
			Filename: "T54W-" + version + ".rom", ObjectKey: "yealink/" + version + "/f.rom", SHA256: fmt.Sprintf("%064d", len(version))})
		if err != nil {
			t.Fatal(err)
		}
		return f.ID
	}
	low, high := mk("96.70.0.1"), mk("96.86.0.70")
	pin := func(want int, id int64, extra map[string]any) {
		t.Helper()
		body := map[string]any{"vendor": "yealink", "modelGlob": "T5*", "firmwareId": id}
		for k, v := range extra {
			body[k] = v
		}
		p.c.must(want, "PUT", "/api/v1/prov/firmware/pins", []map[string]any{body})
	}
	rules := func(min string) {
		t.Helper()
		p.c.must(http.StatusOK, "PUT", "/api/v1/prov/firmware/minimums", []map[string]any{
			{"vendor": "yealink", "modelGlob": "T5*", "minVersion": min, "recommended": "96.86.0.70"}})
	}

	// The migration seeds the WP822 floor.
	got := p.c.must(http.StatusOK, "GET", "/api/v1/prov/firmware/minimums", nil).json(t)["items"].([]any)
	var seeded bool
	for _, r := range got {
		m := r.(map[string]any)
		if m["vendor"] == "grandstream" && m["modelGlob"] == "WP822*" && m["minVersion"] == "1.0.11.22" {
			seeded = true
		}
	}
	if !seeded {
		t.Fatalf("WP822 floor missing from %v", got)
	}
	p.c.must(http.StatusBadRequest, "PUT", "/api/v1/prov/firmware/minimums", []map[string]any{{"vendor": "yealink", "modelGlob": "T5*", "minVersion": "not a version"}})
	p.c.must(http.StatusBadRequest, "PUT", "/api/v1/prov/firmware/minimums", []map[string]any{{"vendor": "nope", "modelGlob": "T5*"}})
	p.c.must(http.StatusBadRequest, "PUT", "/api/v1/prov/firmware/minimums", []map[string]any{
		{"vendor": "yealink", "modelGlob": "T5*"}, {"vendor": "yealink", "modelGlob": "T5*"}})
	rules("96.80.0.1")

	pin(http.StatusBadRequest, low, nil)
	pin(http.StatusBadRequest, high, map[string]any{"upgradeHour": 24})
	pin(http.StatusOK, high, map[string]any{"upgradeHour": 3})

	phone := p.phone("805ec0000010", nil)
	id := int64(phone["id"].(float64))
	d, _, err := p.st.RenderInputs(ctx, id)
	if err != nil || d.Firmware == nil || d.Firmware.Window == nil || d.Firmware.Window.Hour != 3 {
		t.Fatalf("render firmware = %+v, %v", d.Firmware, err)
	}

	phoneOf := func() map[string]any {
		return p.c.must(http.StatusOK, "GET", fmt.Sprint("/api/v1/phones/", id), nil).json(t)
	}
	seen := func(v string) {
		t.Helper()
		if _, err := p.db.ExecContext(ctx, `UPDATE phones SET firmware_seen = $1 WHERE id = $2`, v, id); err != nil {
			t.Fatal(err)
		}
	}
	seen("96.86.0.70")
	if ph := phoneOf(); ph["firmwarePinned"] != "96.86.0.70" || ph["firmwareMinimum"] != "96.80.0.1" || ph["firmwareDrift"] != false {
		t.Fatalf("in-sync phone = %v", ph)
	}
	seen("96.85.0.5")
	if ph := phoneOf(); ph["firmwareDrift"] != true {
		t.Fatalf("drift badge missing: %v", ph)
	}
	listed := p.c.must(http.StatusOK, "GET", "/api/v1/phones", nil).json(t)["items"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["firmwareDrift"] != true {
		t.Fatalf("listed phones = %v", listed)
	}

	// Raising the floor above the stored pin stops it rendering.
	rules("96.90.0.1")
	if d, _, err := p.st.RenderInputs(ctx, id); err != nil || d.Firmware != nil {
		t.Fatalf("a pin below the minimum rendered: %+v, %v", d.Firmware, err)
	}
	pin(http.StatusBadRequest, high, nil)
}
