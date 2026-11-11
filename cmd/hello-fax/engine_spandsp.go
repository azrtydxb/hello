//go:build spandsp && cgo

package main

/*
#cgo pkg-config: spandsp libtiff-4
#include <spandsp.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

#define HF_QMAX 64
#define HF_PKTMAX 600

typedef struct {
    int t38mode;
    fax_state_t *fax;
    t38_terminal_state_t *t38;
    t30_state_t *t30;
    int done;
    int result;
    int bit_rate;
    int ecm;
    int pages;
    uint8_t pkt[HF_QMAX][HF_PKTMAX];
    int pktlen[HF_QMAX];
    int pktcount[HF_QMAX];
    int qhead, qtail;
} hf_t;

static void hf_phase_e(t30_state_t *s, void *user_data, int result) {
    hf_t *h = (hf_t *) user_data;
    (void) s;
    t30_stats_t st;
    memset(&st, 0, sizeof st);
    t30_get_transfer_statistics(h->t30, &st);
    h->result = result;
    h->bit_rate = st.bit_rate;
    h->ecm = st.error_correcting_mode;
    h->pages = st.pages_tx > st.pages_rx ? st.pages_tx : st.pages_rx;
    h->done = 1;
}

static int hf_t38_tx(t38_core_state_t *s, void *user_data, const uint8_t *buf, int len, int count) {
    hf_t *h = (hf_t *) user_data;
    int next = (h->qtail + 1) % HF_QMAX;
    (void) s;
    if (next == h->qhead || len > HF_PKTMAX || len < 0)
        return -1;
    memcpy(h->pkt[h->qtail], buf, (size_t) len);
    h->pktlen[h->qtail] = len;
    h->pktcount[h->qtail] = count;
    h->qtail = next;
    return 0;
}

static hf_t *hf_new(int t38mode, int calling, const char *ident, const char *file) {
    hf_t *h = (hf_t *) calloc(1, sizeof *h);
    if (!h)
        return NULL;
    h->t38mode = t38mode;
    if (t38mode) {
        h->t38 = t38_terminal_init(NULL, calling, hf_t38_tx, h);
        if (!h->t38) { free(h); return NULL; }
        h->t30 = t38_terminal_get_t30_state(h->t38);
    } else {
        h->fax = fax_init(NULL, calling);
        if (!h->fax) { free(h); return NULL; }
        h->t30 = fax_get_t30_state(h->fax);
        fax_set_transmit_on_idle(h->fax, 1);
    }
    t30_set_tx_ident(h->t30, ident);
    t30_set_ecm_capability(h->t30, 1);
    t30_set_supported_modems(h->t30, T30_SUPPORT_V27TER | T30_SUPPORT_V29 | T30_SUPPORT_V17);
    t30_set_supported_compressions(h->t30, T30_SUPPORT_T4_1D_COMPRESSION | T30_SUPPORT_T4_2D_COMPRESSION | T30_SUPPORT_T6_COMPRESSION);
    t30_set_phase_e_handler(h->t30, hf_phase_e, h);
    if (calling)
        t30_set_tx_file(h->t30, file, -1, -1);
    else {
        t30_set_rx_encoding(h->t30, T4_COMPRESSION_ITU_T6);
        t30_set_rx_file(h->t30, file, -1);
    }
    return h;
}

static int hf_rx(hf_t *h, int16_t *amp, int n) { return fax_rx(h->fax, amp, n); }
static int hf_rx_fill(hf_t *h, int n) { return fax_rx_fillin(h->fax, n); }
static int hf_tx(hf_t *h, int16_t *amp, int n) { return fax_tx(h->fax, amp, n); }
static int hf_t38_rx(hf_t *h, const uint8_t *buf, int len, int seq) {
    return t38_core_rx_ifp_packet(t38_terminal_get_t38_core_state(h->t38), buf, len, (uint16_t) seq);
}
static int hf_t38_timeout(hf_t *h, int samples) { return t38_terminal_send_timeout(h->t38, samples); }
static int hf_pop(hf_t *h, uint8_t *buf, int *count) {
    int len;
    if (h->qhead == h->qtail)
        return -1;
    len = h->pktlen[h->qhead];
    memcpy(buf, h->pkt[h->qhead], (size_t) len);
    *count = h->pktcount[h->qhead];
    h->qhead = (h->qhead + 1) % HF_QMAX;
    return len;
}
static void hf_terminate(hf_t *h) { t30_terminate(h->t30); }
static const char *hf_text(int code) { return t30_completion_code_to_str(code); }
static void hf_free(hf_t *h) {
    if (h->t38mode) {
        t38_terminal_release(h->t38);
        t38_terminal_free(h->t38);
    } else {
        fax_release(h->fax);
        fax_free(h->fax);
    }
    free(h);
}
*/
import "C"

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/azrtydxb/hello/internal/fax"
	"github.com/azrtydxb/hello/internal/media"
)

