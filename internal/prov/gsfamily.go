package prov

import (
	"regexp"
	"strings"
)

// Grandstream WP model families. Grandstream publishes one configuration
// template per family and the P-value sets differ, so the built-in template
// writes each family only the keys its own template defines:
//
//	FamilyWP8x6: WP816, WP826, WP836 (wp8x6_config 1.0.3.39)
//	FamilyWP856: WP856                (wp856_config 1.0.3.16)
//	FamilyWP8xx: WP810, WP820, WP822, WP825 (wp810_822_825_config 1.0.11.83)
//
// A Grandstream model outside these (a GRP or GXP desk phone) has the empty
// family and keeps the generic key set.
const (
	FamilyWP8x6 = "8x6"
	FamilyWP856 = "856"
	FamilyWP8xx = "8xx"
)

var (
	wp8x6RE = regexp.MustCompile(`^WP8[1-3]6`)
	wp856RE = regexp.MustCompile(`^WP856`)
	wp8xxRE = regexp.MustCompile(`^WP8(10|2[0258])`)
)

func normModel(model string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(model))
}

// GrandstreamFamily returns the WP family of a Grandstream model, or "" for
// a model that is not a WP.
func GrandstreamFamily(model string) string {
	m := normModel(model)
	switch {
	case wp856RE.MatchString(m):
		return FamilyWP856
	case wp8x6RE.MatchString(m):
		return FamilyWP8x6
	case wp8xxRE.MatchString(m):
		return FamilyWP8xx
	}
	return ""
}

var ht813RE = regexp.MustCompile(`^HT813`)

// HasFXOPort reports whether the model is a Grandstream HT813, the ATA
// with an FXO port to the PSTN.
func HasFXOPort(v Vendor, model string) bool {
	return v == Grandstream && ht813RE.MatchString(normModel(model))
}

// SupportsBLF reports whether Hello can provision BLF keys for the model.
// None of the three WP templates defines the P323 key block the built-in
// template used to write (the WP856 has its BLF keys in the multi-purpose
// key app, and the WP8x6 and WP8xx have no BLF key type), so a WP phone
// takes none. Every other phone does.
func SupportsBLF(v Vendor, model string) bool {
	return v != Grandstream || GrandstreamFamily(model) == ""
}

// wp856RefreshMinutes are the only phonebook download intervals (P332) the
// WP856 accepts, in minutes; 0 disables the download.
var wp856RefreshMinutes = []int{0, 5, 30, 60, 120, 240, 360, 480, 720}

// BookRefreshFor fits the phonebook download interval to what the family
// accepts: the WP856 takes only the values of wp856RefreshMinutes, so the
// interval is rounded up to the next one (a phone never polls more often
// than asked); the WP8x6 and WP8xx take 0 or 5 to 720. Other families keep
// the value.
func BookRefreshFor(family string, minutes int) int {
	switch family {
	case FamilyWP856:
		if minutes <= 0 {
			return 0
		}
		for _, v := range wp856RefreshMinutes {
			if v >= minutes {
				return v
			}
		}
		return 720
	case FamilyWP8x6, FamilyWP8xx:
		switch {
		case minutes <= 0:
			return 0
		case minutes < 5:
			return 5
		case minutes > 720:
			return 720
		}
	}
	return minutes
}

// maxEmergencyNumbers bounds the list handed to a phone.
const maxEmergencyNumbers = 10

var emergencyNumberRE = regexp.MustCompile(`^[0-9*#+]{1,16}$`)

// AddEmergencyNumber appends the route match m to nums when it is a plain
// dial string not already in the list and the list has room.
func AddEmergencyNumber(nums []string, m string) []string {
	m = strings.TrimSpace(m)
	if !emergencyNumberRE.MatchString(m) || len(nums) >= maxEmergencyNumbers {
		return nums
	}
	for _, n := range nums {
		if n == m {
			return nums
		}
	}
	return append(nums, m)
}
