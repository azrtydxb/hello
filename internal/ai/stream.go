package ai

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/azrtydxb/go-ai-sdk/provider"
)

// Stream receives a call's progress while the model generates it (Call.
// Stream). The callbacks are optional and run on the goroutine making the
// call, in order, so they must return quickly.
//
// Only the human answer is streamed. A call with tools streams nothing of
// its tool phase but reasoning: the text comes from the answer phase. The
// model answers with a JSON object
// (HELLO_AI_STRUCTURED_OUTPUT json_schema or prompt alike); Field names its
// string property, and Text receives that property's decoded characters as
// they arrive. Reasoning is never passed on as text: Thinking says the model
// is reasoning, and nothing else of it leaves the call.
type Stream struct {
	// Field is the top-level string property of the answer object to
	// stream, such as "answer".
	Field string
	// Thinking is called once per model step that starts with reasoning.
	Thinking func()
	// Text is called with each new piece of the field's value.
	Text func(delta string)
	// Reset is called when text already passed to Text is void: the step
	// that produced it went on to call tools, or the answer was rejected and
	// the model is asked again.
	Reset func()
}

// fieldStream turns a step's text deltas into Stream callbacks.
type fieldStream struct {
	st *Stream
	// buf is the current step's text so far.
	buf strings.Builder
	// sent is how much of the field's decoded value Text has received in
	// the current answer.
	sent     string
	thinking bool
	// quiet is set in the tool phase: its text is never the answer, so it
	// is not passed on, only that the model is reasoning.
	quiet bool
}

func newFieldStream(st *Stream) *fieldStream { return &fieldStream{st: st} }

// text adds a model text delta.
func (f *fieldStream) text(d string) {
	f.buf.WriteString(d)
	if f.quiet {
		if raw := f.buf.String(); strings.Contains(raw, "<think>") && !strings.Contains(raw, "</think>") {
			f.think()
		}
		return
	}
	val, thinking := extractField(f.buf.String(), f.st.Field)
	if thinking {
		f.think()
		return
	}
	if len(val) <= len(f.sent) || !strings.HasPrefix(val, f.sent) {
		return
	}
	delta := val[len(f.sent):]
	f.sent = val
	if f.st.Text != nil {
		f.st.Text(delta)
	}
}

// reasoning notes a reasoning delta.
func (f *fieldStream) reasoning() { f.think() }

func (f *fieldStream) think() {
	if f.thinking {
		return
	}
	f.thinking = true
	if f.st.Thinking != nil {
		f.st.Thinking()
	}
}

// endStep closes a model step. Text streamed from a step that went on to
// call tools was not the answer.
func (f *fieldStream) endStep(r provider.FinishReason) {
	if r == provider.FinishToolCalls {
		f.reset()
	}
	f.buf.Reset()
	f.thinking = false
}

// reset voids what Text has received.
func (f *fieldStream) reset() {
	if f.sent == "" {
		return
	}
	f.sent = ""
	if f.st.Reset != nil {
		f.st.Reset()
	}
}

// extractField returns the decoded value of the top-level string property
// field in the JSON object that raw starts to be, as far as it has arrived.
// Inline reasoning (<think>...</think>) and a Markdown code fence before the
// object are skipped. thinking is true while a <think> block is still open.
// The value is empty when the property has not started; a string cut inside
// an escape sequence yields the text before the escape.
func extractField(raw, field string) (value string, thinking bool) {
	if i := strings.LastIndex(raw, "</think>"); i >= 0 {
		raw = raw[i+len("</think>"):]
	} else if strings.Contains(raw, "<think>") {
		return "", true
	}
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		nl := strings.IndexByte(raw, '\n')
		if nl < 0 {
			return "", false
		}
		raw = strings.TrimSpace(raw[nl+1:])
	}
	if !strings.HasPrefix(raw, "{") {
		return "", false
	}
	depth, expectKey, haveKey := 0, false, false
	var key string
	for i := 0; i < len(raw); {
		switch raw[i] {
		case '{', '[':
			depth++
			if depth == 1 {
				expectKey, haveKey = true, false
			}
			i++
		case '}', ']':
			depth--
			i++
		case ',':
			if depth == 1 {
				expectKey, haveKey = true, false
			}
			i++
		case ':':
			if depth == 1 {
				expectKey = false
			}
			i++
		case '"':
			s, end, complete := readString(raw, i)
			switch {
			case depth == 1 && expectKey:
				if !complete {
					return "", false
				}
				key, haveKey, expectKey = s, true, false
			case depth == 1 && haveKey && key == field:
				return s, false
			case !complete:
				return "", false
			}
			i = end
		default:
			i++
		}
	}
	return "", false
}

// readString decodes the JSON string that starts at raw[start] (a quote).
// end is the index after the closing quote; complete is false when raw ends
// first, with s the decoded text before the cut (never half an escape).
func readString(raw string, start int) (s string, end int, complete bool) {
	var b strings.Builder
	for i := start + 1; i < len(raw); {
		c := raw[i]
		switch {
		case c == '"':
			return b.String(), i + 1, true
		case c != '\\':
			r, n := utf8.DecodeRuneInString(raw[i:])
			if r == utf8.RuneError && n <= 1 && !utf8.FullRuneInString(raw[i:]) {
				return b.String(), len(raw), false
			}
			b.WriteString(raw[i : i+n])
			i += n
		default:
			if i+1 >= len(raw) {
				return b.String(), len(raw), false
			}
			switch e := raw[i+1]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'u':
				r, n, ok := unicodeEscape(raw[i:])
				if !ok {
					return b.String(), len(raw), false
				}
				b.WriteRune(r)
				i += n
				continue
			default:
				b.WriteByte(e)
			}
			i += 2
		}
	}
	return b.String(), len(raw), false
}

// unicodeEscape decodes the \uXXXX (and a following low surrogate) at the
// start of s; ok is false when s ends inside it.
func unicodeEscape(s string) (r rune, n int, ok bool) {
	hex := func(s string) (rune, bool) {
		if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
			return 0, false
		}
		var v rune
		for _, c := range s[2:6] {
			switch {
			case c >= '0' && c <= '9':
				v = v<<4 | (c - '0')
			case c >= 'a' && c <= 'f':
				v = v<<4 | (c - 'a' + 10)
			case c >= 'A' && c <= 'F':
				v = v<<4 | (c - 'A' + 10)
			default:
				return utf8.RuneError, true
			}
		}
		return v, true
	}
	r, ok = hex(s)
	if !ok {
		return 0, 0, false
	}
	if !utf16.IsSurrogate(r) {
		return r, 6, true
	}
	if r >= 0xDC00 {
		return utf8.RuneError, 6, true
	}
	if len(s) < 12 {
		return 0, 0, false
	}
	if lo, ok := hex(s[6:]); ok && utf16.IsSurrogate(lo) && lo >= 0xDC00 {
		return utf16.DecodeRune(r, lo), 12, true
	}
	return utf8.RuneError, 6, true
}
