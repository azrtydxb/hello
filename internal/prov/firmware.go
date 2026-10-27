package prov

// Firmware policy (spec S-26): per-vendor and model-glob minimum versions,
// recommended versions, and the comparison drift is judged by. Hello ships
// no vendor firmware; a recommended version is only a documented target the
// admin uploads and pins.

import (
	"path"
	"strconv"
	"strings"
)

// FirmwareMinimum is the lowest firmware a vendor's models may be pinned to
// or provisioned with (a prov_firmware_minimums row). MinVersion is empty
// when the row only documents a recommended version. Recommended is the
// version the lab passed, shown to the admin; it is never rendered.
type FirmwareMinimum struct {
	Vendor      Vendor `json:"vendor"`
	ModelGlob   string `json:"modelGlob"`
	MinVersion  string `json:"minVersion"`
	Recommended string `json:"recommended"`
}

// CompareVersions compares two dotted firmware versions part by part,
// numerically where both parts are numbers and as text otherwise. A missing
// part counts as 0, so "1.0.11" equals "1.0.11.0". It returns -1, 0 or 1.
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(strings.TrimSpace(a), "."), strings.Split(strings.TrimSpace(b), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := "0", "0"
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		nx, ex := strconv.ParseUint(x, 10, 64)
		ny, ey := strconv.ParseUint(y, 10, 64)
		switch {
		case ex == nil && ey == nil:
			if nx != ny {
				return cmpInt(nx, ny)
			}
		case x != y:
			return strings.Compare(x, y)
		}
	}
	return 0
}

func cmpInt(a, b uint64) int {
	if a < b {
		return -1
	}
	return 1
}

// MinimumFor is the highest minimum version of the rules that match the
// vendor's model, "" when none does.
func MinimumFor(rules []FirmwareMinimum, v Vendor, model string) string {
	return highest(rules, v, func(g string) bool { ok, _ := path.Match(g, model); return ok })
}

// MinimumForPin is the highest minimum of the rules a pin for modelGlob can
// reach: a rule whose glob matches the pin's glob read as a model name, or
// the other way round. It errs on the side of refusing, so a broad pin such
// as "*" cannot put a below-floor file on a model that has one.
func MinimumForPin(rules []FirmwareMinimum, v Vendor, modelGlob string) string {
	return highest(rules, v, func(g string) bool {
		if g == modelGlob {
			return true
		}
		if ok, _ := path.Match(g, modelGlob); ok {
			return true
		}
		ok, _ := path.Match(modelGlob, g)
		return ok
	})
}

func highest(rules []FirmwareMinimum, v Vendor, match func(glob string) bool) string {
	best := ""
	for _, r := range rules {
		if r.Vendor != v || r.MinVersion == "" || !match(r.ModelGlob) {
			continue
		}
		if best == "" || CompareVersions(r.MinVersion, best) > 0 {
			best = r.MinVersion
		}
	}
	return best
}

// FirmwareDrift reports whether the firmware a phone last showed in its
// User-Agent differs from the pin or sits below the model minimum. A phone
// that never showed a version is not drifting.
func FirmwareDrift(seen, pinned, minimum string) bool {
	if seen == "" {
		return false
	}
	if pinned != "" && CompareVersions(seen, pinned) != 0 {
		return true
	}
	return minimum != "" && CompareVersions(seen, minimum) < 0
}
