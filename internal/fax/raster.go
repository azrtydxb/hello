package fax

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FaxPageWidth is the width in pixels of an A4 page on the T.30 wire (T.4).
const FaxPageWidth = 1728

// ErrRaster is wrapped by every rasterisation failure that is the document's
// fault (not a PDF, no pages, a page too wide), so the API can answer 422.
var ErrRaster = errors.New("fax: cannot rasterise the PDF")

// Rasteriser turns an uploaded PDF into fax pages with pdftoppm. The binary
// is run with a fixed argv, no shell, in a private temporary directory that is
// removed afterwards (decision D13). The PDF bytes never reach an argument.
type Rasteriser struct {
	// Path is the pdftoppm binary (HELLO_FAX_PDFTOPPM); empty means "pdftoppm".
	Path string
	// Timeout bounds one run; zero means 60 seconds.
	Timeout time.Duration
}

// Rasterise converts the first MaxSendPages pages of pdf to 204 by 196 dpi
// bilevel pages, 1728 pixels wide.
func (r Rasteriser) Rasterise(ctx context.Context, pdf []byte) ([]Page, error) {
	if len(pdf) == 0 || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, fmt.Errorf("%w: not a PDF", ErrRaster)
	}
	if len(pdf) > MaxSendBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(pdf), MaxSendBytes)
	}
	dir, err := os.MkdirTemp("", "hello-fax-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a directory needs the execute bit; it is private to us
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "in.pdf"), pdf, 0o600); err != nil { //nolint:gosec // G703: a fixed name inside the private directory MkdirTemp made
		return nil, err
	}
	bin := r.Path
	if bin == "" {
		bin = "pdftoppm"
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// One page more than the limit, so an over-long document is seen as such.
	cmd := exec.CommandContext(ctx, bin, "-mono", //nolint:gosec // G204: the binary is operator configuration and the argv is fixed; the PDF is a file, never an argument
		"-rx", strconv.Itoa(FineXDPI), "-ry", strconv.Itoa(FineYDPI),
		"-f", "1", "-l", strconv.Itoa(MaxSendPages+1), "in.pdf", "page")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", "HOME=" + dir, "LANG=C"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("pdftoppm: %w", ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("%w: %s", ErrRaster, firstLine(stderr.String()))
		}
		return nil, fmt.Errorf("run pdftoppm: %w", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "page-*.pbm"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files) // pdftoppm zero-pads the page number to a fixed width
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: no pages", ErrRaster)
	}
	if len(files) > MaxSendPages {
		return nil, fmt.Errorf("%w: more than %d pages", ErrTooManyPages, MaxSendPages)
	}
	pages := make([]Page, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // G304: a file pdftoppm wrote in our private directory
		if err != nil {
			return nil, err
		}
		p, err := ParsePBM(raw)
		if err != nil {
			return nil, err
		}
		p.XDPI, p.YDPI = FineXDPI, FineYDPI
		if p, err = FitToFaxWidth(p); err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	return pages, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" {
		return "pdftoppm failed"
	}
	return s
}

// ParsePBM reads a binary PBM (P4) image. In PBM a 1 bit is black, as in Page.
func ParsePBM(raw []byte) (Page, error) {
	fields := make([]string, 0, 3)
	i := 0
	isSpace := func(b byte) bool { return b == ' ' || b == '\n' || b == '\t' || b == '\r' }
	for len(fields) < 3 && i < len(raw) {
		for i < len(raw) && isSpace(raw[i]) {
			i++
		}
		if i < len(raw) && raw[i] == '#' {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			continue
		}
		j := i
		for j < len(raw) && !isSpace(raw[j]) {
			j++
		}
		if j == i {
			break
		}
		fields = append(fields, string(raw[i:j]))
		i = j
	}
	if len(fields) < 3 || fields[0] != "P4" {
		return Page{}, fmt.Errorf("%w: not a P4 bitmap", ErrRaster)
	}
	w, err1 := strconv.Atoi(fields[1])
	h, err2 := strconv.Atoi(fields[2])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 || w > 4*MaxPageWidth || h > MaxPageHeight {
		return Page{}, fmt.Errorf("%w: page size %sx%s", ErrRaster, fields[1], fields[2])
	}
	i++ // the single whitespace byte after the height
	rb := (w + 7) / 8
	if len(raw)-i < rb*h {
		return Page{}, fmt.Errorf("%w: short bitmap", ErrRaster)
	}
	return Page{Width: w, Height: h, Data: append([]byte(nil), raw[i:i+rb*h]...)}, nil
}

// FitToFaxWidth makes a page 1728 pixels wide: a narrower page is centred on
// white, a page up to 16 pixels too wide (US Letter) loses its edge columns.
func FitToFaxWidth(p Page) (Page, error) {
	if p.Width == FaxPageWidth {
		return p, nil
	}
	if p.Width > FaxPageWidth+16 {
		return Page{}, fmt.Errorf("%w: page is %d pixels wide, a fax page is %d", ErrRaster, p.Width, FaxPageWidth)
	}
	out := NewPage(FaxPageWidth, p.Height, p.XDPI, p.YDPI)
	shift := (FaxPageWidth - p.Width) / 2 // negative crops
	for y := 0; y < p.Height; y++ {
		for x := 0; x < p.Width; x++ {
			if nx := x + shift; nx >= 0 && nx < FaxPageWidth && p.Black(x, y) {
				out.Set(nx, y)
			}
		}
	}
	return out, nil
}
