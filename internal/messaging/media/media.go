// Package media is the messaging inbox's attachment pipeline (spec
// messaging-channels M-18): what a file is (its type sniffed from its bytes,
// never taken from its name or the sender's word), whether the inbox takes it,
// where it is kept in MinIO, how it is served, and the workers that download
// a carrier's inbound media and remove what retention, an erase or an unused
// upload leaves behind.
//
// An attachment is customer data: nothing here logs its bytes, its name or
// the address it came from.
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits.
const (
	// DefaultMax is HELLO_MSG_MEDIA_MAX's default: the largest attachment the
	// inbox stores (16 MiB).
	DefaultMax = 16 << 20
	// ChatMax is the largest attachment of a website chat, either way (spec
	// M-29a.6).
	ChatMax = 5 << 20
	// LogoMax is the largest chat site logo.
	LogoMax = 256 << 10
	// MaxPerMessage is the most attachments one message carries.
	MaxPerMessage = 10
	// MaxFilename is the longest file name kept, in bytes.
	MaxFilename = 255
	// UploadTTL is how long an agent's upload waits for the message it was
	// made for before it is removed (spec M-18).
	UploadTTL = 24 * time.Hour
	// FetchWindow is how long Hello keeps trying to download an inbound
	// attachment before it is marked expired (spec "Failure modes").
	FetchWindow = 24 * time.Hour
	// OrphanAge is how old an object under msg/ that no attachment row
	// names must be before the orphan sweep removes it: far past a
	// download's lease and an upload's request, so an object whose row is
	// about to name it is never taken.
	OrphanAge = time.Hour
)

// Attachment states.
const (
	StatusPending  = "pending"
	StatusReady    = "ready"
	StatusRejected = "rejected"
	StatusExpired  = "expired"
)

// Why a file is refused. They are short tokens: they are stored as the
// attachment's reason and shown to agents.
const (
	ReasonTooLarge      = "too_large"
	ReasonEmpty         = "empty"
	ReasonNotAllowed    = "type_not_allowed"
	ReasonMismatch      = "type_mismatch"
	ReasonActiveContent = "active_content"
	ReasonMediaHost     = "media_host"
	ReasonNoFetcher     = "provider_cannot_fetch"
	ReasonGone          = "gone_at_provider"
	ReasonWindow        = "download_window_passed"
	ReasonRetention     = "retention"
)

// Rules are what one path accepts: a type allowlist and a size cap.
type Rules struct {
	Allowed func(ctype string) bool
	Max     int64
}

// InboxTypes is the inbox's allowlist (spec M-18): still images other than
// SVG, audio, MP4 and 3GPP video, PDF, vCard and plain text.
func InboxTypes(ctype string) bool {
	switch ctype {
	case "image/jpeg", "image/png", "image/gif", "image/webp",
		"video/mp4", "video/3gpp", "application/pdf", "text/vcard", "text/plain":
		return true
	}
	return strings.HasPrefix(ctype, "audio/")
}

// ChatTypes is a website chat's allowlist (spec M-29a.6): images and PDF.
func ChatTypes(ctype string) bool {
	switch ctype {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "application/pdf":
		return true
	}
	return false
}

