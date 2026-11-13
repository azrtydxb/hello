package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/fax"
)

// maxSessions bounds the sessions the service holds (running or waiting to
// be collected).
const maxSessions = 256

// Timing.
const (
	maxSessionTime = 45 * time.Minute // a fax call never runs longer
	keepFinished   = 15 * time.Minute // a result is kept this long for collection
)

type server struct {
	eng     Engine
	ras     fax.Rasteriser
	token   string
	portMin int
	portMax int
	log     *slog.Logger
	pace    time.Duration // zero: real time; tests run faster
	// mediaHost is the address hello-sip sends media to (HELLO_FAX_MEDIA_HOST,
	// the pod IP); empty uses the address the request arrived on.
	mediaHost string
	ready     bool
	mu        sync.Mutex
	sessions  map[string]*session
}

func newServer(eng Engine, ras fax.Rasteriser, token string, portMin, portMax int, log *slog.Logger) *server {
	return &server{eng: eng, ras: ras, token: token, portMin: portMin, portMax: portMax, log: log,
		ready: engineAvailable, sessions: map[string]*session{}}
}

type session struct {
	id      string
	host    string
	srv     *server
	recv    bool
	ident   string
	pt      uint8
	pages   []fax.Page
	conn    net.PacketConn
	port    int
	remote  *net.UDPAddr
	started time.Time

	mu       sync.Mutex
	mode     Mode
	cancel   context.CancelFunc // ends the session
	runStop  context.CancelFunc // ends the current engine run only
	restart  bool
	wantT38  bool
	done     chan struct{}
	finished time.Time
	out      Outcome
	pdf      []byte
	trunc    bool
	runErr   error
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.auth(s.healthz))
	mux.HandleFunc("POST /v1/rasterise", s.auth(s.rasterise))
	mux.HandleFunc("POST /v1/sessions", s.auth(s.start))
	mux.HandleFunc("GET /v1/sessions/{id}", s.auth(s.status))
	mux.HandleFunc("POST /v1/sessions/{id}/t38", s.auth(s.t38))
	mux.HandleFunc("POST /v1/sessions/{id}/stop", s.auth(s.stop))
	mux.HandleFunc("GET /v1/sessions/{id}/pdf", s.auth(s.getPDF))
	mux.HandleFunc("DELETE /v1/sessions/{id}", s.auth(s.forget))
	return mux
}

func (s *server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !fax.BearerOK(r.Header.Get("Authorization"), s.token) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready {
		writeErr(w, http.StatusServiceUnavailable, ErrNoEngine.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func readPDF(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, fax.MaxSendBytes+1))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the PDF is larger than %d bytes", fax.MaxSendBytes))
		return nil, false
	}
	return body, true
}

func (s *server) rasterise(w http.ResponseWriter, r *http.Request) {
	pdf, ok := readPDF(w, r)
	if !ok {
		return
	}
	pages, err := s.ras.Rasterise(r.Context(), pdf)
	if err != nil {
		code := http.StatusUnprocessableEntity
		if !errors.Is(err, fax.ErrRaster) && !errors.Is(err, fax.ErrTooManyPages) && !errors.Is(err, fax.ErrTooLarge) {
			code = http.StatusServiceUnavailable
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"pages": len(pages)})
}

func (s *server) openPort() (net.PacketConn, int, error) {
	n := s.portMax - s.portMin + 1
	off, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return nil, 0, err
	}
	for i := 0; i < n; i++ {
		port := s.portMin + (int(off.Int64())+i)%n
		c, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", ":"+strconv.Itoa(port))
		if err == nil {
			return c, port, nil
		}
	}
	return nil, 0, errors.New("no free media port")
}

