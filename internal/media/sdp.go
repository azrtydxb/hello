package media

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AudioSDP is the slice of an SDP offer the media package acts on: where to
// send audio, which G.711 codec was negotiated, and how the remote receives
// DTMF.
type AudioSDP struct {
	Address string // IP from the c= line (IN IP4 only)
	Port    int    // m=audio transport port
	// PayloadType is the first supported static codec in the offer's m=
	// line order: 0 (PCMU) or 8 (PCMA).
	PayloadType uint8
	// DTMFPayloadType is the dynamic payload type of the offered
	// telephone-event codec, or 0 when the offer has none. DTMFRate is its
	// clock rate, 8000 when the offer omits it.
	DTMFPayloadType uint8
	DTMFRate        int
}

// ParseAudioSDP extracts the connection address, the audio port and the
// negotiated codecs from a VoIP SDP offer. A port of 0 means the remote
// refused the audio stream, which is an error here and not an answer to
// build on. Session-level c= lines apply to the media section; a
// media-level c= line overrides them. Only the first audio section is
// considered: the B2BUA offers one audio stream per call.
func ParseAudioSDP(offer []byte) (AudioSDP, error) {
	var (
		out         AudioSDP
		sessionConn string
		mediaConn   string
		pts         []uint8
		inAudio     bool
		haveAudio   bool
		dtmfPT      uint8
		dtmfRate    int
	)
	for _, line := range strings.Split(string(offer), "\n") {
		line = strings.TrimRight(line, "\r")
		// A second m= line starts a new media section; we keep only the
		// first audio one, so stop rather than mix sections.
		if haveAudio && strings.HasPrefix(line, "m=") {
			break
		}
		if len(line) < 2 || line[1] != '=' {
			continue // junk between lines is skipped, never fatal
		}
		switch line[0] {
		case 'm':
			fields := strings.Fields(line[2:])
			// m=<media> <port> <transport> <fmt>...
			if len(fields) < 4 || fields[0] != "audio" || fields[2] != "RTP/AVP" {
				continue
			}
			port, err := strconv.Atoi(fields[1])
			if err != nil {
				return out, fmt.Errorf("media: SDP m=audio port %q is not a number", fields[1])
			}
			if port == 0 {
				// Port 0 in an offer means the remote refuses this stream.
				return out, errors.New("media: remote refused audio (m=audio port 0)")
			}
			pts = pts[:0]
			for _, f := range fields[3:] {
				n, err := strconv.Atoi(f)
				if err != nil || n < 0 || n > 127 {
					continue // not a payload type; skip the token
				}
				pts = append(pts, uint8(n))
			}
			inAudio, haveAudio = true, true
			out.Port = port
		case 'c':
			// c=<nettype> <addrtype> <address>; we only speak IN IP4.
			fields := strings.Fields(line[2:])
			if len(fields) < 3 || fields[0] != "IN" || fields[1] != "IP4" {
				continue
			}
			if inAudio {
				mediaConn = fields[2]
			} else if !haveAudio {
				sessionConn = fields[2]
			}
		case 'a':
			if !inAudio {
				continue
			}
			rest, ok := strings.CutPrefix(line[2:], "rtpmap:")
			if !ok {
				continue
			}
			fields := strings.Fields(rest)
			if len(fields) < 2 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil || n < 0 || n > 127 {
				continue
			}
			name, rateStr, hasRate := strings.Cut(strings.ToLower(fields[1]), "/")
			if name != "telephone-event" {
				continue
			}
			if dtmfPT != 0 {
				continue // first telephone-event rtpmap wins
			}
			dtmfPT = uint8(n)
			dtmfRate = sampleRateHz
			if hasRate {
				r, err := strconv.Atoi(rateStr)
				if err != nil || r <= 0 {
					return out, fmt.Errorf("media: SDP telephone-event rate %q is not a number", rateStr)
				}
				dtmfRate = r
			}
		}
	}
	if !haveAudio {
		return out, errors.New("media: SDP offer has no audio media section")
	}
	addr := mediaConn
	if addr == "" {
		addr = sessionConn
	}
	if addr == "" {
		return out, errors.New("media: SDP offer has no IN IP4 connection address")
	}
	for _, p := range pts {
		if p == 0 || p == 8 {
			out.PayloadType = p
			break
		}
	}
	supported := false
	for _, p := range pts {
		if p == 0 || p == 8 {
			supported = true
			break
		}
	}
	if !supported {
		return out, errors.New("media: SDP offer lists no supported audio codec (PCMU or PCMA)")
	}
	out.Address = addr
	out.DTMFPayloadType = dtmfPT
	out.DTMFRate = dtmfRate
	return out, nil
}