// LogoTypes is a chat site logo's allowlist.
func LogoTypes(ctype string) bool {
	switch ctype {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

// InboxRules are the rules of inbound carrier media and of agents' uploads to
// a channel that is not a chat; max is HELLO_MSG_MEDIA_MAX.
func InboxRules(max int64) Rules {
	if max <= 0 {
		max = DefaultMax
	}
	return Rules{Allowed: InboxTypes, Max: max}
}

// ChatRules are the rules of a website chat's attachments, both ways.
func ChatRules() Rules { return Rules{Allowed: ChatTypes, Max: ChatMax} }

// LogoRules are the rules of a chat site logo.
func LogoRules() Rules { return Rules{Allowed: LogoTypes, Max: LogoMax} }

// Refusal is why a file was not taken.
type Refusal struct {
	Reason string
	// Detail says it in words for the person who sent it.
	Detail string
}

func (r *Refusal) Error() string { return "media: " + r.Reason }

// Inspected is a file the rules accept.
type Inspected struct {
	// ContentType is the sniffed type, without parameters; the file is
	// always served with it.
	ContentType string
	Size        int64
	SHA256      string
}

// Inspect decides whether data is a file the rules accept. The type is
// sniffed from the bytes; declared (the sender's Content-Type, empty when
// none) is advisory but must name the same class (image, audio, video, text,
// application) as the sniffed type. A file that also carries markup a
// browser would run (HTML, SVG, script: a polyglot) is refused whatever it
// sniffs as, and so is SVG itself.
func Inspect(data []byte, declared string, rules Rules) (Inspected, *Refusal) {
	if int64(len(data)) > rules.Max {
		return Inspected{}, &Refusal{Reason: ReasonTooLarge, Detail: "the file is larger than " + Size(rules.Max)}
	}
	if len(data) == 0 {
		return Inspected{}, &Refusal{Reason: ReasonEmpty, Detail: "the file is empty"}
	}
	decl := baseType(declared)
	if decl == "image/svg+xml" || strings.Contains(decl, "html") || strings.Contains(decl, "javascript") || strings.HasSuffix(decl, "+xml") || decl == "text/xml" || decl == "application/xml" {
		return Inspected{}, &Refusal{Reason: ReasonActiveContent, Detail: "SVG, HTML, XML and script files are not accepted"}
	}
	ctype := Sniff(data)
	if activeContent(data, ctype) {
		return Inspected{}, &Refusal{Reason: ReasonActiveContent, Detail: "the file carries markup or a second format a browser could run"}
	}
	if !rules.Allowed(ctype) {
		return Inspected{}, &Refusal{Reason: ReasonNotAllowed, Detail: "files of this type (" + ctype + ") are not accepted"}
	}
	if decl != "" && decl != "application/octet-stream" && class(decl) != class(ctype) {
		return Inspected{}, &Refusal{Reason: ReasonMismatch, Detail: "the file is not what its type says"}
	}
	sum := sha256.Sum256(data)
	return Inspected{ContentType: ctype, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, nil
}

// baseType is a Content-Type without parameters, lower-cased.
func baseType(ct string) string {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return ""
	}
	if t, _, err := mime.ParseMediaType(ct); err == nil {
		return strings.ToLower(t)
	}
	t, _, _ := strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(t))
}

func class(ct string) string {
	c, _, _ := strings.Cut(ct, "/")
	return c
}

// Sniff names the type of data from its first bytes: http.DetectContentType
// (the WHATWG algorithm) plus the formats it does not know that MMS carries
// (3GPP video, AMR audio, vCard). The answer has no parameters.
func Sniff(data []byte) string {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	switch {
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return "application/pdf"
	case bytes.HasPrefix(head, []byte("#!AMR")):
		return "audio/amr"
	case len(head) >= 12 && string(head[4:8]) == "ftyp" && (string(head[8:11]) == "3gp" || string(head[8:11]) == "3g2"):
		return "video/3gpp"
	}
	ct := baseType(http.DetectContentType(head))
	switch ct {
	case "audio/wave":
		return "audio/wav"
	case "application/ogg":
		return "audio/ogg"
	case "text/plain":
		t := bytes.TrimLeft(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), " \t\r\n")
		if len(t) >= 11 && strings.EqualFold(string(t[:11]), "BEGIN:VCARD") {
			return "text/vcard"
		}
		if !utf8.Valid(data) {
			return "application/octet-stream"
		}
	}
	return ct
}

// markers are what makes a browser run a file as a document: the start of an
// HTML or SVG document, a script, a frame, an XML prologue.
var markers = [][]byte{
	[]byte("<script"), []byte("<html"), []byte("<svg"), []byte("<!doctype"), []byte("<iframe"), []byte("<object"),
	[]byte("<embed"), []byte("<body"), []byte("<?xml"), []byte("<meta"), []byte("<frameset"), []byte("javascript:"),
}

