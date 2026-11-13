package api

import (
	"regexp"
	"strings"
)

var chatPeerRE = regexp.MustCompile(`^visitor:[1-9][0-9]{0,17}$`)

// chatPeerKey recognises the peer of a chat visitor ("visitor:<id>"), which
// an erase request may name instead of a phone number.
func chatPeerKey(v string) (string, bool) {
	v = strings.TrimSpace(v)
	return v, chatPeerRE.MatchString(v)
}
