package fax

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// testPage draws a recognisable pattern: a diagonal and a frame.
func testPage(w, h int) Page {
	p := NewPage(w, h, FineXDPI, FineYDPI)
	for i := 0; i < h && i < w; i++ {
		p.Set(i, i)
	}
	for x := 0; x < w; x++ {
		p.Set(x, 0)
		p.Set(x, h-1)
	}
	return p
}

func TestPDFRoundTrip(t *testing.T) {
	pages := []Page{testPage(1728, 200), testPage(1728, 300), testPage(100, 50)}
	pdf, err := EncodePDF(pages)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatalf("not a PDF: %q ... %q", pdf[:8], pdf[len(pdf)-8:])
	}
	if !bytes.Contains(pdf, []byte("/Count 3")) {
		t.Fatal("page count missing from the page tree")
	}
	back, err := DecodePDF(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 3 {
		t.Fatalf("decoded %d pages", len(back))
	}
	for i := range pages {
		if back[i].Width != pages[i].Width || back[i].Height != pages[i].Height || !bytes.Equal(back[i].Data, pages[i].Data) {
			t.Fatalf("page %d changed in the round trip", i+1)
		}
	}
	again, _ := EncodePDF(pages)
	if !bytes.Equal(pdf, again) {
		t.Fatal("EncodePDF is not deterministic")
	}
}

func TestPDFLimits(t *testing.T) {
	if _, err := EncodePDF(nil); !errors.Is(err, ErrNoPages) {
		t.Fatalf("no pages: %v", err)
	}
	many := make([]Page, MaxReceivePages+1)
	for i := range many {
		many[i] = NewPage(8, 8, FineXDPI, FineYDPI)
	}
	if _, err := EncodePDF(many); !errors.Is(err, ErrTooManyPages) {
		t.Fatalf("51 pages: %v", err)
	}
	if _, err := EncodePDF(many[:MaxReceivePages]); err != nil {
		t.Fatalf("50 pages: %v", err)
	}
	if _, err := EncodePDF([]Page{{Width: 8, Height: 8, XDPI: 1, YDPI: 1, Data: []byte{1}}}); !errors.Is(err, ErrBadPage) {
		t.Fatalf("bad data length: %v", err)
	}
}

