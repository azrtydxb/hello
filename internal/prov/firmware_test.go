package prov

import (
	"context"
	"strings"
	"testing"
)

// TestFirmwareMinimum fails if a version below a model's minimum is not
// recognised, if a pin glob that can reach a floored model escapes its
// minimum, if drift is missed or invented, or if a pin's upgrade window does
// not reach the Grandstream config (and only there).
func TestFirmwareMinimum(t *testing.T) {
	t.Run("compare", func(t *testing.T) {
		for _, c := range []struct {
			a, b string
			want int
		}{
			{"1.0.11.22", "1.0.11.22", 0},
			{"1.0.11.9", "1.0.11.22", -1},
			{"1.0.11.78", "1.0.11.22", 1},
			{"1.0.3.39", "1.0.11.22", -1},
			{"1.0.11", "1.0.11.0", 0},
			{"96.86.0.70", "96.86.0.7", 1},
			{"1.0.11.22b", "1.0.11.22", 1},
		} {
			if got := CompareVersions(c.a, c.b); got != c.want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
			}
			if got := CompareVersions(c.b, c.a); got != -c.want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
			}
		}
	})

	rules := []FirmwareMinimum{
		{Vendor: Grandstream, ModelGlob: "WP822*", MinVersion: "1.0.11.22", Recommended: "1.0.11.78"},
		{Vendor: Grandstream, ModelGlob: "WP8?6*", Recommended: "1.0.3.39"},
		{Vendor: Yealink, ModelGlob: "T5*", MinVersion: "96.80.0.1"},
		{Vendor: Yealink, ModelGlob: "T54W", MinVersion: "96.86.0.1"},
	}
	t.Run("model", func(t *testing.T) {
		for _, c := range []struct {
			v     Vendor
			model string
			want  string
		}{
			{Grandstream, "WP822", "1.0.11.22"},
			{Grandstream, "WP836", ""},
			{Grandstream, "GXP2130", ""},
			{Yealink, "T54W", "96.86.0.1"},
			{Yealink, "T57W", "96.80.0.1"},
			{Snom, "WP822", ""},
		} {
			if got := MinimumFor(rules, c.v, c.model); got != c.want {
				t.Errorf("MinimumFor(%s, %s) = %q, want %q", c.v, c.model, got, c.want)
			}
		}
	})
	t.Run("pin", func(t *testing.T) {
		for _, c := range []struct {
			v    Vendor
			glob string
			want string
		}{
			{Grandstream, "WP822", "1.0.11.22"},
			{Grandstream, "WP822*", "1.0.11.22"},
			{Grandstream, "WP8*", "1.0.11.22"}, // reaches the WP822
			{Grandstream, "*", "1.0.11.22"},
			{Grandstream, "WP836", ""},
			{Yealink, "T5*", "96.86.0.1"},
			{Yealink, "T4*", ""},
		} {
			if got := MinimumForPin(rules, c.v, c.glob); got != c.want {
				t.Errorf("MinimumForPin(%s, %s) = %q, want %q", c.v, c.glob, got, c.want)
			}
		}
	})
	t.Run("drift", func(t *testing.T) {
		for _, c := range []struct {
			seen, pinned, min string
			want              bool
		}{
			{"1.0.11.78", "1.0.11.78", "1.0.11.22", false},
			{"1.0.11.75", "1.0.11.78", "1.0.11.22", true}, // differs from the pin
			{"1.0.11.20", "", "1.0.11.22", true},          // below the floor, nothing pinned
			{"1.0.11.30", "", "1.0.11.22", false},
			{"", "1.0.11.78", "1.0.11.22", false}, // never showed a version
			{"96.86.0.70", "96.86.0.070", "", false},
			{"96.86.0.70", "", "", false},
		} {
			if got := FirmwareDrift(c.seen, c.pinned, c.min); got != c.want {
				t.Errorf("FirmwareDrift(%q, %q, %q) = %v, want %v", c.seen, c.pinned, c.min, got, c.want)
			}
		}
	})
}

// TestFirmwareUpgradeWindow fails if a pin's window does not make the
// Grandstream phone upgrade daily at that hour, if a pin without one changes
// the existing keys, or if the WP856, whose window key is unverified, gets
// the unverified keys.
func TestFirmwareUpgradeWindow(t *testing.T) {
	render := func(model string, window *UpgradeWindow) string {
		t.Helper()
		d := renderSample(true)
		d.Phone.Vendor, d.Phone.Model = Grandstream, model
		d.Firmware.Window = window
		out, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	has := func(t *testing.T, s string, want, notWant []string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(s, w) {
				t.Errorf("missing %q", w)
			}
		}
		for _, w := range notWant {
			if strings.Contains(s, w) {
				t.Errorf("unexpected %q", w)
			}
		}
	}
	has(t, render("WP822", &UpgradeWindow{Hour: 3}), []string{"<P194>2</P194>", "<P285>3</P285>", "<P238>1</P238>"}, []string{"<P194>1</P194>", "<P238>0</P238>"})
	has(t, render("WP836", &UpgradeWindow{Hour: 0}), []string{"<P194>2</P194>", "<P285>0</P285>", "<P238>1</P238>"}, nil)
	has(t, render("WP822", nil), []string{"<P194>1</P194>", "<P238>0</P238>"}, []string{"<P285>"})
	has(t, render("WP856", &UpgradeWindow{Hour: 3}), []string{"<P22296>1</P22296>", "<P238>0</P238>"}, []string{"<P285>", "<P194>"})
}
