package media

import "encoding/binary"

// The minimal fixed RFC 3550 header: version, payload type and marker in
// the first two bytes, then sequence, timestamp and SSRC. CSRC and header
// extension lengths are parsed only to find the payload.
const (
	rtpVersion   = 2
	rtpHeaderLen = 12
	rtpMaxPacket = 2048 // larger datagrams are not RTP we care about
)

// RTPPacket is one parsed RTP packet. Payload aliases the caller's buffer,
// so it is only valid until that buffer is reused.
type RTPPacket struct {
	PayloadType uint8
	Marker      bool
	Sequence    uint16
	Timestamp   uint32
	SSRC        uint32
	Payload     []byte
}

// BuildRTP builds a minimal RFC 3550 packet: version 2, no padding, no
// extension, no CSRCs, with the marker in bit 7 of the second byte.
func BuildRTP(payloadType uint8, sequence uint16, timestamp uint32, ssrc uint32, marker bool, payload []byte) []byte {
	out := make([]byte, rtpHeaderLen+len(payload))
	be := binary.BigEndian
	be.PutUint16(out[2:], sequence)
	be.PutUint32(out[4:], timestamp)
	be.PutUint32(out[8:], ssrc)
	out[0] = rtpVersion << 6
	out[1] = payloadType
	if marker {
		out[1] |= 0x80
	}
	copy(out[rtpHeaderLen:], payload)
	return out
}

// ParseRTP parses one RTP datagram. It reports ok=false for anything it
// cannot trust instead of returning an error: RTP arrives unsolicited over
// UDP, so a malformed datagram is a dropped packet, not a condition worth
// reporting. The version must be 2 and the declared CSRC and extension
// lengths must fit the datagram; the payload then covers the rest.
func ParseRTP(data []byte) (*RTPPacket, bool) {
	if len(data) < rtpHeaderLen {
		return nil, false
	}
	if data[0]>>6 != rtpVersion {
		return nil, false
	}
	hdr := rtpHeaderLen + 4*int(data[0]&0x0F)
	if len(data) < hdr {
		return nil, false
	}
	if data[0]&0x10 != 0 { // extension bit
		if len(data) < hdr+4 {
			return nil, false
		}
		hdr += 4 + 4*int(binary.BigEndian.Uint16(data[hdr+2:hdr+4]))
		if len(data) < hdr {
			return nil, false
		}
	}
	return &RTPPacket{
		PayloadType: data[1] & 0x7F,
		Marker:      data[1]&0x80 != 0,
		Sequence:    binary.BigEndian.Uint16(data[2:]),
		Timestamp:   binary.BigEndian.Uint32(data[4:]),
		SSRC:        binary.BigEndian.Uint32(data[8:]),
		Payload:     data[hdr:],
	}, true
}

// DTMFEvent is one decoded RFC 2833/4733 telephone-event packet. Sequence
// is filled in by the caller from the RTP packet so the filter can tell a
// repeated end packet from a new press of the same digit.
type DTMFEvent struct {
	Code     byte
	End      bool
	Sequence uint16
}

// ParseTelephoneEvent extracts the event code and end-of-event flag from a
// telephone-event payload. The short 4-byte form carries the E flag in bit
// 7 of byte 1; the long form (RFC 4733 section 2.5.1.3) carries it in bit 7
// of the last byte. Fewer than 4 bytes is not an event we can trust.
func ParseTelephoneEvent(payload []byte) (DTMFEvent, bool) {
	if len(payload) < 4 {
		return DTMFEvent{}, false
	}
	var ev DTMFEvent
	ev.Code = payload[0]
	if len(payload) == 4 {
		ev.End = payload[1]&0x80 != 0
	} else {
		ev.End = payload[len(payload)-1]&0x80 != 0
	}
	return ev, true
}

// dtmfEndWindow is how many packets behind or near a delivered press a
// repeated end packet for the same code still counts as the same press.
// Retransmitted end packets are adjacent in the sequence, so a window far
// below a second (50 packets/second at 20 ms framing) separates presses.
const dtmfEndWindow = 32

// DTMFFilter turns a stream of telephone-event packets into one delivery
// per press. A press is delivered once, on whichever of its start marker or
// end-of-event packet arrives first, so senders that never send end
// packets still produce digits. An end packet for a press already started
// by its marker is absorbed, and a repeated end packet for a press already
// delivered on its end is suppressed when it falls inside dtmfEndWindow
// packets of the delivery.
type DTMFFilter struct {
	openCode      byte // code of the press started by a marker
	open          bool
	deliveredCode byte   // code of the last press delivered on its end
	deliveredSeq  uint16 // sequence of that delivery
	haveDelivered bool
}

// Observe feeds one event packet through the filter. It returns the digit
// when this packet is the one delivery for its press.
func (f *DTMFFilter) Observe(ev DTMFEvent, marker bool) (byte, bool) {
	if marker && !ev.End {
		f.open, f.openCode = true, ev.Code
		return ev.Code, true
	}
	if !ev.End {
		return 0, false
	}
	if f.open && f.openCode == ev.Code {
		// The press was already delivered when its marker arrived.
		f.open = false
		return 0, false
	}
	f.open = false
	if f.haveDelivered && f.deliveredCode == ev.Code && seqWithin(ev.Sequence, f.deliveredSeq, dtmfEndWindow) {
		return 0, false // repeated end for a press already delivered
	}
	f.haveDelivered = true
	f.deliveredCode = ev.Code
	f.deliveredSeq = ev.Sequence
	return ev.Code, true
}

// seqWithin reports whether b is at most window packets after a, wrapping
// at the 16-bit sequence boundary.
func seqWithin(b, a uint16, window int) bool {
	d := int(b - a) // uint16 subtraction wraps correctly
	return d >= 0 && d <= window
}
