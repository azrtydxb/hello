// Package webhook delivers call events to subscriber URLs (spec
// integrations I-42): endpoints with sealed signing secrets, an HMAC
// signature over the exact body, retries with jittered backoff, a
// dead-letter state with redelivery, automatic disabling of endpoints that
// keep failing, and SSRF-safe outbound requests.
//
// Delivery is at least once. A receiver verifies the signature against the
// raw body, rejects stale timestamps and dedupes on Hello-Event-Id.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Request headers of a delivery.
const (
	HeaderEventID   = "Hello-Event-Id"
	HeaderEventType = "Hello-Event-Type"
	// HeaderDelivery is unique per attempt.
	HeaderDelivery = "Hello-Delivery"
	// HeaderSignature is "t=<unix seconds>,v1=<hex>"; during a rotation
	// window it carries one v1 per valid secret.
	HeaderSignature = "Hello-Signature"
)

// Tolerance is how far a signature timestamp may be from the receiver's
// clock (documented to receivers; Verify defaults to it).
const Tolerance = 5 * time.Minute

// Sign is the hex HMAC-SHA256 of "<t>.<body>" under secret.
func Sign(secret string, t int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(t, 10)))
	m.Write([]byte("."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// SignatureHeader is the Hello-Signature value for body at time t, signed by
// every secret given (one v1 each).
func SignatureHeader(secrets []string, t time.Time, body []byte) string {
	sec := t.Unix()
	var b strings.Builder
	b.WriteString("t=")
	b.WriteString(strconv.FormatInt(sec, 10))
	for _, s := range secrets {
		b.WriteString(",v1=")
		b.WriteString(Sign(s, sec, body))
	}
	return b.String()
}

// Errors Verify returns.
var (
	ErrSignatureFormat   = errors.New("webhook: malformed signature header")
	ErrSignatureStale    = errors.New("webhook: signature timestamp outside the tolerance")
	ErrSignatureMismatch = errors.New("webhook: signature does not match")
)

// Verify checks a Hello-Signature header against the raw body: the timestamp
// must be within tolerance of now (Tolerance when zero) and at least one v1
// must equal the HMAC under one of secrets. The comparison is constant time.
func Verify(header string, body []byte, secrets []string, now time.Time, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = Tolerance
	}
	var (
		t     int64
		haveT bool
		sigs  [][]byte
	)
	for part := range strings.SplitSeq(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return ErrSignatureFormat
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return ErrSignatureFormat
			}
			t, haveT = n, true
		case "v1":
			raw, err := hex.DecodeString(v)
			if err != nil {
				return ErrSignatureFormat
			}
			sigs = append(sigs, raw)
		}
	}
	if !haveT || len(sigs) == 0 {
		return ErrSignatureFormat
	}
	if d := now.Sub(time.Unix(t, 0)); d > tolerance || d < -tolerance {
		return ErrSignatureStale
	}
	ok := false
	for _, s := range secrets {
		want, _ := hex.DecodeString(Sign(s, t, body))
		for _, got := range sigs {
			if hmac.Equal(want, got) {
				ok = true // keep comparing: the time does not depend on which matched
			}
		}
	}
	if !ok {
		return ErrSignatureMismatch
	}
	return nil
}
