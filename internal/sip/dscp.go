package sip

import (
	"context"
	"log/slog"
	"net"

	"github.com/azrtydxb/hello/internal/media"
)

// ListenUDP binds the SIP UDP socket and marks it with dscp (HELLO_DSCP_SIP,
// default 26, CS3; spec phone-feature-parity S-21). Requests the server
// originates leave from this same socket (see Serve), so they carry the
// mark too. A platform that refuses the mark does not stop SIP: it is logged.
func ListenUDP(ctx context.Context, addr string, dscp int, log *slog.Logger) (net.PacketConn, error) {
	c, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}
	if err := media.MarkDSCP(c, dscp); err != nil {
		log.Warn("cannot mark the SIP socket with DSCP; signalling goes unmarked", "dscp", dscp, "error", err)
	}
	return c, nil
}
