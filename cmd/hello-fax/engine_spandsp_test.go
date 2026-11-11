//go:build spandsp && cgo

package main

import (
	"bytes"
	"context"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/fax"
)

// pair opens two UDP sockets on loopback and aims each at the other.
func pair(t *testing.T) (a, b net.PacketConn, aa, ba *net.UDPAddr) {
	t.Helper()
	var err error
	a, err = net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b, err = net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b, a.LocalAddr().(*net.UDPAddr), b.LocalAddr().(*net.UDPAddr)
}

func samplePages() []fax.Page {
	mk := func(seed int) fax.Page {
		p := fax.NewPage(fax.FaxPageWidth, 220, fax.FineXDPI, fax.FineYDPI)
		for y := 0; y < p.Height; y++ {
			for x := 0; x < p.Width; x++ {
				// Bars, a diagonal and a block: runs of many lengths for the T.4 coder.
				if (x/(13+seed))%2 == 0 && y%40 < 6 || x == y+seed*3 || (x > 300 && x < 700 && y > 100 && y < 160) {
					p.Set(x, y)
				}
			}
		}
		return p
	}
	return []fax.Page{mk(0), mk(1)}
}

// runFax sends pages from one engine to another over a real socket pair and
// returns both outcomes.
func runFax(t *testing.T, mode Mode, pt uint8, pages []fax.Page) (tx, rx Outcome) {
	t.Helper()
	ca, cb, aa, ba := pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	type res struct {
		o   Outcome
		err error
	}
	rxc, txc := make(chan res, 1), make(chan res, 1)
	eng := newEngine()
	go func() {
		o, err := eng.Run(ctx, Job{Mode: mode, Calling: false, Ident: "5550002", PT: pt, Conn: cb, Remote: aa, Pace: 2 * time.Millisecond})
		rxc <- res{o, err}
	}()
	go func() {
		o, err := eng.Run(ctx, Job{Mode: mode, Calling: true, Ident: "5550001", Pages: pages, PT: pt, Conn: ca, Remote: ba, Pace: 2 * time.Millisecond})
		txc <- res{o, err}
	}()
	r, x := <-rxc, <-txc
	if r.err != nil || x.err != nil {
		t.Fatalf("rx err %v, tx err %v", r.err, x.err)
	}
	return x.o, r.o
}

func checkReceived(t *testing.T, sent []fax.Page, tx, rx Outcome) {
	t.Helper()
	if !tx.Success || !rx.Success {
		t.Fatalf("transmit %+v, receive %+v", tx, rx)
	}
	if len(rx.Received) != len(sent) {
		t.Fatalf("received %d pages, sent %d", len(rx.Received), len(sent))
	}
	for i, p := range rx.Received {
		if p.Width != sent[i].Width || p.Height != sent[i].Height {
			t.Fatalf("page %d is %dx%d, sent %dx%d", i+1, p.Width, p.Height, sent[i].Width, sent[i].Height)
		}
		if !bytes.Equal(p.Data, sent[i].Data) {
			t.Fatalf("page %d differs from what was sent", i+1)
		}
	}
	// The received pages become the PDF the user gets.
	pdf, err := fax.EncodePDF(rx.Received)
	if err != nil {
		t.Fatal(err)
	}
	back, err := fax.DecodePDF(pdf)
	if err != nil || len(back) != len(sent) || !bytes.Equal(back[0].Data, sent[0].Data) {
		t.Fatalf("PDF does not hold the sent pages: %v", err)
	}
}

// TestFaxPipelineG711 sends a generated two-page fax through the T.30 modems
// over G.711 (u-law and A-law) and decodes it.
func TestFaxPipelineG711(t *testing.T) {
	for _, pt := range []uint8{0, 8} {
		pages := samplePages()
		tx, rx := runFax(t, ModeG711, pt, pages)
		checkReceived(t, pages, tx, rx)
	}
}

// TestFaxPipelineT38 does the same over T.38 UDPTL.
func TestFaxPipelineT38(t *testing.T) {
	pages := samplePages()
	tx, rx := runFax(t, ModeT38, 0, pages)
	checkReceived(t, pages, tx, rx)
}

// relayPair stands in for hello-sip's relay between two engines: each engine
// sends to its own socket, and what arrives there goes to the other engine's
// port from the other socket, so each engine latches the socket it was given.
type relayPair struct{ a, b *net.UDPConn }

