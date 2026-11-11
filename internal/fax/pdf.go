package fax

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

// EncodePDF writes pages as a PDF: one image XObject per page, 1 bit per
// pixel, Flate compressed (stdlib compress/zlib), no external dependency
// (decision D13). The document is deterministic for the same pages, so a
// golden file can pin it. It refuses more than MaxReceivePages pages or a
// result over MaxReceiveBytes.
func EncodePDF(pages []Page) ([]byte, error) {
	if len(pages) == 0 {
		return nil, ErrNoPages
	}
	if len(pages) > MaxReceivePages {
		return nil, fmt.Errorf("%w: %d pages, limit %d", ErrTooManyPages, len(pages), MaxReceivePages)
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	// Object numbers: 1 catalog, 2 page tree, then three per page.
	offsets := make([]int, 3+3*len(pages))
	obj := func(n int, body []byte) {
		offsets[n] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", n)
		out.Write(body)
		out.WriteString("\nendobj\n")
	}
	obj(1, []byte("<< /Type /Catalog /Pages 2 0 R >>"))
	var kids bytes.Buffer
	for i := range pages {
		fmt.Fprintf(&kids, "%d 0 R ", 3+3*i)
	}
	obj(2, fmt.Appendf(nil, "<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), bytes.TrimSpace(kids.Bytes())))
	for i, p := range pages {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		pageN, contN, imgN := 3+3*i, 4+3*i, 5+3*i
		wPt := float64(p.Width) * 72 / float64(p.XDPI)
		hPt := float64(p.Height) * 72 / float64(p.YDPI)
		obj(pageN, fmt.Appendf(nil,
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
			wPt, hPt, imgN, contN))
		content := fmt.Appendf(nil, "q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", wPt, hPt)
		obj(contN, streamObj("", content))
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		_, _ = zw.Write(p.Data)
		_ = zw.Close()
		// Decode [1 0]: sample 1 is black, as in Page.Data.
		dict := fmt.Sprintf(" /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 1 /Decode [1 0] /Filter /FlateDecode", p.Width, p.Height)
		obj(imgN, streamObj(dict, z.Bytes()))
		if out.Len() > MaxReceiveBytes {
			return nil, fmt.Errorf("%w: over %d bytes after %d pages", ErrTooLarge, MaxReceiveBytes, i+1)
		}
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	if out.Len() > MaxReceiveBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, out.Len(), MaxReceiveBytes)
	}
	return out.Bytes(), nil
}

func streamObj(dict string, data []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "<<%s /Length %d >>\nstream\n", dict, len(data))
	b.Write(data)
	b.WriteString("\nendstream")
	return b.Bytes()
}

var imageObj = regexp.MustCompile(`(?s)<<([^>]*?/Subtype /Image[^>]*?)/Length (\d+) >>\nstream\n`)

// DecodePDF reads back a PDF written by EncodePDF. It exists so tests and the
// receive pipeline can check a result; it does not read arbitrary PDFs.
func DecodePDF(pdf []byte) ([]Page, error) {
	var pages []Page
	for _, m := range imageObj.FindAllSubmatchIndex(pdf, -1) {
		dict := string(pdf[m[2]:m[3]])
		n, _ := strconv.Atoi(string(pdf[m[4]:m[5]]))
		start := m[1]
		if start+n > len(pdf) {
			return nil, fmt.Errorf("%w: truncated stream", ErrBadPage)
		}
		var p Page
		p.Width = intAfter(dict, "/Width ")
		p.Height = intAfter(dict, "/Height ")
		zr, err := zlib.NewReader(bytes.NewReader(pdf[start : start+n]))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBadPage, err)
		}
		p.Data, err = io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBadPage, err)
		}
		p.XDPI, p.YDPI = 1, 1 // not recorded in the image; the MediaBox carries the size
		if err := (Page{Width: p.Width, Height: p.Height, XDPI: 1, YDPI: 1, Data: p.Data}).Validate(); err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	if len(pages) == 0 {
		return nil, ErrNoPages
	}
	return pages, nil
}

func intAfter(s, key string) int {
	i := bytes.Index([]byte(s), []byte(key))
	if i < 0 {
		return 0
	}
	j := i + len(key)
	k := j
	for k < len(s) && s[k] >= '0' && s[k] <= '9' {
		k++
	}
	n, _ := strconv.Atoi(s[j:k])
	return n
}
