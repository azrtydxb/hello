package mailer

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// BuildMessage renders one notification email: a text summary (caller ID,
// time, duration) plus, when audio is given, the recording as a base64
// attachment. The attachment keeps the stored format: WAV, or MP3 if that is
// what the box holds; nothing is transcoded.
func BuildMessage(from string, j Job, audio []byte) ([]byte, error) {
	if from == "" {
		return nil, errors.New("mailer: no From address configured")
	}
	if strings.ContainsAny(from, "\r\n") || strings.ContainsAny(j.To, "\r\n") {
		return nil, errors.New("mailer: address contains a line break")
	}
	var buf bytes.Buffer
	subject := "New voicemail"
	if j.Caller != "" {
		subject += " from " + j.Caller
	}
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", j.To)
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&buf, "Date: %s\r\n", j.CreatedAt.Format(time.RFC1123Z))
	buf.WriteString("MIME-Version: 1.0\r\n")
	mp := multipart.NewWriter(&buf)
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%q\r\n", mp.Boundary())
	buf.WriteString("\r\n")

	// The summary.
	tw, err := mp.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("You have a new voicemail")
	if j.Caller != "" {
		b.WriteString(" from " + j.Caller)
	}
	b.WriteString(".\r\n\r\nDuration: ")
	b.WriteString(formatDuration(j.DurationMs))
	b.WriteString(".\r\nReceived: ")
	b.WriteString(j.CreatedAt.UTC().Format(time.RFC1123Z))
	b.WriteString(".\r\n")
	if len(audio) == 0 {
		b.WriteString("Play it in the Hello web UI.\r\n")
	}
	if _, err := quotedprintable.NewWriter(tw).Write([]byte(b.String())); err != nil {
		return nil, err
	}

	// The audio attachment.
	if len(audio) > 0 {
		ctype, ext := audioType(audio)
		aw, err := mp.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {ctype + "; name=\"voicemail." + ext + "\""},
			"Content-Disposition":       {"attachment; filename=\"voicemail." + ext + "\""},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeBase64(aw, audio); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), mp.Close()
}

// writeBase64 writes b base64-encoded in 76-character lines.
func writeBase64(w io.Writer, b []byte) error {
	enc := base64.StdEncoding.EncodeToString(b)
	for len(enc) > 76 {
		if _, err := io.WriteString(w, enc[:76]+"\r\n"); err != nil {
			return err
		}
		enc = enc[76:]
	}
	_, err := io.WriteString(w, enc+"\r\n")
	return err
}

// formatDuration renders milliseconds as m:ss.
func formatDuration(ms int64) string {
	s := (ms + 500) / 1000
	return strconv.FormatInt(s/60, 10) + ":" + fmt.Sprintf("%02d", s%60)
}

// audioType names the attachment's media type and extension from its bytes.
func audioType(b []byte) (ctype, ext string) {
	if len(b) >= 3 && (string(b[:3]) == "ID3" || (b[0] == 0xFF && b[1]&0xE0 == 0xE0)) {
		return "audio/mpeg", "mp3"
	}
	return "audio/wav", "wav"
}

// BuildTest renders the message the "send test email" button sends.
func BuildTest(from, to string, now time.Time) ([]byte, error) {
	if from == "" || strings.ContainsAny(from+to, "\r\n") {
		return nil, errors.New("mailer: bad address")
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\nTo: %s\r\nSubject: Hello PBX test email\r\nDate: %s\r\nMIME-Version: 1.0\r\n", from, to, now.Format(time.RFC1123Z))
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	buf.WriteString("This is a test email from your Hello PBX. Voicemail notifications will be sent the same way.\r\n")
	return buf.Bytes(), nil
}
