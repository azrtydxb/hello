// Package syslogsink is Hello's syslog receiver for phones
// (phone-feature-parity S-27): it takes syslog over UDP and TLS, redacts
// each line, caps the rate per phone, and keeps the last lines per phone in
// Valkey for a day. A line from an address no phone owns is kept under
// that address, not dropped.
package syslogsink

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
)

// Lines is where accepted lines are kept; *livestate.Store implements it.
type Lines interface {
	AppendSyslogPhone(ctx context.Context, phoneID int64, l livestate.SyslogLine) error
	AppendSyslogUnknown(ctx context.Context, l livestate.SyslogLine) error
}

// Phones maps source IPs to the phone that owns each, from the address the
// phone last fetched its configuration from. An address shared by more than
// one phone (NAT) is not in the map: its lines are kept as unknown rather
// than attributed to a guess.
type Phones interface {
	PhoneIPs(ctx context.Context) (map[netip.Addr]int64, error)
}

// Config configures a Sink.
type Config struct {
	// UDPAddr and TLSAddr are the listen addresses; empty turns that
	// listener off. TLSConfig is required with TLSAddr.
	UDPAddr, TLSAddr string
	TLSConfig        *tls.Config
	// RatePerSec and Burst are the per-phone cap (a token bucket); lines over
	// it are dropped and counted. Zero values mean 20 and 200.
	RatePerSec float64
	Burst      int
	// RefreshEvery is how often the phone-IP map is reloaded (default 30 s).
	RefreshEvery time.Duration
	// MaxConns bounds concurrent TLS connections (default 256).
	MaxConns int
	Lines    Lines
	Phones   Phones
	Log      *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Sink is the syslog receiver.
type Sink struct {
	cfg Config
	now func() time.Time

	mu      sync.Mutex
	ips     map[netip.Addr]int64
	loaded  time.Time
	buckets map[string]*bucket

	udp net.PacketConn
	tcp net.Listener
}

// bucket is a token bucket with a count of lines it dropped since the last
// one it let through.
type bucket struct {
	tokens  float64
	last    time.Time
	dropped int
}

// New returns a Sink; Start it to listen.
func New(cfg Config) *Sink {
	if cfg.RatePerSec <= 0 {
		cfg.RatePerSec = 20
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 200
	}
	if cfg.RefreshEvery <= 0 {
		cfg.RefreshEvery = 30 * time.Second
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 256
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	s := &Sink{cfg: cfg, now: cfg.Now, buckets: map[string]*bucket{}}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Start binds the configured listeners and serves them until ctx ends.
func (s *Sink) Start(ctx context.Context) error {
	if s.cfg.UDPAddr != "" {
		pc, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", s.cfg.UDPAddr)
		if err != nil {
			return fmt.Errorf("syslog udp: %w", err)
		}
		s.udp = pc
	}
	if s.cfg.TLSAddr != "" {
		if s.cfg.TLSConfig == nil {
			s.closeListeners()
			return errors.New("syslog tls: no certificate")
		}
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.cfg.TLSAddr)
		if err != nil {
			s.closeListeners()
			return fmt.Errorf("syslog tls: %w", err)
		}
		s.tcp = tls.NewListener(ln, s.cfg.TLSConfig)
	}
	if s.udp != nil {
		go s.serveUDP(ctx)
	}
	if s.tcp != nil {
		go s.serveTLS(ctx)
	}
	go func() {
		<-ctx.Done()
		s.closeListeners()
	}()
	return nil
}

// UDPAddr is the bound UDP address, nil when off.
func (s *Sink) UDPAddr() net.Addr {
	if s.udp == nil {
		return nil
	}
	return s.udp.LocalAddr()
}

// TLSAddr is the bound TLS address, nil when off.
func (s *Sink) TLSAddr() net.Addr {
	if s.tcp == nil {
		return nil
	}
	return s.tcp.Addr()
}

func (s *Sink) closeListeners() {
	if s.udp != nil {
		_ = s.udp.Close()
	}
	if s.tcp != nil {
		_ = s.tcp.Close()
	}
}

// maxDatagram bounds what is read per datagram or frame; the stored line is
// cut shorter (MaxLineBytes).
const maxDatagram = 8192

func (s *Sink) serveUDP(ctx context.Context) {
	buf := make([]byte, maxDatagram)
	for {
		n, addr, err := s.udp.ReadFrom(buf)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				s.cfg.Log.Warn("syslog udp read", "error", err)
			}
			return
		}
		if ua, ok := addr.(*net.UDPAddr); ok {
			s.Handle(ctx, ua.AddrPort().Addr(), string(buf[:n]))
		}
	}
}

func (s *Sink) serveTLS(ctx context.Context) {
	sem := make(chan struct{}, s.cfg.MaxConns)
	for {
		c, err := s.tcp.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				s.cfg.Log.Warn("syslog tls accept", "error", err)
			}
			return
		}
		select {
		case sem <- struct{}{}:
		default:
			_ = c.Close()
			continue
		}
		go func() {
			defer func() { <-sem }()
			s.serveConn(ctx, c)
		}()
	}
}

