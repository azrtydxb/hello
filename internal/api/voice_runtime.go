package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/voice"
)

// The voice-runtime operations (spec voice-agents S-18 to S-22) and the
// voice status. Every handler is thin: scope and role are the route table's,
// a nil s.Voice answers 503 voice_disabled, and the logic lives in
// internal/voice. The runtime operations are not MCP tools (their
// x-hello-mcp entries exclude them), and the view is the only place an MCP
// credential is unsealed; nothing here logs a body, a prompt, a credential
// or a transcript.

// maxWait is the longest long poll (spec S-19 asks for ?wait=25); a longer
// wait is capped, not refused.
const maxWait = 30 * time.Second

// getVoiceRuntimeAgents serves GET /api/v1/voice-runtime/agents: the view
// with its strong ETag and Cache-Control: no-store, 304 on If-None-Match,
// and the long poll ?wait=25&revision=n that answers on a revision change
// or after wait (spec S-19). A fetch that answers 200 is audited by service
// account, never by content.
func (s *server) getVoiceRuntimeAgents(w http.ResponseWriter, r *http.Request) {
	if s.Voice == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "voice agents are not configured")
		return
	}
	wait, rev, polled := parseWaitRevision(r.URL.Query())
	imm := r.Header.Get("If-None-Match")
	if wait > 0 {
		cur, err := s.Voice.Await(r.Context(), rev, wait)
		if err != nil {
			s.internal(w, "voice runtime long poll", err)
			return
		}
		// The wait ran out without a change: 304 (spec S-19), unless the
		// client's If-None-Match already disagrees with the current tag.
		if cur == rev && (imm == "" || imm == voice.ETag(rev)) {
			w.Header().Set("ETag", voice.ETag(rev))
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	view, err := s.Voice.View(r.Context())
	if err != nil {
		s.internal(w, "voice runtime view", err)
		return
	}
	w.Header().Set("ETag", view.ETag())
	w.Header().Set("Cache-Control", "no-store")
	if (imm != "" && imm == view.ETag()) || (polled && rev == view.Revision) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	auditRuntime(s, r, "read")
	writeJSON(w, http.StatusOK, view)
}

// parseWaitRevision reads ?wait (seconds, capped at maxWait) and
// ?revision. Both default to 0: without parameters the handler answers the
// current view at once. polled reports whether a usable ?revision was
// sent, so the handler can answer 304 when the revision the client already
// has is still current. The document gives this operation no 400, so an
// unparseable value is ignored rather than refused.
func parseWaitRevision(q url.Values) (wait time.Duration, rev int64, polled bool) {
	waitQ, revQ := q.Get("wait"), q.Get("revision")
	if secs, err := strconv.ParseFloat(waitQ, 64); err == nil && secs > 0 {
		wait = time.Duration(secs * float64(time.Second))
		if wait > maxWait {
			wait = maxWait
		}
	}
	if n, err := strconv.ParseInt(revQ, 10, 64); err == nil && n >= 0 {
		rev, polled = n, true
	}
	return wait, rev, polled
}

// auditRuntime writes the fetch's audit row: the actor, never the content.
func auditRuntime(s *server, r *http.Request, action string) {
	_ = s.Store.Audit(r.Context(), actor(r).String(), "voice-runtime-"+action, "voice", "")
}

// ackVoiceRuntime serves POST /api/v1/voice-runtime/ack: it records the
// runtime's load acknowledgement, rate limited to 6 per minute per service
// account (spec S-20). The only write a runtime poll loop makes besides the
// call report.
func (s *server) ackVoiceRuntime(w http.ResponseWriter, r *http.Request) {
	if s.Voice == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "voice agents are not configured")
		return
	}
	var in voice.Ack
	if !decode(w, r, &in) {
		return
	}
	if err := s.Voice.Ack(r.Context(), actor(r).UserID, in); err != nil {
		switch {
		case errors.Is(err, voice.ErrRateLimited):
			// The document's Error schema has no rate-limit code; the
			// nearest fixed code carries the message.
			writeError(w, http.StatusForbidden, "conflict", "rate limited: more than six acks in a minute")
		default:
			badRequest(w, "invalid ack: "+err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reportVoiceCall serves POST /api/v1/voice-runtime/calls: talking-agent's
// report for one call (spec S-21).
func (s *server) reportVoiceCall(w http.ResponseWriter, r *http.Request) {
	if s.Voice == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "voice agents are not configured")
		return
	}
	var rep voice.CallReport
	if !decodeReport(w, r, &rep) {
		return
	}
	if err := s.Voice.Report(r.Context(), rep); err != nil {
		switch {
		case errors.Is(err, voice.ErrUnknownAgent):
			writeError(w, http.StatusBadRequest, "bad_request", "unknown agent")
		case errors.Is(err, voice.ErrReportTooLarge):
			// The 413 documents no body.
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		case errors.Is(err, voice.ErrBadReport):
			badRequest(w, "invalid report")
		default:
			s.internal(w, "voice call report", err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getVoiceStatus serves GET /api/v1/voice/status: the runtime status of
// spec S-20 and S-30, red (healthy false) after two minutes of silence
// while an enabled agent exists.
func (s *server) getVoiceStatus(w http.ResponseWriter, r *http.Request) {
	if s.Voice == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "voice agents are not configured")
		return
	}
	st, err := s.Voice.Status(r.Context())
	if err != nil {
		s.internal(w, "voice status", err)
		return
	}
	out := map[string]any{
		"revision": st.Revision, "version": st.Version,
		"healthy": st.Healthy, "agents": st.Agents,
	}
	// lastSeenAt is a string, never null: leave it out until an ack set it.
	if st.LastSeenAt != nil {
		out["lastSeenAt"] = st.LastSeenAt
	}
	writeJSON(w, http.StatusOK, out)
}

// decodeReport is decode with the report's own body limit and 413 answer
// (spec S-21: a body above 128 KiB is refused 413).
func decodeReport(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, voice.MaxReportBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			// The 413 documents no body.
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			badRequest(w, "invalid JSON body: "+jsonProblem(err))
		}
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		badRequest(w, "invalid JSON body: trailing data")
		return false
	}
	return true
}
