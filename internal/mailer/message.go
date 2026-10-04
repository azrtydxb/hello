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

// BuildMessage renders one notification email: a text summary plus the
// message audio as a base64 WAV attachment (spec S-8). A missing attachment
// still sends: the summary tells the recipient where to play the message.
func BuildMessage(from string, j Job, wav []byte) ([]byte, error) {
	if from == "" {
		return nil, errors.New("mailer: no From address configured")
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
	b.WriteString(".\r\nPlay it in the Hello web UI.\r\n")
	if _, err := quotedprintable.NewWriter(tw).Write([]byte(b.String())); err != nil {
		return nil, err
	}

	// The audio attachment.
	if len(wav) > 0 {
		aw, err := mp.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {"audio/wav; name=\"voicemail.wav\""},
			"Content-Disposition":       {"attachment; filename=\"voicemail.wav\""},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeBase64(aw, wav); err != nil {
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