func (s *server) start(w http.ResponseWriter, r *http.Request) {
	if !s.ready {
		writeErr(w, http.StatusServiceUnavailable, ErrNoEngine.Error())
		return
	}
	q := r.URL.Query()
	dir, mode := q.Get("direction"), Mode(q.Get("mode"))
	if (dir != fax.SessionReceive && dir != fax.SessionSend) || (mode != ModeG711 && mode != ModeT38) {
		writeErr(w, http.StatusBadRequest, "direction must be receive or send and mode g711 or t38")
		return
	}
	pt, err := strconv.Atoi(q.Get("pt"))
	if err != nil || (pt != 0 && pt != 8) {
		writeErr(w, http.StatusBadRequest, "pt must be 0 (PCMU) or 8 (PCMA)")
		return
	}
	remote, err := net.ResolveUDPAddr("udp", q.Get("remote"))
	if err != nil || remote.IP == nil || remote.Port == 0 {
		writeErr(w, http.StatusBadRequest, "remote must be host:port")
		return
	}
	ident := fax.Ident(q.Get("ident"))
	ss := &session{srv: s, recv: dir == fax.SessionReceive, ident: ident, pt: uint8(pt), //nolint:gosec // G115: pt was checked to be 0 or 8
		remote: remote,
		mode:   mode, wantT38: dir == fax.SessionSend && q.Get("t38") == "1", started: time.Now(), done: make(chan struct{})}
	if !ss.recv {
		pdf, ok := readPDF(w, r)
		if !ok {
			return
		}
		ss.pages, err = s.ras.Rasterise(r.Context(), pdf)
		if err != nil {
			code := http.StatusUnprocessableEntity
			if !errors.Is(err, fax.ErrRaster) && !errors.Is(err, fax.ErrTooManyPages) && !errors.Is(err, fax.ErrTooLarge) {
				code = http.StatusServiceUnavailable
			}
			writeErr(w, code, err.Error())
			return
		}
	}
	idb := make([]byte, 12)
	_, _ = rand.Read(idb)
	ss.id = hex.EncodeToString(idb)
	ss.host = s.mediaHost
	if ss.host == "" {
		if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			ss.host, _, _ = net.SplitHostPort(la.String())
		}
	}
	s.mu.Lock()
	s.reapLocked()
	if len(s.sessions) >= maxSessions {
		s.mu.Unlock()
		writeErr(w, http.StatusServiceUnavailable, "too many sessions")
		return
	}
	ss.conn, ss.port, err = s.openPort()
	if err != nil {
		s.mu.Unlock()
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	s.sessions[ss.id] = ss
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), maxSessionTime)
	ss.cancel = cancel
	go ss.run(ctx)
	writeJSON(w, http.StatusOK, ss.info())
}

// run drives the engine; a switch to T.38 ends one run and starts the next on
// the same socket.
func (ss *session) run(ctx context.Context) {
	defer close(ss.done)
	defer func() { _ = ss.conn.Close() }()
	for {
		rctx, rcancel := context.WithCancel(ctx)
		ss.mu.Lock()
		mode := ss.mode
		ss.runStop = rcancel
		ss.mu.Unlock()
		out, err := ss.srv.eng.Run(rctx, Job{Mode: mode, Calling: !ss.recv, Ident: ss.ident, Pages: ss.pages,
			PT: ss.pt, Conn: ss.conn, Remote: ss.remote, Pace: ss.srv.pace})
		rcancel()
		ss.mu.Lock()
		if ss.restart && ctx.Err() == nil {
			// The restart cancelled this run only; the next starts T.30 over as a T.38 terminal.
			ss.restart = false
			ss.mode = ModeT38
			ss.wantT38 = false
			ss.mu.Unlock()
			continue
		}
		ss.out, ss.runErr, ss.finished = out, err, time.Now()
		ss.mu.Unlock()
		break
	}
	ss.finish()
}

func (ss *session) finish() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if !ss.recv || len(ss.out.Received) == 0 {
		return
	}
	pages := ss.out.Received
	if len(pages) > fax.MaxReceivePages {
		pages, ss.trunc = pages[:fax.MaxReceivePages], true
	}
	for len(pages) > 0 {
		pdf, err := fax.EncodePDF(pages)
		if err == nil {
			ss.pdf = pdf
			return
		}
		if !errors.Is(err, fax.ErrTooLarge) {
			ss.runErr = err
			return
		}
		pages, ss.trunc = pages[:len(pages)/2], true // keep the first pages that fit
	}
}

