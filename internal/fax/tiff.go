package fax

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/image/ccitt"
)

// The T.30 engine exchanges pages with the Go side as TIFF-F files: the
// engine reads the pages to send from one and writes the pages it receives
// into one. Bilevel, uncompressed, little-endian: the one shape both sides
// agree on, so neither needs a CCITT codec of its own.

const (
	tagNewSubfile    = 254
	tagWidth         = 256
	tagLength        = 257
	tagBits          = 258
	tagCompression   = 259
	tagPhotometric   = 262
	tagFillOrder     = 266
	tagStripOffsets  = 273
	tagSamples       = 277
	tagRowsPerStrip  = 278
	tagStripCounts   = 279
	tagXRes          = 282
	tagYRes          = 283
	tagResUnit       = 296
	typeShort        = 3
	typeLong         = 4
	typeRational     = 5
	compressionNone  = 1
	compressionT6    = 4
	photometricWhite = 0
)

// ErrTIFF is wrapped by every TIFF read error.
var ErrTIFF = errors.New("fax: bad TIFF")

// WriteTIFF encodes pages as a multi-page, uncompressed bilevel TIFF.
func WriteTIFF(pages []Page) ([]byte, error) {
	if len(pages) == 0 {
		return nil, ErrNoPages
	}
	var b bytes.Buffer
	le := binary.LittleEndian
	b.Write([]byte{'I', 'I', 42, 0, 0, 0, 0, 0})
	type entry struct {
		tag, typ uint16
		val      uint32
	}
	var ifdOffsets []int
	for i, p := range pages {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		dataOff := b.Len()
		b.Write(p.Data)
		if b.Len()%2 == 1 {
			b.WriteByte(0)
		}
		resOff := b.Len()
		for _, d := range []int{p.XDPI, p.YDPI} {
			_ = binary.Write(&b, le, uint32(d)) //nolint:gosec // G115: a resolution is validated positive and small
			_ = binary.Write(&b, le, uint32(1))
		}
		es := []entry{
			{tagNewSubfile, typeLong, 2},
			{tagWidth, typeLong, uint32(p.Width)},   //nolint:gosec // G115: Validate bounds the page size
			{tagLength, typeLong, uint32(p.Height)}, //nolint:gosec // G115: Validate bounds the page size
			{tagBits, typeShort, 1},
			{tagCompression, typeShort, compressionNone},
			{tagPhotometric, typeShort, photometricWhite},
			{tagFillOrder, typeShort, 1},
			{tagStripOffsets, typeLong, uint32(dataOff)}, //nolint:gosec // G115: a TIFF of at most 50 pages is far below 4 GiB
			{tagSamples, typeShort, 1},
			{tagRowsPerStrip, typeLong, uint32(p.Height)},   //nolint:gosec // G115: Validate bounds the page size
			{tagStripCounts, typeLong, uint32(len(p.Data))}, //nolint:gosec // G115: bounded by Validate
			{tagXRes, typeRational, uint32(resOff)},         //nolint:gosec // G115: see the offsets above
			{tagYRes, typeRational, uint32(resOff + 8)},     //nolint:gosec // G115: see the offsets above
			{tagResUnit, typeShort, 2},
		}
		ifdOffsets = append(ifdOffsets, b.Len())
		_ = binary.Write(&b, le, uint16(len(es))) //nolint:gosec // G115: fourteen entries
		for _, e := range es {
			_ = binary.Write(&b, le, e.tag)
			_ = binary.Write(&b, le, e.typ)
			_ = binary.Write(&b, le, uint32(1))
			_ = binary.Write(&b, le, e.val) // a SHORT value sits in the low half, little-endian
		}
		_ = binary.Write(&b, le, uint32(0)) // next IFD, patched below
	}
	out := b.Bytes()
	le.PutUint32(out[4:], uint32(ifdOffsets[0])) //nolint:gosec // G115: offsets in a file of at most 20 MB
	for i := 0; i+1 < len(ifdOffsets); i++ {
		next := ifdOffsets[i] + 2 + 14*12
		le.PutUint32(out[next:], uint32(ifdOffsets[i+1])) //nolint:gosec // G115: offsets in a file of at most 20 MB
	}
	return out, nil
}

