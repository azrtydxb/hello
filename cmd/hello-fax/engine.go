package main

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/azrtydxb/hello/internal/fax"
)

// Mode is how the fax signal travels between hello-sip's relay and the
// engine: G.711 audio with the T.30 modems inside it, or T.38 UDPTL packets.
type Mode string

const (
	ModeG711 Mode = "g711"
	ModeT38  Mode = "t38"
)

// Job is one T.30 session.
type Job struct {
	Mode Mode
	// Calling is true when this side placed the call (it transmits) and false
	// when it answered (it receives).
	Calling bool
	// Ident is the station identifier sent in the T.30 frames (digits).
	Ident string
	// Pages are the pages to transmit; empty when receiving.
	Pages []fax.Page
	// PT is the G.711 RTP payload type, 0 (PCMU) or 8 (PCMA).
	PT uint8
	// Conn is the socket the media arrives on and leaves from. Remote is where
	// packets go until the first one arrives and re-aims them.
	Conn   net.PacketConn
	Remote *net.UDPAddr
	// TempDir is a private directory for the page files; the engine removes
	// nothing outside it. Empty means a fresh temporary directory.
	TempDir string
	// Pace is the time of one 20 ms frame; zero means real time. Tests run
	// faster than real time.
	Pace time.Duration
}

// Outcome is what a finished session produced.
type Outcome struct {
	// Success is true when T.30 ended with the "OK" completion code.
	Success bool
	// Code and Text are the T.30 completion code and its name.
	Code int
	Text string
	// Received are the pages written by the receiver (also on failure, when
	// some pages arrived: a partial fax keeps them).
	Received []fax.Page
	// PagesDone is the number of pages transferred.
	PagesDone int
	// BitRate is the modem rate of the last page and ECM whether error
	// correction was used.
	BitRate int
	ECM     bool
}

// ErrNoEngine is returned when the binary was built without spandsp.
var ErrNoEngine = errors.New("hello-fax was built without the spandsp engine (build tag spandsp)")

// Engine runs T.30 sessions.
type Engine interface {
	Run(ctx context.Context, j Job) (Outcome, error)
}