func TestPDFSizeLimit(t *testing.T) {
	// Incompressible pages reach the byte cap before the page cap.
	p := NewPage(2048, 4096, FineXDPI, FineYDPI)
	x := uint32(1)
	for i := range p.Data {
		x = x*1664525 + 1013904223
		p.Data[i] = byte(x >> 24)
	}
	pages := make([]Page, 40)
	for i := range pages {
		pages[i] = p
	}
	if _, err := EncodePDF(pages); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestTIFFRoundTrip(t *testing.T) {
	pages := []Page{testPage(1728, 100), testPage(1728, 120)}
	raw, err := WriteTIFF(pages)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ReadTIFF(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 {
		t.Fatalf("%d pages", len(back))
	}
	for i := range pages {
		if back[i].Width != 1728 || back[i].XDPI != FineXDPI || back[i].YDPI != FineYDPI || !bytes.Equal(back[i].Data, pages[i].Data) {
			t.Fatalf("page %d differs", i+1)
		}
	}
	for _, bad := range [][]byte{nil, []byte("II*\x00\xff\xff\xff\xff"), raw[:len(raw)/2], {'X', 'X', 42, 0, 8, 0, 0, 0}} {
		if _, err := ReadTIFF(bad); err == nil {
			t.Fatalf("accepted a corrupt TIFF (%d bytes)", len(bad))
		}
	}
}

func TestFitToFaxWidth(t *testing.T) {
	narrow := testPage(1687, 20) // A4 at 204 dpi
	got, err := FitToFaxWidth(narrow)
	if err != nil || got.Width != FaxPageWidth {
		t.Fatalf("narrow: %v %d", err, got.Width)
	}
	shift := (FaxPageWidth - 1687) / 2
	if !got.Black(shift+3, 3) || got.Black(shift+4, 3) {
		t.Fatal("content not centred")
	}
	if _, err := FitToFaxWidth(testPage(1734, 20)); err != nil {
		t.Fatalf("letter: %v", err)
	}
	if _, err := FitToFaxWidth(testPage(2400, 20)); !errors.Is(err, ErrRaster) {
		t.Fatalf("too wide: %v", err)
	}
}

func TestParsePBM(t *testing.T) {
	raw := append([]byte("P4\n# c\n10 2\n"), 0xFF, 0xC0, 0x00, 0x40)
	p, err := ParsePBM(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Width != 10 || p.Height != 2 || !p.Black(9, 0) || p.Black(0, 1) || !p.Black(9, 1) {
		t.Fatalf("%+v", p)
	}
	for _, bad := range [][]byte{nil, []byte("P1\n1 1\n0"), []byte("P4\n8 8\n\x00"), []byte("P4\n-1 5\n")} {
		if _, err := ParsePBM(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestRasteriseRejects(t *testing.T) {
	r := Rasteriser{}
	if _, err := r.Rasterise(context.Background(), []byte("hello")); !errors.Is(err, ErrRaster) {
		t.Fatalf("not a PDF: %v", err)
	}
	if _, err := r.Rasterise(context.Background(), append([]byte("%PDF-1.4\n"), make([]byte, MaxSendBytes)...)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
}

// TestRasterisePDF feeds the pages of a generated PDF through the real
// pdftoppm; it skips where poppler is not installed.
func TestRasterisePDF(t *testing.T) {
	bin, err := exec.LookPath("pdftoppm")
	if err != nil {
		t.Skip("pdftoppm not installed")
	}
	src := []Page{testPage(1728, 400), testPage(1728, 400)}
	pdf, err := EncodePDF(src)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := Rasteriser{Path: bin}.Rasterise(context.Background(), pdf)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("%d pages, want 2", len(pages))
	}
	for i, p := range pages {
		if p.Width != FaxPageWidth || p.XDPI != FineXDPI || p.YDPI != FineYDPI || p.Validate() != nil {
			t.Fatalf("page %d: %dx%d", i+1, p.Width, p.Height)
		}
		ink := 0
		for _, b := range p.Data {
			for ; b != 0; b &= b - 1 {
				ink++
			}
		}
		if ink == 0 {
			t.Fatalf("page %d is blank after rasterisation", i+1)
		}
	}
}

func TestUDPTLRoundTrip(t *testing.T) {
	big := bytes.Repeat([]byte{7}, 300)
	in := UDPTL{Seq: 0xFFFE, Primary: []byte{1, 2, 3}, Secondary: [][]byte{{9, 9}, big}}
	raw, err := in.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseUDPTL(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Seq != in.Seq || !bytes.Equal(out.Primary, in.Primary) || len(out.Secondary) != 2 || !bytes.Equal(out.Secondary[1], big) {
		t.Fatalf("round trip: %+v", out)
	}
	// A datagram with no error recovery section, and with an FEC marker.
	bare, _ := ParseUDPTL([]byte{0, 5, 2, 0xAA, 0xBB})
	if bare.Seq != 5 || len(bare.Primary) != 2 || bare.Secondary != nil {
		t.Fatalf("bare: %+v", bare)
	}
	if fec, err := ParseUDPTL([]byte{0, 5, 1, 0xAA, 0x80, 1}); err != nil || len(fec.Secondary) != 0 {
		t.Fatalf("fec: %+v %v", fec, err)
	}
	for _, bad := range [][]byte{nil, {0, 1}, {0, 1, 9, 1}, {0, 1, 1, 1, 0, 3, 5}, {0, 1, 1, 1, 0, 99}} {
		if _, err := ParseUDPTL(bad); err == nil {
			t.Fatalf("accepted % x", bad)
		}
	}
}

func TestUDPTLRecovered(t *testing.T) {
	// Packet 10 carries 9 and 8 as redundancy; the receiver last saw 7.
	u := UDPTL{Seq: 10, Primary: []byte{10}, Secondary: [][]byte{{9}, {8}}}
	seqs, ifps := u.Recovered(7, true)
	if len(seqs) != 3 || seqs[0] != 8 || seqs[1] != 9 || seqs[2] != 10 || ifps[0][0] != 8 || ifps[2][0] != 10 {
		t.Fatalf("recover: %v %v", seqs, ifps)
	}
	if s, _ := u.Recovered(9, true); len(s) != 1 || s[0] != 10 {
		t.Fatalf("in order: %v", s)
	}
	if s, _ := u.Recovered(10, true); len(s) != 0 {
		t.Fatalf("duplicate delivered: %v", s)
	}
	if s, _ := u.Recovered(12, true); len(s) != 0 {
		t.Fatalf("late packet delivered: %v", s)
	}
	if s, _ := u.Recovered(2, true); len(s) != 3 || s[0] != 8 {
		t.Fatalf("wide gap: %v", s) // holes older than the redundancy stay holes
	}
	u.Seq = 1 // wraps: last was 65535
	if s, _ := u.Recovered(0xFFFF, true); len(s) != 2 || s[0] != 0 || s[1] != 1 {
		t.Fatalf("wrap: %v", s)
	}
}

var update = flag.Bool("update", false, "rewrite the golden files")

// goldenPage is a small page with a frame and a diagonal, in the shape a
// receiver writes: 1-bit rows, black as 1.
func goldenPages() []Page {
	a := NewPage(64, 32, FineXDPI, FineYDPI)
	for x := 0; x < 64; x++ {
		a.Set(x, 0)
		a.Set(x, 31)
	}
	for y := 0; y < 32; y++ {
		a.Set(0, y)
		a.Set(63, y)
		a.Set(y, y)
	}
	b := NewPage(64, 16, FineXDPI, FineYDPI)
	for i := 0; i < 16; i++ {
		b.Set(i*4, i)
	}
	return []Page{a, b}
}

// TestPDFGolden pins the bytes of a received fax's PDF: a change of the
// encoder shows up as a diff of testdata/received.pdf (go test -update).
func TestPDFGolden(t *testing.T) {
	pdf, err := EncodePDF(goldenPages())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "received.pdf")
	if *update {
		if err := os.WriteFile(path, pdf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pdf, want) {
		t.Fatalf("the PDF changed (%d bytes, golden %d); run go test ./internal/fax -update and review the diff", len(pdf), len(want))
	}
	// The golden file is a PDF any viewer reads: poppler rasterises it to the
	// pages that went in.
	if bin, err := exec.LookPath("pdftoppm"); err == nil {
		pages, err := Rasteriser{Path: bin}.Rasterise(context.Background(), want)
		if err != nil || len(pages) != 2 {
			t.Fatalf("poppler read %d pages (%v), want 2", len(pages), err)
		}
	}
}
