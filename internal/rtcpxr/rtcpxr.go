// Package rtcpxr parses the voice-quality reports phones publish with SIP
// PUBLISH (Event: vq-rtcpxr, RFC 6035; spec phone-feature-parity S-36).
//
// Only the phone's own view is kept (the LocalMetrics block: what the phone
// received): MOS-LQ and MOS-CQ, inter-arrival jitter, network loss, jitter
// buffer discard and round-trip delay. A report that is not a well-formed
// RFC 6035 session report, or that carries a value outside its range, is
// rejected whole and nothing of it is stored.
package rtcpxr

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ContentType and Event are the PUBLISH's media type and Event package.
const (
	ContentType = "application/vq-rtcpxr"
	Event       = "vq-rtcpxr"
)

// MaxBody is the largest report body accepted; real reports are under 2 KiB.
const MaxBody = 4096

// ErrMalformed marks a body that is not a usable session report.
var ErrMalformed = errors.New("rtcpxr: malformed report")

// Report is the phone-side metrics of one dialog. A nil metric was not in the
// report.
type Report struct {
	// CallID is the Call-ID of the dialog the report describes (the phone's
	// leg of the call).
	CallID string
	// Final is true for the call-end report (VQSessionReport: CallTerm), false
	// for an interval or alert report.
	Final bool

	MOSLQ       *float64 // listening quality, 0 to 5
	MOSCQ       *float64 // conversational quality, 0 to 5
	JitterMs    *float64 // inter-arrival jitter (IAJ)
	LossPct     *float64 // network packet loss rate (NLR), percent
	DiscardPct  *float64 // jitter buffer discard rate (JDR), percent
	RoundTripMs *float64 // round-trip delay (RTD)
}

// Parse reads a report body. It ignores lines and fields it does not know
// (RFC 6035 lets vendors add them), reads only the LocalMetrics block, and
// fails with ErrMalformed when the session report header is missing, the
// dialog is not named, no metric is present or a value is out of range.
func Parse(body []byte) (Report, error) {
	var r Report
	if len(body) == 0 || len(body) > MaxBody {
		return r, fmt.Errorf("%w: size %d", ErrMalformed, len(body))
	}
	var (
		seenHeader bool
		block      string // LocalMetrics, RemoteMetrics, DialogID or ""
		dialogID   string
		callID     string
	)
	for _, line := range strings.FieldsFunc(string(body), func(c rune) bool { return c == '\n' || c == '\r' }) {
		if line[0] == ' ' || line[0] == '\t' {
			continue // continuation of a line we do not read
		}
		name, val, ok := strings.Cut(line, ":")
		if !ok && !strings.EqualFold(strings.TrimSpace(line), "VQSessionReport") {
			return r, fmt.Errorf("%w: line without a field name", ErrMalformed)
		}
		name, val = strings.TrimSpace(name), strings.TrimSpace(val)
		switch strings.ToLower(name) {
		case "vqsessionreport":
			seenHeader = true
			r.Final = strings.EqualFold(val, "CallTerm")
			block = ""
			continue
		case "vqintervalreport", "vqalertreport":
			seenHeader = true
			block = ""
			continue
		case "localmetrics":
			block = "local"
			continue
		case "remotemetrics":
			block = "remote"
			continue
		case "dialogid":
			dialogID = val
			continue
		case "callid":
			if block == "local" || block == "" {
				callID = val
			}
			continue
		}
		if block != "local" {
			continue
		}
		fields := keyValues(val)
		var err error
		switch strings.ToLower(name) {
		case "qualityest":
			if r.MOSLQ, err = field(fields, "MOSLQ", 0, 5); err == nil {
				r.MOSCQ, err = field(fields, "MOSCQ", 0, 5)
			}
		case "delay":
			if r.RoundTripMs, err = field(fields, "RTD", 0, 65535); err == nil {
				r.JitterMs, err = field(fields, "IAJ", 0, 65535)
			}
		case "packetloss":
			if r.LossPct, err = field(fields, "NLR", 0, 100); err == nil {
				r.DiscardPct, err = field(fields, "JDR", 0, 100)
			}
		}
		if err != nil {
			return Report{}, err
		}
	}
	if !seenHeader {
		return Report{}, fmt.Errorf("%w: no session report header", ErrMalformed)
	}
	// RFC 6035: DialogID is "call-id;to-tag=...;from-tag=..."; some phones
	// also send a bare CallID.
	id, _, _ := strings.Cut(dialogID, ";")
	if id = strings.TrimSpace(id); id == "" {
		id = callID
	}
	if id == "" || len(id) > 256 || strings.ContainsAny(id, " \t") {
		return Report{}, fmt.Errorf("%w: dialog not named", ErrMalformed)
	}
	r.CallID = id
	if r.MOSLQ == nil && r.MOSCQ == nil && r.JitterMs == nil && r.LossPct == nil && r.DiscardPct == nil && r.RoundTripMs == nil {
		return Report{}, fmt.Errorf("%w: no metrics", ErrMalformed)
	}
	return r, nil
}

// keyValues splits "A=1 B=2.5" into a map; a token without = is dropped.
func keyValues(s string) map[string]string {
	m := map[string]string{}
	for _, tok := range strings.Fields(s) {
		if k, v, ok := strings.Cut(tok, "="); ok && k != "" {
			m[strings.ToUpper(k)] = v
		}
	}
	return m
}

// field reads a decimal metric in [lo, hi]; an absent key is nil with no
// error, a present one that is not a finite number in range is ErrMalformed.
func field(m map[string]string, key string, lo, hi float64) (*float64, error) {
	s, ok := m[key]
	if !ok {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < lo || v > hi {
		return nil, fmt.Errorf("%w: %s out of range", ErrMalformed, key)
	}
	return &v, nil
}