// activeContent reports whether data, whatever it sniffed as, also reads as
// a document a browser would run: markup anywhere in it (a GIF or JPEG with
// HTML in a comment, a PDF that is also an HTML page), or an image that is
// also a ZIP archive (a GIFAR). Binary data matches a marker by chance with a
// negligible probability; a text file that holds markup is refused on
// purpose.
func activeContent(data []byte, ctype string) bool {
	lower := bytes.ToLower(data)
	for _, m := range markers {
		if bytes.Contains(lower, m) {
			return true
		}
	}
	if strings.HasPrefix(ctype, "image/") || ctype == "application/pdf" {
		// A ZIP's end-of-central-directory record sits in its last 64 KiB.
		tail := data
		if len(tail) > 64<<10+22 {
			tail = tail[len(tail)-(64<<10+22):]
		}
		if bytes.Contains(tail, []byte("PK\x05\x06")) {
			return true
		}
	}
	return false
}

// Inline reports whether a type is shown in the page (images, audio and
// video previews); everything else is served as a download.
func Inline(ctype string) bool {
	switch class(ctype) {
	case "image", "audio", "video":
		return true
	}
	return false
}

// CleanFilename keeps the last element of a sender's file name: printable,
// without path separators or quotes, at most MaxFilename bytes. An empty or
// unusable name becomes "attachment" plus an extension for the type.
func CleanFilename(name, ctype string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	var b strings.Builder
	for _, r := range name {
		if !unicode.IsPrint(r) || r == '"' || r == '/' || r == '\\' || r == utf8.RuneError {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > MaxFilename {
			break
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		out = "attachment" + Extension(ctype)
	}
	return out
}

// Extension is the usual file extension of a sniffed type.
func Extension(ctype string) string {
	switch ctype {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/3gpp":
		return ".3gp"
	case "application/pdf":
		return ".pdf"
	case "text/vcard":
		return ".vcf"
	case "text/plain":
		return ".txt"
	case "audio/amr":
		return ".amr"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav":
		return ".wav"
	case "audio/ogg":
		return ".ogg"
	}
	return ""
}

// Size says a byte count the way the inbox shows it.
func Size(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return strconv.FormatInt(n>>10, 10) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// MessageKey is where an inbound attachment is kept (spec "Data", MinIO):
// msg/<channel>/<yyyy>/<message>/<sha256>.
func MessageKey(channelID int64, at time.Time, messageID int64, sha string) string {
	return fmt.Sprintf("msg/%d/%04d/%d/%s", channelID, at.UTC().Year(), messageID, sha)
}

// UploadKey is where an upload is kept: no message exists yet when it is
// made, so the attachment's own id stands in for the message's.
func UploadKey(channelID int64, at time.Time, mediaID int64, sha string) string {
	return fmt.Sprintf("msg/%d/%04d/upload-%d/%s", channelID, at.UTC().Year(), mediaID, sha)
}

// ParseKey reads an attachment's object key: the message of a MessageKey,
// or the attachment of an UploadKey (the other is 0). ok is false for any
// other key under msg/ (a chat site's logo), which the orphan sweep leaves
// alone.
func ParseKey(key string) (messageID, mediaID int64, ok bool) {
	p := strings.Split(key, "/")
	if len(p) != 5 || p[0] != "msg" || p[4] == "" {
		return 0, 0, false
	}
	if ch, err := strconv.ParseInt(p[1], 10, 64); err != nil || ch <= 0 {
		return 0, 0, false
	}
	if y, err := strconv.Atoi(p[2]); err != nil || len(p[2]) != 4 || y <= 0 {
		return 0, 0, false
	}
	if id, found := strings.CutPrefix(p[3], "upload-"); found {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		return 0, n, true
	}
	n, err := strconv.ParseInt(p[3], 10, 64)
	if err != nil || n <= 0 {
		return 0, 0, false
	}
	return n, 0, true
}

// LogoKey is where a chat site's logo is kept.
func LogoKey(siteID int64) string { return fmt.Sprintf("msg/site/%d/logo", siteID) }
