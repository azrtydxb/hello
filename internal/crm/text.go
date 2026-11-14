package crm

import "strings"

// RecordingPrefix starts the description line that carries a recording link.
const RecordingPrefix = "Recording: "

// RecordingLine puts a recording link into a CRM record's description: it
// replaces the line of an earlier link (a renewal) and keeps everything a
// person wrote. An empty link returns the description unchanged.
func RecordingLine(desc, link string) string {
	if link == "" {
		return desc
	}
	lines := strings.Split(desc, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, RecordingPrefix) {
			lines[i] = RecordingPrefix + link
			return strings.Join(lines, "\n")
		}
	}
	if strings.TrimSpace(desc) == "" {
		return RecordingPrefix + link
	}
	return strings.TrimRight(desc, "\n") + "\n" + RecordingPrefix + link
}