// idleTimeout closes a TLS connection that sends nothing for this long.
const idleTimeout = 5 * time.Minute

func (s *Sink) serveConn(ctx context.Context, c net.Conn) {
	defer func() { _ = c.Close() }()
	ta, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return
	}
	src := ta.AddrPort().Addr()
	done := make(chan struct{})
	defer close(done)
	go func() { // a stopped sink closes its connections
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-done:
		}
	}()
	r := bufio.NewReaderSize(c, maxDatagram)
	for {
		_ = c.SetReadDeadline(time.Now().Add(idleTimeout))
		msg, err := readFrame(r)
		if msg != "" {
			s.Handle(ctx, src, msg)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				s.cfg.Log.Debug("syslog tls read", "source", src.String(), "error", err)
			}
			return
		}
	}
}

// readFrame reads one message in RFC 6587 framing: octet-counting
// ("<len> <msg>") when the first byte is a digit, else newline-delimited.
func readFrame(r *bufio.Reader) (string, error) {
	b, err := r.Peek(1)
	if err != nil {
		return "", err
	}
	if b[0] >= '1' && b[0] <= '9' {
		lenStr, err := r.ReadString(' ')
		if err != nil {
			return "", err
		}
		n, perr := strconv.Atoi(strings.TrimSuffix(lenStr, " "))
		if perr != nil || n < 1 || n > maxDatagram {
			return "", fmt.Errorf("bad octet count %q", strings.TrimSpace(lenStr))
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	}
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if len(line) < maxDatagram {
			line = append(line, chunk...)
		}
		if err != nil || !isPrefix {
			return string(line), err
		}
	}
}

// Handle takes one received message from src: it is parsed and redacted,
// attributed to its phone (else kept under its source address), rate
// capped per owner and stored. Store errors are logged, not returned: a
// phone cannot act on them.
func (s *Sink) Handle(ctx context.Context, src netip.Addr, raw string) {
	src = src.Unmap()
	now := s.now()
	line, ok := Parse(raw, src.String(), now)
	if !ok {
		return
	}
	phoneID, known := s.owner(ctx, src, now)
	key := "ip:" + src.String()
	if known {
		key = "phone:" + strconv.FormatInt(phoneID, 10)
	}
	allowed, dropped := s.allow(key, now)
	if !allowed {
		return
	}
	put := func(l livestate.SyslogLine) error {
		if known {
			return s.cfg.Lines.AppendSyslogPhone(ctx, phoneID, l)
		}
		return s.cfg.Lines.AppendSyslogUnknown(ctx, l)
	}
	if dropped > 0 {
		note := livestate.SyslogLine{At: now.UTC(), Source: line.Source, Severity: -1,
			Message: fmt.Sprintf("[hello] %d lines dropped by the rate cap of %g lines/s", dropped, s.cfg.RatePerSec)}
		if err := put(note); err != nil {
			s.cfg.Log.Warn("syslog store", "error", err)
		}
	}
	if err := put(line); err != nil {
		s.cfg.Log.Warn("syslog store", "error", err)
	}
}

// owner returns the phone that owns src, reloading the map when it is
// stale. A failed reload keeps the old map.
func (s *Sink) owner(ctx context.Context, src netip.Addr, now time.Time) (int64, bool) {
	s.mu.Lock()
	stale := s.ips == nil || now.Sub(s.loaded) >= s.cfg.RefreshEvery
	if stale {
		s.loaded = now // one reload at a time, and no storm after a failure
	}
	s.mu.Unlock()
	if stale && s.cfg.Phones != nil {
		m, err := s.cfg.Phones.PhoneIPs(ctx)
		if err != nil {
			s.cfg.Log.Warn("syslog phone addresses", "error", err)
		} else {
			s.mu.Lock()
			s.ips = m
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.ips[src]
	return id, ok
}

// allow takes a token from key's bucket. When it lets a line through after
// some were dropped it also returns how many.
func (s *Sink) allow(key string, now time.Time) (ok bool, dropped int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buckets[key]
	if b == nil {
		if len(s.buckets) >= 10000 {
			s.sweep(now)
		}
		b = &bucket{tokens: float64(s.cfg.Burst), last: now}
		s.buckets[key] = b
	}
	if d := now.Sub(b.last).Seconds(); d > 0 {
		b.tokens = min(float64(s.cfg.Burst), b.tokens+d*s.cfg.RatePerSec)
		b.last = now
	}
	if b.tokens < 1 {
		b.dropped++
		return false, 0
	}
	b.tokens--
	dropped, b.dropped = b.dropped, 0
	return true, dropped
}

// sweep forgets buckets idle long enough to have refilled, bounding memory
// against a flood of spoofed sources. Called with s.mu held.
func (s *Sink) sweep(now time.Time) {
	for k, b := range s.buckets {
		if now.Sub(b.last) > time.Minute && b.dropped == 0 {
			delete(s.buckets, k)
		}
	}
}
