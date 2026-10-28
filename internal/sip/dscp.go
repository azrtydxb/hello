package sip

import (
	"context"
	"log/slog"
	"net"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

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

// fromListener makes a request the server originates leave from the SIP
// listening socket, which is marked (ListenUDP). sipgo copies the client's
// forced local address into a request only when no request options are
// passed; a call that passes options, such as ClientRequestAddVia, otherwise
// gets an ephemeral socket of sipgo's own that nothing can mark (sipgo has
// no socket-control hook) and that does not use the SIP port.
func (s *Server) fromListener(_ *sipgo.Client, r *sip.Request) error {
	if s.laddr.IP != nil {
		s.laddr.Copy(&r.Laddr)
	}
	return nil
}

// addViaFromListener is sipgo.ClientRequestAddVia sent from the listener.
func (s *Server) addViaFromListener(c *sipgo.Client, r *sip.Request) error {
	if err := sipgo.ClientRequestAddVia(c, r); err != nil {
		return err
	}
	return s.fromListener(c, r)
}
