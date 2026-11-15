package prov

import "slices"

// Grandstream WP side key and speed dials (phone-feature-parity S-12).
//
// Keys and values are from Grandstream's own configuration templates
// (config-template-10212026.zip):
//
//   - Side key: P82317 "Side Key Functions" in wp8x6_config_1.0.3.39 (0
//     PTT/Paging, 2 Alarm, 4 None) and the "PTT Button" in
//     wp856_config_1.0.3.16 (0 PTT, 1 Mute, 3 Speed Dial, 4 Disable, 5 Scan,
//     6 Open the app); the WP820's multi-function button P22258 in
//     wp820_config_1.0.7.90 (0 PTT/Paging, 1 Mute, 2 SAFE/Panic Call, 3 Speed
//     Dial, 4 Disable). wp810_822_825_config_1.0.11.83 has no side key (the
//     WP822's PTT button is fixed).
//   - Speed dials: the WP8x6 "Number Keys" (long press of 0 and 2 to 9; 1 is
//     voicemail): key 0 mode P8682, account P8683, label P8684, value P8685,
//     key n (2 to 9) the same four from P8690 + 4 (n-2); mode -1 None, 0
//     Speed Dial. The WP810/822/825 "Speed Dial" (section "P values not on
//     WEB") and the WP820 "Handset/Speed Dial" (listed commented out): key 0
//     account P23001 and number P23003, key n (2 to 9) P23011 + 5 (n-2) and
//     P23013 + 5 (n-2); no mode and no label. The WP856 has no number keys:
//     its multi-purpose keys ("EXT MPK n") take mode P23000 + 5 (n-1) (0
//     Speed Dial, 1 BLF, ...), account + 1, description + 2 and value + 3.

// The side key functions Hello offers. PTT/paging is one firmware value: the
// key talks on the phone's PTT channel, or pages, as its PTT and paging
// setup says.
const (
	SideKeyPTT   = "ptt"
	SideKeyAlarm = "alarm"
	SideKeyNone  = "none"
)

// SideKeyFunctions are the side key choices, the default first.
var SideKeyFunctions = []string{SideKeyPTT, SideKeyAlarm, SideKeyNone}

// ValidSideKey reports whether s is one of SideKeyFunctions.
func ValidSideKey(s string) bool { return slices.Contains(SideKeyFunctions, s) }

// SideKey is the phone's side key as a template writes it: Param is the
// P-value of the key (0 for a model without a side key Hello can set) and
// Value the function's number on that model. Function is the choice the
// phone resolved to (phone, then group, then SideKeyPTT); the store sets it
// and Render fills Param and Value.
type SideKey struct {
	Function string
	Param    int
	Value    int
}

// SideKeyChoices lists the side key functions a model takes: every WP with
// a side key takes PTT/paging and none; only the WP8x6 has an alarm value
// (and it dials the group's alarm destination, S-29). Empty for a model
// without a side key.
func SideKeyChoices(v Vendor, model string) []string {
	switch WPFamily(v, model) {
	case WP8x6:
		return []string{SideKeyPTT, SideKeyAlarm, SideKeyNone}
	case WP856, WP820:
		return []string{SideKeyPTT, SideKeyNone}
	}
	return nil
}

// sideKeyFor resolves the side key of a WP family. A choice the model does
// not take, or alarm without an alarm destination (nothing to dial), falls
// back to PTT/paging, the factory default.
func sideKeyFor(wp, function string, alarm bool) SideKey {
	k := SideKey{Function: function}
	switch wp {
	case WP8x6, WP856:
		k.Param = 82317
	case WP820:
		k.Param = 22258
	default:
		return SideKey{Function: function}
	}
	switch {
	case function == SideKeyNone:
		k.Value = 4
	case function == SideKeyAlarm && wp == WP8x6 && alarm:
		k.Value = 2
	default:
		k.Function, k.Value = SideKeyPTT, 0
	}
	return k
}

// SpeedDial is one speed dial of a Grandstream WP: a Hello "BLF" entry the
// phone takes as a speed dial (spec S-12: WP phones have no BLF keys). The
// P-values are the slot's own; a zero ModeParam or LabelParam means the
// model has no such key. An unused slot has an empty Number (and Mode -1 on
// the WP8x6), so an entry taken off the phone is cleared at its next
// resync.
type SpeedDial struct {
	Key                                             int // the digit key (WP8x6, WP8xx, WP820) or MPK number (WP856)
	Number, Label                                   string
	Mode                                            int
	ModeParam, AccountParam, LabelParam, ValueParam int
}

// wpDigitKeys are the digit keys that take a speed dial, in the order
// entries fill them: 2 to 9, then 0 (1 is voicemail).
var wpDigitKeys = []int{2, 3, 4, 5, 6, 7, 8, 9, 0}

// wp856SpeedDials is how many of the WP856's multi-purpose keys Hello owns
// for speed dials (MPK 1 to 30); every one is written, so a removed entry
// is cleared.
const wp856SpeedDials = 30

// SpeedDialSlots is how many speed dials a model takes: the WP families'
// counts above, 0 for any other phone (which takes BLF keys instead).
func SpeedDialSlots(v Vendor, model string) int {
	switch WPFamily(v, model) {
	case WP8x6, WP8xx, WP820:
		return len(wpDigitKeys)
	case WP856:
		return wp856SpeedDials
	}
	return 0
}

// speedDialKeys lays the entries out on the family's slots: every slot is
// returned, the first ones holding the entries in order and the rest empty.
// Entries beyond the last slot are dropped.
func speedDialKeys(wp string, keys []BLFKey) []SpeedDial {
	entry := func(i int) (string, string) {
		if i < len(keys) {
			return keys[i].Number, keys[i].Label
		}
		return "", ""
	}
	var out []SpeedDial
	switch wp {
	case WP8x6:
		for i, k := range wpDigitKeys {
			base := 8682
			if k > 0 {
				base = 8690 + 4*(k-2)
			}
			s := SpeedDial{Key: k, Mode: -1, ModeParam: base, AccountParam: base + 1, LabelParam: base + 2, ValueParam: base + 3}
			if n, l := entry(i); n != "" {
				s.Number, s.Label, s.Mode = n, l, 0
			}
			out = append(out, s)
		}
	case WP8xx, WP820:
		for i, k := range wpDigitKeys {
			s := SpeedDial{Key: k, AccountParam: 23001, ValueParam: 23003}
			if k > 0 {
				s.AccountParam, s.ValueParam = 23011+5*(k-2), 23013+5*(k-2)
			}
			s.Number, s.Label = entry(i)
			out = append(out, s)
		}
	case WP856:
		for i := range wp856SpeedDials {
			base := 23000 + 5*i
			s := SpeedDial{Key: i + 1, ModeParam: base, AccountParam: base + 1, LabelParam: base + 2, ValueParam: base + 3}
			s.Number, s.Label = entry(i)
			out = append(out, s)
		}
	}
	return out
}
