package api

// Remote resync and reboot (spec S-5): the API queues a check-sync push,
// a hello-sip node sends it and writes the result, and the pushes list
// reports it (ok, not_registered, timeout, auth_failed, failed).

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/azrtydxb/hello/internal/store"
)

const maxPushList = 100

func (s *server) pushPhone(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok || s.provDisabled(w) {
			return
		}
		p, err := s.ProvStore.QueuePush(r.Context(), actor(r).String(), id, kind)
		switch {
		case errors.Is(err, store.ErrPushUnbound):
			writeError(w, http.StatusConflict, "conflict", "the phone has no enabled device to push to")
		case err != nil:
			s.provError(w, "phone", err)
		default:
			writeJSON(w, http.StatusAccepted, p)
		}
	}
}

// pushBulk queues the push for a phone group (path id) or, when group is
// false, for every phone.
func (s *server) pushBulk(kind string, group bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var gid int64
		if group {
			id, ok := pathID(w, r)
			if !ok {
				return
			}
			gid = id
		}
		if s.provDisabled(w) {
			return
		}
		queued, skipped, err := s.ProvStore.QueuePushGroup(r.Context(), actor(r).String(), gid, kind)
		if err != nil {
			s.provError(w, "phone group", err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]int{"queued": queued, "skipped": skipped})
	}
}

func (s *server) listPhonePushes(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPushList {
			badRequest(w, "limit must be 1-100")
			return
		}
		limit = n
	}
	if _, err := s.ProvStore.GetPhone(r.Context(), id); err != nil {
		s.provError(w, "phone", err)
		return
	}
	ps, err := s.ProvStore.ListPhonePushes(r.Context(), id, limit)
	if err != nil {
		s.internal(w, "list phone pushes", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ps))
}

func (s *server) latestPhonePushes(w http.ResponseWriter, r *http.Request) {
	if s.provDisabled(w) {
		return
	}
	ps, err := s.ProvStore.LatestPhonePushes(r.Context())
	if err != nil {
		s.internal(w, "list phone pushes", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ps))
}
