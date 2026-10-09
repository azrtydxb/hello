package sip

import (
	"context"
	"log/slog"
	"regexp"
)

// authHeader matches an Authorization or Proxy-Authorization header with
// its folded continuation lines (RFC 3261 §7.3.1: a line starting with SP or
// HTAB continues the previous header).
var authHeader = regexp.MustCompile(`(?im)^([ \t]*(?:proxy-)?authorization[ \t]*:)[^\r\n]*(?:\r?\n[ \t][^\r\n]*)*`)

// flowParam matches the value of an hflow URI parameter: a valid flow token
// lets a peer node reach a phone, so it is kept out of logs.
var flowParam = regexp.MustCompile(`(?i)(\bhflow=)[^;>,\s"]*`)

// voiceAuthHeader matches an X-Hello-Auth header with its folded
// continuation lines: the call signature (spec voice-agents S-32) is kept
// out of logs like any credential.
var voiceAuthHeader = regexp.MustCompile(`(?im)^([ \t]*x-hello-auth[ \t]*:)[^\r\n]*(?:\r?\n[ \t][^\r\n]*)*`)

// RedactSIP strips the values of Authorization and Proxy-Authorization
// headers, folded or not, and of hflow flow tokens from a raw SIP message,
// so it can be logged.
func RedactSIP(msg string) string {
	msg = authHeader.ReplaceAllString(msg, "${1} REDACTED")
	msg = voiceAuthHeader.ReplaceAllString(msg, "${1} REDACTED")
	return flowParam.ReplaceAllString(msg, "${1}REDACTED")
}

// redactHandler applies RedactSIP to every string in a log record. sipgo logs
// raw datagrams when parsing fails; this keeps credentials out of those.
type redactHandler struct{ next slog.Handler }

// NewRedactingHandler wraps next so Authorization header values never reach
// it.
func NewRedactingHandler(next slog.Handler) slog.Handler { return redactHandler{next: next} }

func (h redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, RedactSIP(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = redactAttr(a)
	}
	return redactHandler{next: h.next.WithAttrs(red)}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{next: h.next.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, RedactSIP(v.String()))
	case slog.KindGroup:
		g := v.Group()
		red := make([]any, len(g))
		for i, x := range g {
			red[i] = redactAttr(x)
		}
		return slog.Group(a.Key, red...)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, RedactSIP(err.Error()))
		}
		return slog.Attr{Key: a.Key, Value: v}
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}