func newRelayPair(t *testing.T) *relayPair {
	t.Helper()
	mk := func() *net.UDPConn {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	return &relayPair{a: mk(), b: mk()}
}

func (r *relayPair) run(toA, toB int) {
	pump := func(from *net.UDPConn, to *net.UDPAddr, via *net.UDPConn) {
		buf := make([]byte, 4096)
		for {
			n, _, err := from.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = via.WriteToUDP(buf[:n], to)
		}
	}
	go pump(r.a, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: toB}, r.b)
	go pump(r.b, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: toA}, r.a)
}

// TestFaxPipelineFromPDF is the whole path of a sent fax: a PDF is rasterised
// by pdftoppm, transmitted by one engine service over G.711 or T.38, received
// by another and stored as a PDF with the page count that went out.
func TestFaxPipelineFromPDF(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	pdf, err := fax.EncodePDF(samplePages())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"g711", "t38"} {
		t.Run(mode, func(t *testing.T) {
			s1, ts1 := testServer(t, newEngine(), "")
			s2, ts2 := testServer(t, newEngine(), "")
			s1.pace, s2.pace = 2*time.Millisecond, 2*time.Millisecond
			cl1, cl2 := client(ts1, ""), client(ts2, "")
			ctx := context.Background()
			rp := newRelayPair(t)
			recv, err := cl2.Start(ctx, fax.SessionRequest{Direction: fax.SessionReceive, Mode: mode, Ident: "5550002", Remote: rp.b.LocalAddr().String()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			send, err := cl1.Start(ctx, fax.SessionRequest{Direction: fax.SessionSend, Mode: mode, Ident: "5550001", Remote: rp.a.LocalAddr().String()}, pdf)
			if err != nil {
				t.Fatal(err)
			}
			rp.run(send.Port, recv.Port)
			deadline := time.Now().Add(80 * time.Second)
			var si, ri fax.SessionInfo
			for time.Now().Before(deadline) {
				si, _ = cl1.Status(ctx, send.ID)
				ri, _ = cl2.Status(ctx, recv.ID)
				if si.Done() && ri.Done() {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !si.Success || !ri.Success || ri.Pages != 2 || !ri.HasPDF {
				t.Fatalf("send %+v, receive %+v", si, ri)
			}
			got, err := cl2.PDF(ctx, recv.ID)
			if err != nil {
				t.Fatal(err)
			}
			pages, err := fax.DecodePDF(got)
			if err != nil || len(pages) != 2 || pages[0].Width != fax.FaxPageWidth {
				t.Fatalf("received PDF: %d pages, %v", len(pages), err)
			}
			if bytes.Equal(pages[0].Data, make([]byte, len(pages[0].Data))) {
				t.Fatal("the received page is blank")
			}
		})
	}
}

// TestFaxStoppedMidTransferKeepsPages cancels a session after the first
// pages arrived: the pages that completed are kept (a partial fax), and the
// result says the call ended before the fax did.
func TestFaxStoppedMidTransferKeepsPages(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	many := make([]fax.Page, 0, 6)
	for i := 0; i < 6; i++ {
		many = append(many, samplePages()[i%2])
	}
	pdf, _ := fax.EncodePDF(many)
	s1, ts1 := testServer(t, newEngine(), "")
	s2, ts2 := testServer(t, newEngine(), "")
	s1.pace, s2.pace = 2*time.Millisecond, 2*time.Millisecond
	cl1, cl2 := client(ts1, ""), client(ts2, "")
	ctx := context.Background()
	rp := newRelayPair(t)
	recv, _ := cl2.Start(ctx, fax.SessionRequest{Direction: fax.SessionReceive, Mode: "t38", Remote: rp.b.LocalAddr().String()}, nil)
	send, _ := cl1.Start(ctx, fax.SessionRequest{Direction: fax.SessionSend, Mode: "t38", Remote: rp.a.LocalAddr().String()}, pdf)
	rp.run(send.Port, recv.Port)
	// Stop the receiver once it has at least one page's worth of time.
	time.Sleep(3 * time.Second)
	info, err := cl2.Stop(ctx, recv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Success {
		t.Skip("the fax finished before it could be stopped")
	}
	if info.Pages == 0 || !info.HasPDF {
		t.Fatalf("a stopped fax must keep the pages it had: %+v", info)
	}
	if pages, err := fax.DecodePDF(mustPDF(t, cl2, recv.ID)); err != nil || len(pages) != info.Pages {
		t.Fatalf("PDF holds %d pages (%v), the result says %d", len(pages), err, info.Pages)
	}
}

func mustPDF(t *testing.T, c *fax.Client, id string) []byte {
	t.Helper()
	b, err := c.PDF(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