func (ss *session) info() SessionInfoJSON {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	info := fax.SessionInfo{ID: ss.id, Host: ss.host, Port: ss.port, Mode: string(ss.mode), State: "running", WantT38: ss.wantT38 && ss.mode == ModeG711}
	if !ss.finished.IsZero() {
		pages := ss.out.PagesDone
		if ss.recv && len(ss.out.Received) > 0 && len(ss.out.Received) < pages {
			pages = len(ss.out.Received)
		}
		if ss.recv && pages == 0 {
			pages = len(ss.out.Received)
		}
		info.State, info.Success, info.Code, info.Text = "done", ss.out.Success, ss.out.Code, ss.out.Text
		info.Pages, info.BitRate, info.ECM, info.HasPDF, info.Truncated = pages, ss.out.BitRate, ss.out.ECM, len(ss.pdf) > 0, ss.trunc
		if ss.runErr != nil {
			info.Success = false
			if info.Text == "" {
				info.Text = ss.runErr.Error()
			}
		}
	}
	return info
}

// SessionInfoJSON is the wire shape of a session.
type SessionInfoJSON = fax.SessionInfo

func (s *server) find(w http.ResponseWriter, r *http.Request) *session {
	s.mu.Lock()
	ss := s.sessions[r.PathValue("id")]
	s.mu.Unlock()
	if ss == nil {
		writeErr(w, http.StatusNotFound, "no such session")
	}
	return ss
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	if ss := s.find(w, r); ss != nil {
		writeJSON(w, http.StatusOK, ss.info())
	}
}

// t38 restarts a running G.711 session as a T.38 terminal. T.30 starts over,
// which is right at the moment it is asked for (CNG heard, or before the
// first page): nothing has been exchanged yet.
func (s *server) t38(w http.ResponseWriter, r *http.Request) {
	ss := s.find(w, r)
	if ss == nil {
		return
	}
	ss.mu.Lock()
	if !ss.finished.IsZero() || ss.mode != ModeG711 || ss.restart {
		ss.mu.Unlock()
		writeJSON(w, http.StatusOK, ss.info())
		return
	}
	ss.restart = true
	stop := ss.runStop
	ss.mu.Unlock()
	if stop != nil {
		stop()
	}
	writeJSON(w, http.StatusOK, ss.info())
}

func (s *server) stop(w http.ResponseWriter, r *http.Request) {
	ss := s.find(w, r)
	if ss == nil {
		return
	}
	ss.cancel()
	select {
	case <-ss.done:
	case <-time.After(10 * time.Second):
	}
	writeJSON(w, http.StatusOK, ss.info())
}

func (s *server) getPDF(w http.ResponseWriter, r *http.Request) {
	ss := s.find(w, r)
	if ss == nil {
		return
	}
	ss.mu.Lock()
	pdf := ss.pdf
	ss.mu.Unlock()
	if len(pdf) == 0 {
		writeErr(w, http.StatusNotFound, "no pages were received")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	_, _ = w.Write(pdf)
}

func (s *server) forget(w http.ResponseWriter, r *http.Request) {
	ss := s.find(w, r)
	if ss == nil {
		return
	}
	ss.cancel()
	s.mu.Lock()
	delete(s.sessions, ss.id)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// reapLocked drops sessions whose result nobody collected.
func (s *server) reapLocked() {
	for id, ss := range s.sessions {
		ss.mu.Lock()
		old := !ss.finished.IsZero() && time.Since(ss.finished) > keepFinished
		ss.mu.Unlock()
		if old {
			delete(s.sessions, id)
		}
	}
}

// Shutdown ends every session.
func (s *server) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.sessions {
		ss.cancel()
	}
}
