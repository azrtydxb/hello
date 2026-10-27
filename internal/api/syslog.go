package api

// Phone syslog lines (phone-feature-parity S-27): what the sink kept for a
// phone, and for source addresses no phone owns.

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/azrtydxb/hello/internal/livestate"
)

// SyslogLive reads the lines the syslog sink stored; *LazyValkey implements
// it.
type SyslogLive interface {
	SyslogPhoneLines(ctx context.Context, phoneID int64, limit int) ([]livestate.SyslogLine, error)
	SyslogUnknownLines(ctx context.Context, ip string, limit int) ([]livestate.SyslogLine, error)
	SyslogUnknownSources(ctx context.Context) ([]string, error)
}

// syslogLines is the body of both line endpoints.
type syslogLines struct {
	Items []livestate.SyslogLine `json:"items"`
	// Cap is the most lines kept and RetentionSeconds how long they outlive
	// the newest one.
	Cap              int   `json:"cap"`
	RetentionSeconds int64 `json:"retentionSeconds"`
}

const defaultSyslogLimit = 200

func syslogLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return defaultSyslogLimit, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > livestate.SyslogPhoneCap {
		badRequest(w, "limit must be 1-10000")
		return 0, false
	}
	return n, true
}

func (s *server) syslogBody(lines []livestate.SyslogLine, max int) syslogLines {
	if lines == nil {
		lines = []livestate.SyslogLine{}
	}
	return syslogLines{Items: lines, Cap: max, RetentionSeconds: int64(livestate.SyslogTTL.Seconds())}
}

// phoneSyslog is GET /api/v1/phones/{id}/syslog: the phone's lines, newest
// first.
func (s *server) phoneSyslog(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok || s.provDisabled(w) {
		return
	}
	limit, ok := syslogLimit(w, r)
	if !ok {
		return
	}
	if _, err := s.ProvStore.GetPhone(r.Context(), id); err != nil {
		s.provError(w, "phone", err)
		return
	}
	if s.Syslog == nil {
		s.liveDown(w, "phone syslog", errors.New("no syslog store configured"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	lines, err := s.Syslog.SyslogPhoneLines(ctx, id, limit)
	if err != nil {
		s.liveDown(w, "phone syslog", err)
		return
	}
	writeJSON(w, http.StatusOK, s.syslogBody(lines, livestate.SyslogPhoneCap))
}

// unknownSyslog is GET /api/v1/syslog/unknown: without ip, the source
// addresses that sent lines no phone owns; with ip, that address's lines.
func (s *server) unknownSyslog(w http.ResponseWriter, r *http.Request) {
	limit, ok := syslogLimit(w, r)
	if !ok {
		return
	}
	if s.Syslog == nil {
		s.liveDown(w, "unknown syslog", errors.New("no syslog store configured"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	ip := r.URL.Query().Get("ip")
	if ip == "" {
		srcs, err := s.Syslog.SyslogUnknownSources(ctx)
		if err != nil {
			s.liveDown(w, "unknown syslog", err)
			return
		}
		if srcs == nil {
			srcs = []string{}
		}
		writeJSON(w, http.StatusOK, items(srcs))
		return
	}
	if _, err := netip.ParseAddr(ip); err != nil {
		badRequest(w, "ip must be an IPv4 or IPv6 address")
		return
	}
	lines, err := s.Syslog.SyslogUnknownLines(ctx, ip, limit)
	if err != nil {
		s.liveDown(w, "unknown syslog", err)
		return
	}
	writeJSON(w, http.StatusOK, s.syslogBody(lines, livestate.SyslogUnknownCap))
}
