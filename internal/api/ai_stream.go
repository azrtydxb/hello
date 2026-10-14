package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/ai/assistant/chat"
)

// sseKeepalive is how often an idle event stream sends a comment, so a
// proxy or load balancer does not close it during a long reasoning phase.
// A variable for the tests.
var sseKeepalive = 15 * time.Second

// wantsEventStream reports whether the caller asked for the answer as a
// server-sent event stream (Accept: text/event-stream); any other Accept
// keeps the JSON behaviour.
func wantsEventStream(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept") {
		for part := range strings.SplitSeq(v, ",") {
			mt, _, _ := strings.Cut(part, ";")
			if strings.EqualFold(strings.TrimSpace(mt), "text/event-stream") {
				return true
			}
		}
	}
	return false
}

// streamAIMessage answers postAIMessage as a text/event-stream: the events
// of chat.Streamer, flushed one by one, with a keepalive comment while
// nothing happens. A refusal before the task starts is the usual JSON
// error. The request's context ends the work: when the client goes, or this
// function returns, the model call is cancelled.
func (s *server) streamAIMessage(w http.ResponseWriter, r *http.Request, st chat.Streamer, u chat.User, sessionID, content string) {
	ch, err := st.PostMessageStream(r.Context(), u, sessionID, content)
	if err != nil {
		s.assistantError(w, "session", err)
		return
	}
	rc := http.NewResponseController(w)
	// A long answer outlives any write deadline the server may set.
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}
	tick := time.NewTicker(sseKeepalive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil || rc.Flush() != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			frame, err := ev.Frame()
			if err != nil {
				s.Log.Error("assistant: encoding a stream event", "event", ev.Name, "error", err)
				continue
			}
			if _, err := w.Write(frame); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
