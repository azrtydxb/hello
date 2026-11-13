package media

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// T38SDP is the T.38 part of an SDP (ITU-T T.38 Annex D, RFC 3362): an
// m=image line with transport udptl and format t38, and its a=T38... lines.
type T38SDP struct {
	Address string // IP from the c= line (IN IP4 only)
	Port    int    // m=image port; 0 means the stream is refused
	// MaxBitRate is T38MaxBitRate, 14400 when absent.
	MaxBitRate int
	// RateManagement is T38FaxRateManagement: transferredTCF or
	// localTCF; empty when absent.
	RateManagement string
	// UDPEC is T38FaxUdpEC: t38UDPRedundancy, t38UDPFEC or t38UDPNoEC; empty
	// when absent.
	UDPEC string
	// Version is T38FaxVersion, 0 when absent.
	Version int
}

// T.38 parameters Hello answers with (pbx-gaps S-16).
const (
	T38Version        = 0
	T38MaxBitRate     = 14400
	T38RateManagement = "transferredTCF"
	T38UDPEC          = "t38UDPRedundancy"
)

// ParseT38SDP finds the image stream of an SDP. ok is false when the SDP
// carries none (not T.38), and a refused stream (port 0) is returned with
// Port 0 and ok true so the caller can tell a refusal from silence.
func ParseT38SDP(body []byte) (T38SDP, bool) {
	var (
		out         T38SDP
		sessionConn string
		mediaConn   string
		inImage     bool
		found       bool
	)
	out.MaxBitRate = 14400
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 2 || line[1] != '=' {
			continue
		}
		if line[0] == 'm' {
			if found {
				break // only the first image stream
			}
			f := strings.Fields(line[2:])
			if len(f) >= 4 && f[0] == "image" && strings.EqualFold(f[2], "udptl") && strings.EqualFold(f[3], "t38") {
				port, err := strconv.Atoi(f[1])
				if err != nil || port < 0 || port > 65535 {
					return T38SDP{}, false
				}
				out.Port, inImage, found = port, true, true
			} else {
				inImage = false
			}
			continue
		}
		switch line[0] {
		case 'c':
			f := strings.Fields(line[2:])
			if len(f) < 3 || f[0] != "IN" || f[1] != "IP4" {
				continue
			}
			if inImage {
				mediaConn = f[2]
			} else if !found {
				sessionConn = f[2]
			}
		case 'a':
			if !inImage {
				continue
			}
			k, v, _ := strings.Cut(line[2:], ":")
			v = strings.TrimSpace(v)
			switch strings.ToLower(k) {
			case "t38maxbitrate":
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					out.MaxBitRate = n
				}
			case "t38faxratemanagement":
				out.RateManagement = v
			case "t38faxudpec":
				out.UDPEC = v
			case "t38faxversion":
				out.Version, _ = strconv.Atoi(v)
			}
		}
	}
	if !found {
		return T38SDP{}, false
	}
	out.Address = mediaConn
	if out.Address == "" {
		out.Address = sessionConn
	}
	return out, true
}

// OffersT38 reports whether body offers a usable T.38 stream.
func OffersT38(body []byte) bool {
	t, ok := ParseT38SDP(body)
	return ok && t.Port != 0
}

// BuildT38SDP writes the SDP Hello offers or answers with for a T.38 call:
// version 0, 14400 bit/s, transferred TCF and UDP redundancy.
func BuildT38SDP(ip string, port int) []byte {
	var b strings.Builder
	b.WriteString("v=0\r\n")
	fmt.Fprintf(&b, "o=- %d 2 IN IP4 %s\r\n", time.Now().UnixMilli(), ip)
	b.WriteString("s=-\r\n")
	fmt.Fprintf(&b, "c=IN IP4 %s\r\n", ip)
	b.WriteString("t=0 0\r\n")
	fmt.Fprintf(&b, "m=image %d udptl t38\r\n", port)
	fmt.Fprintf(&b, "a=T38FaxVersion:%d\r\n", T38Version)
	fmt.Fprintf(&b, "a=T38MaxBitRate:%d\r\n", T38MaxBitRate)
	fmt.Fprintf(&b, "a=T38FaxRateManagement:%s\r\n", T38RateManagement)
	b.WriteString("a=T38FaxMaxBuffer:1760\r\n")
	b.WriteString("a=T38FaxMaxDatagram:400\r\n")
	fmt.Fprintf(&b, "a=T38FaxUdpEC:%s\r\n", T38UDPEC)
	return []byte(b.String())
}

// T38Acceptable reports whether the far end's T.38 parameters can be served:
// version 0, or no version; UDP redundancy, or none stated (the far end will
// then use what we sent). Transferred TCF is required: Hello's terminal does
// not run local TCF.
func (t T38SDP) T38Acceptable() bool {
	if t.Port == 0 || t.Version != 0 {
		return false
	}
	if t.RateManagement != "" && !strings.EqualFold(t.RateManagement, T38RateManagement) {
		return false
	}
	return t.UDPEC == "" || strings.EqualFold(t.UDPEC, T38UDPEC) || strings.EqualFold(t.UDPEC, "t38UDPNoEC")
}