// BuildAudioSDP builds a well-formed SDP answer with one audio stream: the
// negotiated G.711 payload type plus, when dtmfPT is non-zero, the offered
// telephone-event type. Lines use CRLF and the session is valid forever
// (t=0 0), the shape every SIP endpoint accepts for an answer.
func BuildAudioSDP(ip string, port int, pt uint8, dtmfPT uint8, dtmfRate int) []byte {
	if dtmfRate <= 0 {
		dtmfRate = sampleRateHz
	}
	var b strings.Builder
	b.WriteString("v=0\r\n")
	// The o= line needs a session id and version; Unix millis keep answers
	// from different sessions apart without extra state.
	fmt.Fprintf(&b, "o=- %d 1 IN IP4 %s\r\n", time.Now().UnixMilli(), ip)
	b.WriteString("s=-\r\n")
	fmt.Fprintf(&b, "c=IN IP4 %s\r\n", ip)
	b.WriteString("t=0 0\r\n")
	if dtmfPT != 0 {
		fmt.Fprintf(&b, "m=audio %d RTP/AVP %d %d\r\n", port, pt, dtmfPT)
	} else {
		fmt.Fprintf(&b, "m=audio %d RTP/AVP %d\r\n", port, pt)
	}
	fmt.Fprintf(&b, "a=rtpmap:%d %s/8000\r\n", pt, codecName(pt))
	if dtmfPT != 0 {
		fmt.Fprintf(&b, "a=rtpmap:%d telephone-event/%d\r\n", dtmfPT, dtmfRate)
	}
	b.WriteString("a=sendrecv\r\n")
	b.WriteString("a=ptime:20\r\n")
	return []byte(b.String())
}

// codecName names a static G.711 payload type for rtpmap lines; anything
// that is not PCMA is written as PCMU, the package's default codec.
func codecName(pt uint8) string {
	if pt == 8 {
		return "PCMA"
	}
	return "PCMU"
}

// BuildAudioSDPDir is BuildAudioSDP with an explicit media direction
// ("sendrecv", "sendonly", "recvonly", "inactive"); anything else is
// sendrecv.
func BuildAudioSDPDir(ip string, port int, pt uint8, dtmfPT uint8, dtmfRate int, dir string) []byte {
	switch dir {
	case "sendonly", "recvonly", "inactive":
	default:
		dir = "sendrecv"
	}
	body := BuildAudioSDP(ip, port, pt, dtmfPT, dtmfRate)
	return []byte(strings.ReplaceAll(string(body), "a=sendrecv", "a="+dir))
}

// SDPDirection reports an SDP body's media direction: the first direction
// attribute in the body, "sendrecv" when none.
func SDPDirection(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch {
		case strings.HasPrefix(line, "a=sendonly"):
			return "sendonly"
		case strings.HasPrefix(line, "a=recvonly"):
			return "recvonly"
		case strings.HasPrefix(line, "a=inactive"):
			return "inactive"
		case strings.HasPrefix(line, "a=sendrecv"):
			return "sendrecv"
		}
	}
	return "sendrecv"
}