// ReadTIFF decodes an uncompressed (or packed, one-strip-per-page) bilevel
// TIFF into pages, normalised to bit 1 = black. It accepts what WriteTIFF
// writes and what the engine writes back with compression off.
func ReadTIFF(data []byte) ([]Page, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("%w: short file", ErrTIFF)
	}
	var bo binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, fmt.Errorf("%w: byte order", ErrTIFF)
	}
	if bo.Uint16(data[2:]) != 42 {
		return nil, fmt.Errorf("%w: magic", ErrTIFF)
	}
	var pages []Page
	off := int(bo.Uint32(data[4:]))
	for off != 0 {
		if len(pages) >= MaxReceivePages {
			return nil, fmt.Errorf("%w: %d pages", ErrTooManyPages, len(pages))
		}
		if off < 8 || off+2 > len(data) {
			return nil, fmt.Errorf("%w: IFD offset", ErrTIFF)
		}
		n := int(bo.Uint16(data[off:]))
		if off+2+12*n+4 > len(data) {
			return nil, fmt.Errorf("%w: IFD length", ErrTIFF)
		}
		tags := map[uint16][]uint32{}
		for i := 0; i < n; i++ {
			e := data[off+2+12*i:]
			tag, typ, cnt := bo.Uint16(e), bo.Uint16(e[2:]), int(bo.Uint32(e[4:]))
			vals, err := tiffValues(data, bo, typ, cnt, e[8:12])
			if err != nil {
				return nil, err
			}
			tags[tag] = vals
		}
		first := func(t uint16, def uint32) uint32 {
			if v := tags[t]; len(v) > 0 {
				return v[0]
			}
			return def
		}
		p := Page{Width: int(first(tagWidth, 0)), Height: int(first(tagLength, 0))}
		comp := first(tagCompression, compressionNone)
		if comp != compressionNone && comp != compressionT6 {
			return nil, fmt.Errorf("%w: compression %d", ErrTIFF, comp)
		}
		if first(tagBits, 1) != 1 || first(tagSamples, 1) != 1 {
			return nil, fmt.Errorf("%w: not bilevel", ErrTIFF)
		}
		fill := first(tagFillOrder, 1)
		if fill != 1 && fill != 2 || (comp == compressionNone && fill != 1) {
			return nil, fmt.Errorf("%w: fill order %d", ErrTIFF, fill)
		}
		p.XDPI, p.YDPI = resolution(data, bo, e12(data, bo, off, n, tagXRes), first(tagResUnit, 2)), resolution(data, bo, e12(data, bo, off, n, tagYRes), first(tagResUnit, 2))
		offs, counts := tags[tagStripOffsets], tags[tagStripCounts]
		if len(offs) == 0 || len(offs) != len(counts) {
			return nil, fmt.Errorf("%w: strips", ErrTIFF)
		}
		if p.Width <= 0 || p.Width > MaxPageWidth || p.Height <= 0 || p.Height > MaxPageHeight {
			return nil, fmt.Errorf("%w: page size %dx%d", ErrBadPage, p.Width, p.Height)
		}
		rps := int(first(tagRowsPerStrip, uint32(p.Height)))
		if rps <= 0 || rps > p.Height {
			rps = p.Height
		}
		for i := range offs {
			s, c := int(offs[i]), int(counts[i])
			if s < 0 || c < 0 || s+c > len(data) || len(p.Data)+c > MaxReceiveBytes {
				return nil, fmt.Errorf("%w: strip bounds", ErrTIFF)
			}
			if comp == compressionNone {
				p.Data = append(p.Data, data[s:s+c]...)
				continue
			}
			rows := min(rps, p.Height-len(p.Data)/p.RowBytes())
			order := ccitt.MSB
			if fill == 2 {
				order = ccitt.LSB
			}
			// Invert: the decoder's black is 1, as in Page.Data; the
			// photometric inversion below is then applied to a white-is-0 page.
			dec, err := io.ReadAll(ccitt.NewReader(bytes.NewReader(data[s:s+c]), order, ccitt.Group4, p.Width, rows, &ccitt.Options{Invert: true}))
			if err != nil && len(dec) != p.RowBytes()*rows {
				return nil, fmt.Errorf("%w: T.6 strip: %w", ErrTIFF, err)
			}
			p.Data = append(p.Data, dec...)
		}
		if first(tagPhotometric, photometricWhite) == 1 && comp == compressionNone { // min-is-black: invert
			for i := range p.Data {
				p.Data[i] = ^p.Data[i]
			}
		}
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("page %d: %w", len(pages)+1, err)
		}
		pages = append(pages, p)
		off = int(bo.Uint32(data[off+2+12*n:]))
	}
	if len(pages) == 0 {
		return nil, ErrNoPages
	}
	return pages, nil
}

// e12 returns the 4 value bytes of tag t in the IFD at off, or nil.
func e12(data []byte, bo binary.ByteOrder, off, n int, t uint16) []byte {
	for i := 0; i < n; i++ {
		e := data[off+2+12*i:]
		if bo.Uint16(e) == t {
			return e[8:12]
		}
	}
	return nil
}

// resolution reads a RATIONAL resolution; a missing one reads as 0 and fails
// page validation. cm resolutions are converted to inches.
func resolution(data []byte, bo binary.ByteOrder, ptr []byte, unit uint32) int {
	if ptr == nil {
		return 0
	}
	o := int(bo.Uint32(ptr))
	if o < 0 || o+8 > len(data) {
		return 0
	}
	num, den := float64(bo.Uint32(data[o:])), float64(bo.Uint32(data[o+4:]))
	if den == 0 {
		return 0
	}
	v := num / den
	if unit == 3 {
		v *= 2.54
	}
	return int(v + 0.5)
}

func tiffValues(data []byte, bo binary.ByteOrder, typ uint16, cnt int, inline []byte) ([]uint32, error) {
	size := map[uint16]int{typeShort: 2, typeLong: 4, typeRational: 8}[typ]
	if size == 0 {
		return nil, nil // a type this reader does not use
	}
	if cnt < 0 || cnt > 1<<20 {
		return nil, fmt.Errorf("%w: value count", ErrTIFF)
	}
	src := inline
	if size*cnt > 4 {
		o := int(bo.Uint32(inline))
		if o < 0 || o+size*cnt > len(data) {
			return nil, fmt.Errorf("%w: value offset", ErrTIFF)
		}
		src = data[o:]
	}
	out := make([]uint32, 0, cnt)
	for i := 0; i < cnt; i++ {
		switch typ {
		case typeShort:
			out = append(out, uint32(bo.Uint16(src[2*i:])))
		case typeLong:
			out = append(out, bo.Uint32(src[4*i:]))
		case typeRational:
			out = append(out, bo.Uint32(src[8*i:]))
		}
	}
	return out, nil
}
