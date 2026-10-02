package sip

import (
	"context"
	"log/slog"
	"regexp"
)

// authHeaderLine matches an Authorization or Proxy-Authorization header line
// (full or folded into one string) up to the end of the line.
var authHeaderLine = regexp.MustCompile(`(?im)^(\s*(?:proxy-)?authorization\s*:)[^\r\n]*`)

// RedactSIP strips the values of Authorization and Proxy-Authorization
// headers from a raw SIP message, so it can be logged.
func RedactSIP(msg string) string {
	return authHeaderLine.ReplaceAllString(msg, "${1} REDACTED")
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
