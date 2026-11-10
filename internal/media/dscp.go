package media

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// DefaultRTPDSCP is the DSCP of relayed RTP: EF (46), expedited forwarding
// (spec phone-feature-parity S-21).
const DefaultRTPDSCP = 46

// rtpDSCP is the DSCP every RTP socket this package binds is marked with
// (HELLO_DSCP_RTP). It is set once at startup by SetRTPDSCP.
var rtpDSCP atomic.Int32

var markWarn sync.Once

func init() { rtpDSCP.Store(DefaultRTPDSCP) }

// SetRTPDSCP sets the DSCP (0 to 63) of the RTP sockets bound from now on.
// Out-of-range values are ignored.
func SetRTPDSCP(dscp int) {
	if dscp >= 0 && dscp <= 63 {
		rtpDSCP.Store(int32(dscp)) //nolint:gosec // G115: range checked
	}
}

// RTPDSCP is the DSCP RTP sockets are marked with.
func RTPDSCP() int { return int(rtpDSCP.Load()) }

// MarkDSCP sets the IP TOS / traffic class of c's outgoing datagrams to
// dscp (the six high bits of the byte). It tries IPv4 and then IPv6, so a
// dual-stack or IPv6 socket is marked too.
func MarkDSCP(c net.PacketConn, dscp int) error {
	if dscp < 0 || dscp > 63 {
		return fmt.Errorf("media: DSCP %d out of range 0-63", dscp)
	}
	tos := dscp << 2 // the two low bits are ECN, left clear
	var err4, err6 error
	if err4 = ipv4.NewPacketConn(c).SetTOS(tos); err4 == nil {
		return nil
	}
	if err6 = ipv6.NewPacketConn(c).SetTrafficClass(tos); err6 == nil {
		return nil
	}
	return errors.Join(err4, err6)
}

// markRTP marks an RTP socket. A failure (a platform or sandbox that refuses
// IP_TOS) never fails the call: the audio flows unmarked, and the first
// failure is logged.
func markRTP(c *net.UDPConn) {
	if err := MarkDSCP(c, RTPDSCP()); err != nil {
		markWarn.Do(func() {
			slog.Warn("cannot mark RTP sockets with DSCP; media goes unmarked", "dscp", RTPDSCP(), "error", err)
		})
	}
}
