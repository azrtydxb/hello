package fax

import (
	"errors"
	"fmt"
)

// UDPTL is the transport of T.38 over UDP (ITU-T T.38 Annex A): a sequence
// number, the primary IFP packet and, with the redundancy error recovery
// scheme, copies of the packets just before it. hello-sip relays these
// datagrams untouched; hello-fax is the endpoint that reads and writes them.
type UDPTL struct {
	Seq     uint16
	Primary []byte
	// Secondary holds the redundant IFP packets: Secondary[i] is the IFP
	// packet of sequence number Seq-1-i.
	Secondary [][]byte
}

// ErrUDPTL is wrapped by every UDPTL parse error.
var ErrUDPTL = errors.New("fax: bad UDPTL packet")

// MaxUDPTLLen bounds a length determinant (aligned PER, up to 16383).
const MaxUDPTLLen = 16383

func appendLen(b []byte, n int) []byte {
	if n < 128 {
		return append(b, byte(n)) //nolint:gosec // G115: n < 128
	}
	return append(b, 0x80|byte(n>>8), byte(n)) //nolint:gosec // G115: n <= 16383 is checked by the callers
}

func readLen(b []byte) (n, used int, err error) {
	if len(b) == 0 {
		return 0, 0, fmt.Errorf("%w: truncated length", ErrUDPTL)
	}
	if b[0]&0x80 == 0 {
		return int(b[0]), 1, nil
	}
	if b[0]&0x40 != 0 || len(b) < 2 {
		return 0, 0, fmt.Errorf("%w: unsupported length", ErrUDPTL)
	}
	return int(b[0]&0x3f)<<8 | int(b[1]), 2, nil
}

// Marshal encodes the packet with the redundancy scheme: a zero octet, the
// count and the secondary packets (omitted when there are none).
func (u UDPTL) Marshal() ([]byte, error) {
	if len(u.Primary) > MaxUDPTLLen {
		return nil, fmt.Errorf("%w: primary of %d bytes", ErrUDPTL, len(u.Primary))
	}
	b := []byte{byte(u.Seq >> 8), byte(u.Seq)} //nolint:gosec // G115: the two halves of a 16-bit number
	b = appendLen(b, len(u.Primary))
	b = append(b, u.Primary...)
	b = append(b, 0x00)
	b = appendLen(b, len(u.Secondary))
	for _, s := range u.Secondary {
		if len(s) > MaxUDPTLLen {
			return nil, fmt.Errorf("%w: secondary of %d bytes", ErrUDPTL, len(s))
		}
		b = appendLen(b, len(s))
		b = append(b, s...)
	}
	return b, nil
}

// ParseUDPTL decodes one datagram. A forward error correction section is
// accepted and ignored (the SDP offers redundancy only).
func ParseUDPTL(b []byte) (UDPTL, error) {
	var u UDPTL
	if len(b) < 3 {
		return u, fmt.Errorf("%w: %d bytes", ErrUDPTL, len(b))
	}
	u.Seq = uint16(b[0])<<8 | uint16(b[1])
	n, used, err := readLen(b[2:])
	if err != nil {
		return u, err
	}
	p := 2 + used
	if p+n > len(b) {
		return u, fmt.Errorf("%w: primary overruns the datagram", ErrUDPTL)
	}
	u.Primary = append([]byte(nil), b[p:p+n]...)
	p += n
	if p >= len(b) || b[p]&0x80 != 0 { // none, or forward error correction
		return u, nil
	}
	p++ // the error recovery choice octet
	count, used, err := readLen(b[p:])
	if err != nil {
		return u, err
	}
	p += used
	if count > 16 {
		return u, fmt.Errorf("%w: %d secondary packets", ErrUDPTL, count)
	}
	for i := 0; i < count; i++ {
		n, used, err := readLen(b[p:])
		if err != nil {
			return u, err
		}
		p += used
		if p+n > len(b) {
			return u, fmt.Errorf("%w: secondary overruns the datagram", ErrUDPTL)
		}
		u.Secondary = append(u.Secondary, append([]byte(nil), b[p:p+n]...))
		p += n
	}
	return u, nil
}

// Recovered returns the IFP packets this datagram carries that follow last
// (the highest sequence number already delivered), oldest first, with their
// sequence numbers. A gap wider than the redundancy leaves a hole; the T.30
// engine sees the missing sequence numbers and decides what to do.
func (u UDPTL) Recovered(last uint16, haveLast bool) (seqs []uint16, ifps [][]byte) {
	if !haveLast {
		return []uint16{u.Seq}, [][]byte{u.Primary}
	}
	gap := int(uint16(u.Seq - last)) // 1 means in order
	if gap == 0 || gap > 0x8000 {
		return nil, nil // duplicate or late
	}
	for i := len(u.Secondary) - 1; i >= 0; i-- {
		// Secondary[i] has sequence number Seq-1-i.
		if back := i + 1; back < gap {
			seqs = append(seqs, u.Seq-uint16(back)) //nolint:gosec // G115: back < gap <= 0x8000
			ifps = append(ifps, u.Secondary[i])
		}
	}
	return append(seqs, u.Seq), append(ifps, u.Primary)
}