// engineAvailable reports whether sessions can run.
const engineAvailable = true

// newEngine returns the spandsp engine.
func newEngine() Engine { return spandspEngine{} }

type spandspEngine struct{}

const (
	frameSamples = 160 // 20 ms at 8 kHz
	// A fixed jitter buffer (pbx-gaps S-15): playout starts after this many
	// frames and never adapts; beyond the cap the oldest audio is dropped.
	jitterFrames = 3
	jitterCap    = 40
	flushFrames  = 15 // frames sent after T.30 finishes, so the last frames leave
	t38History   = 3  // redundant IFP packets per UDPTL datagram
)

// Run implements Engine.
func (spandspEngine) Run(ctx context.Context, j Job) (Outcome, error) {
	dir := j.TempDir
	if dir == "" {
		d, err := os.MkdirTemp("", "hello-fax-run-")
		if err != nil {
			return Outcome{}, err
		}
		defer func() { _ = os.RemoveAll(d) }()
		dir = d
	}
	file := filepath.Join(dir, "pages.tif")
	if j.Calling {
		raw, err := fax.WriteTIFF(j.Pages)
		if err != nil {
			return Outcome{}, fmt.Errorf("prepare pages: %w", err)
		}
		if err := os.WriteFile(file, raw, 0o600); err != nil {
			return Outcome{}, err
		}
	}
	ident := C.CString(j.Ident)
	cfile := C.CString(file)
	defer C.free(unsafe.Pointer(ident))
	defer C.free(unsafe.Pointer(cfile))
	t38 := 0
	if j.Mode == ModeT38 {
		t38 = 1
	}
	calling := 0
	if j.Calling {
		calling = 1
	}
	h := C.hf_new(C.int(t38), C.int(calling), ident, cfile)
	if h == nil {
		return Outcome{}, fmt.Errorf("spandsp: cannot start a %s session", j.Mode)
	}
	pace := j.Pace
	if pace <= 0 {
		pace = 20 * time.Millisecond
	}
	var runErr error
	if j.Mode == ModeT38 {
		runErr = runT38(ctx, h, j, pace)
	} else {
		runErr = runG711(ctx, h, j, pace)
	}
	out := Outcome{
		Code: int(h.result), Text: C.GoString(C.hf_text(h.result)),
		PagesDone: int(h.pages), BitRate: int(h.bit_rate), ECM: h.ecm != 0,
	}
	finished := h.done != 0
	C.hf_free(h) // closes the page file
	out.Success = finished && out.Code == 0
	if !finished {
		out.Text = "the call ended before the fax did"
		out.Code = -1
	}
	if !j.Calling {
		if raw, err := os.ReadFile(file); err == nil {
			if pages, err := fax.ReadTIFF(raw); err == nil {
				out.Received = pages
			} else if runErr == nil {
				runErr = fmt.Errorf("read received pages: %w", err)
			}
		}
	}
	return out, runErr
}

// jitter is the fixed receive buffer of G.711 samples.
type jitter struct {
	mu      sync.Mutex
	samples []int16
	playing bool
}

func (b *jitter) push(s []int16) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.samples = append(b.samples, s...)
	if over := len(b.samples) - jitterCap*frameSamples; over > 0 {
		b.samples = b.samples[over:]
	}
}

// frame returns the next 20 ms or ok=false when the buffer has run dry (the
// engine then asks spandsp to conceal the gap).
func (b *jitter) frame() (f []int16, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.playing {
		if len(b.samples) < jitterFrames*frameSamples {
			return nil, false
		}
		b.playing = true
	}
	if len(b.samples) < frameSamples {
		b.playing = false
		return nil, false
	}
	f = append([]int16(nil), b.samples[:frameSamples]...)
	b.samples = b.samples[frameSamples:]
	return f, true
}

type remote struct {
	mu   sync.Mutex
	addr *net.UDPAddr
	ip   net.IP // packets from other hosts are ignored
}

func newRemote(a *net.UDPAddr) *remote {
	r := &remote{addr: a}
	if a != nil {
		r.ip = a.IP
	}
	return r
}

// accept reports whether from may send to us, and latches its port.
func (r *remote) accept(from net.Addr) bool {
	ua, ok := from.(*net.UDPAddr)
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ip != nil && !r.ip.Equal(ua.IP) {
		return false
	}
	if r.ip == nil {
		r.ip = ua.IP
	}
	r.addr = ua
	return true
}

func (r *remote) get() *net.UDPAddr {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.addr
}

func tickEvery(ctx context.Context, pace time.Duration, step func() (stop bool)) {
	t := time.NewTicker(pace)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if step() {
				return
			}
		}
	}
}

