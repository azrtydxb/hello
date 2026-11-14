package media

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
)

// Stored is what serving an attachment needs to know of it.
type Stored struct {
	Key         string
	ContentType string
	Filename    string
}

// Serve streams an attachment the caller was found allowed to read (spec
// M-18, "Serving"): with its sniffed type, X-Content-Type-Options: nosniff, a
// sandboxing Content-Security-Policy, Cache-Control: private, no-store, and
// Content-Disposition: attachment for everything but images, audio and
// video. Range requests work, so audio and video can seek.
func Serve(w http.ResponseWriter, r *http.Request, objs Objects, m Stored, log *slog.Logger) {
	obj, err := objs.Open(r.Context(), m.Key)
	if errors.Is(err, ErrNoObject) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		if log != nil {
			log.Error("media: open attachment", "error", err)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Retry-After", "10")
		http.Error(w, "the media store is unavailable", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = obj.Body.Close() }()
	Headers(w.Header(), m)
	http.ServeContent(w, r, "", time.Time{}, obj.Body)
}

// Headers sets the headers every served attachment carries.
func Headers(h http.Header, m Stored) {
	ctype := m.ContentType
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	disp := "attachment"
	if Inline(ctype) {
		disp = "inline"
	}
	name := m.Filename
	if name == "" {
		name = "attachment" + Extension(ctype)
	}
	if v := mime.FormatMediaType(disp, map[string]string{"filename": name}); v != "" {
		h.Set("Content-Disposition", v)
	} else {
		h.Set("Content-Disposition", disp)
	}
}

// Link lifetimes (spec M-18, "Outbound"): a public link to an outbound
// attachment, for a carrier that fetches media by URL, works for 15 minutes
// and at most 5 times.
const (
	LinkTTL     = 15 * time.Minute
	LinkMaxUses = 5
)

// NewLinkToken returns a link token (256 random bits) and the SHA-256 that
// is all the database keeps of it.
func NewLinkToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, LinkHash(token), nil
}

// LinkHash is the stored form of a link token.
func LinkHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// Redeemer counts one use of a link and returns its attachment; a link that
// is unknown, expired or used up is an error (any error answers 404).
type Redeemer func(ctx context.Context, tokenHash []byte) (Stored, error)

// LinkHandler serves GET /m/{token} on the public listener (spec M-5): the
// one way an attachment leaves Hello without a session, for a carrier that
// fetches an outbound MMS's media by URL. Every refusal is the same 404.
func LinkHandler(redeem Redeemer, objs Objects, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.PathValue("token")
		if len(tok) != 43 || strings.Trim(tok, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			notFoundLink(w)
			return
		}
		m, err := redeem(r.Context(), LinkHash(tok))
		if err != nil {
			notFoundLink(w)
			return
		}
		Serve(w, r, objs, m, log)
	}
}

func notFoundLink(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	http.Error(w, "not found", http.StatusNotFound)
}
