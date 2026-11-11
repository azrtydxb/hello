// Package fax holds the parts of Hello's fax feature that are plain Go: the
// bilevel page type, the PDF encoder for received pages, the rasteriser for
// outbound PDFs (pdftoppm, argv only), the TIFF files the T.30 engine reads
// and writes, and the HTTP protocol hello-sip speaks to hello-fax. The T.30
// engine itself (spandsp through cgo) lives in cmd/hello-fax so the SIP core
// stays pure Go (decision D13).
package fax

import (
	"errors"
	"fmt"
)

// Limits (pbx-gaps S-15).
const (
	// MaxReceivePages and MaxReceiveBytes bound one received fax.
	MaxReceivePages = 50
	MaxReceiveBytes = 20 << 20
	// MaxSendPages and MaxSendBytes bound one uploaded PDF.
	MaxSendPages = 50
	MaxSendBytes = 10 << 20
	// FineXDPI and FineYDPI are the resolution outbound pages are rasterised at
	// (T.30 "fine": 204 by 196 dpi).
	FineXDPI = 204
	FineYDPI = 196
	// MaxPageWidth and MaxPageHeight bound a page's pixels (A4 at fine
	// resolution is 1728 by 2156; the extra room allows B4 and long pages).
	MaxPageWidth  = 2432
	MaxPageHeight = 8192
)

// Errors returned by the page and PDF helpers.
var (
	ErrTooManyPages = errors.New("fax: too many pages")
	ErrTooLarge     = errors.New("fax: document too large")
	ErrBadPage      = errors.New("fax: bad page")
	ErrNoPages      = errors.New("fax: no pages")
)

// Page is one bilevel page. Data holds Height rows of (Width+7)/8 bytes, most
// significant bit first, with bit 1 meaning black ink. XDPI and YDPI are the
// horizontal and vertical resolution.
type Page struct {
	Width, Height int
	XDPI, YDPI    int
	Data          []byte
}

// RowBytes is the packed length of one row.
func (p Page) RowBytes() int { return (p.Width + 7) / 8 }

// Validate checks the geometry against the data.
func (p Page) Validate() error {
	if p.Width <= 0 || p.Height <= 0 || p.Width > MaxPageWidth || p.Height > MaxPageHeight {
		return fmt.Errorf("%w: %dx%d pixels", ErrBadPage, p.Width, p.Height)
	}
	if p.XDPI <= 0 || p.YDPI <= 0 {
		return fmt.Errorf("%w: resolution %dx%d", ErrBadPage, p.XDPI, p.YDPI)
	}
	if len(p.Data) != p.RowBytes()*p.Height {
		return fmt.Errorf("%w: %d data bytes for %dx%d", ErrBadPage, len(p.Data), p.Width, p.Height)
	}
	return nil
}

// Black reports whether the pixel at (x, y) is black.
func (p Page) Black(x, y int) bool {
	return p.Data[y*p.RowBytes()+x/8]&(0x80>>uint(x%8)) != 0
}

// NewPage returns a blank page.
func NewPage(width, height, xdpi, ydpi int) Page {
	return Page{Width: width, Height: height, XDPI: xdpi, YDPI: ydpi, Data: make([]byte, (width+7)/8*height)}
}

// Set blackens the pixel at (x, y).
func (p Page) Set(x, y int) {
	p.Data[y*p.RowBytes()+x/8] |= 0x80 >> uint(x%8)
}