func runG711(ctx context.Context, h *C.hf_t, j Job, pace time.Duration) error {
	if j.PT != 0 && j.PT != 8 {
		return fmt.Errorf("payload type %d is not G.711", j.PT)
	}
	rem := newRemote(j.Remote)
	buf := &jitter{}
	readCtx, stopRead := context.WithCancel(ctx)
	readerDone := make(chan struct{})
	defer func() { stopRead(); <-readerDone }()
	go func() {
		defer close(readerDone)
		pkt := make([]byte, 2048)
		for readCtx.Err() == nil {
			_ = j.Conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, from, err := j.Conn.ReadFrom(pkt)
			if err != nil {
				continue
			}
			p, ok := media.ParseRTP(pkt[:n])
			if !ok || p.PayloadType != j.PT || !rem.accept(from) {
				continue
			}
			var pcm []byte
			if j.PT == 0 {
				pcm = media.DecodePCMU(p.Payload)
			} else {
				pcm = media.DecodePCMA(p.Payload)
			}
			s := make([]int16, len(pcm)/2)
			for i := range s {
				s[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
			}
			buf.push(s)
		}
	}()
	var seq uint16
	var ts uint32
	ssrc := uint32(time.Now().UnixNano())
	amp := make([]C.int16_t, frameSamples)
	out := make([]byte, 2*frameSamples)
	flush := 0
	tickEvery(ctx, pace, func() bool {
		if f, ok := buf.frame(); ok {
			for i, v := range f {
				amp[i] = C.int16_t(v)
			}
			C.hf_rx(h, &amp[0], frameSamples)
		} else {
			C.hf_rx_fill(h, frameSamples)
		}
		for i := range amp {
			amp[i] = 0
		}
		n := int(C.hf_tx(h, &amp[0], frameSamples))
		for i := 0; i < frameSamples; i++ {
			v := int16(0)
			if i < n {
				v = int16(amp[i])
			}
			binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
		}
		var payload []byte
		if j.PT == 0 {
			payload = media.EncodePCMU(out)
		} else {
			payload = media.EncodePCMA(out)
		}
		if to := rem.get(); to != nil {
			_, _ = j.Conn.WriteTo(media.BuildRTP(j.PT, seq, ts, ssrc, seq == 0, payload), to)
		}
		seq++
		ts += frameSamples
		if h.done != 0 {
			flush++
		}
		return flush >= flushFrames
	})
	if ctx.Err() != nil && h.done == 0 {
		C.hf_terminate(h)
	}
	return nil
}

type t38In struct {
	seq uint16
	ifp []byte
}

func runT38(ctx context.Context, h *C.hf_t, j Job, pace time.Duration) error {
	rem := newRemote(j.Remote)
	in := make(chan t38In, 256)
	readCtx, stopRead := context.WithCancel(ctx)
	readerDone := make(chan struct{})
	defer func() { stopRead(); <-readerDone }()
	go func() {
		defer close(readerDone)
		pkt := make([]byte, 4096)
		var last uint16
		have := false
		for readCtx.Err() == nil {
			_ = j.Conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, from, err := j.Conn.ReadFrom(pkt)
			if err != nil || !rem.accept(from) {
				continue
			}
			u, err := fax.ParseUDPTL(pkt[:n])
			if err != nil {
				continue
			}
			seqs, ifps := u.Recovered(last, have)
			for i := range seqs {
				select {
				case in <- t38In{seqs[i], ifps[i]}:
				default: // the engine is behind: drop, the sender repeats
				}
				last, have = seqs[i], true
			}
		}
	}()
	var txSeq uint16
	var history [][]byte // newest first
	buf := make([]byte, 600)
	flush := 0
	tickEvery(ctx, pace, func() bool {
	drain:
		for {
			select {
			case p := <-in:
				if len(p.ifp) > 0 {
					C.hf_t38_rx(h, (*C.uint8_t)(unsafe.Pointer(&p.ifp[0])), C.int(len(p.ifp)), C.int(p.seq))
				}
			default:
				break drain
			}
		}
		C.hf_t38_timeout(h, frameSamples)
		for {
			var count C.int
			n := int(C.hf_pop(h, (*C.uint8_t)(unsafe.Pointer(&buf[0])), &count))
			if n < 0 {
				break
			}
			ifp := append([]byte(nil), buf[:n]...)
			copies := int(count)
			if copies < 1 {
				copies = 1
			}
			if copies > 3 {
				copies = 3
			}
			for c := 0; c < copies; c++ {
				u := fax.UDPTL{Seq: txSeq, Primary: ifp, Secondary: history}
				if len(u.Secondary) > t38History {
					u.Secondary = u.Secondary[:t38History]
				}
				if raw, err := u.Marshal(); err == nil {
					if to := rem.get(); to != nil {
						_, _ = j.Conn.WriteTo(raw, to)
					}
				}
				txSeq++
				history = append([][]byte{ifp}, history...)
				if len(history) > t38History {
					history = history[:t38History]
				}
			}
		}
		if h.done != 0 {
			flush++
		}
		return flush >= flushFrames
	})
	if ctx.Err() != nil && h.done == 0 {
		C.hf_terminate(h)
	}
	return nil
}
