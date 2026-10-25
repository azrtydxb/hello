package media

import (
	"fmt"
	"net"

	"golang.org/x/net/ipv4"
)

// EnableMulticast also sends the fanned-out stream to the multicast group
// addr (IPv4), as the G.711 payload type pt (0 PCMU, 8 PCMA; a stream of the
// other law is converted sample by sample). iface names the interface the
// packets leave on ("" is the system's choice) and ttl how many routers they
// cross. Multicast is one way and unacknowledged: nothing a listener sends is
// read.
func (f *Fanout) EnableMulticast(addr *net.UDPAddr, pt uint8, iface string, ttl int) error {
	if addr == nil || !addr.IP.IsMulticast() || addr.IP.To4() == nil {
		return fmt.Errorf("media: %v is not an IPv4 multicast address", addr)
	}
	if pt != 0 && pt != 8 {
		return fmt.Errorf("media: multicast payload type %d is not G.711", pt)
	}
	pc := ipv4.NewPacketConn(f.member)
	if iface != "" {
		ifi, err := net.InterfaceByName(iface)
		if err != nil {
			return fmt.Errorf("media: multicast interface: %w", err)
		}
		if err := pc.SetMulticastInterface(ifi); err != nil {
			return fmt.Errorf("media: multicast interface %s: %w", iface, err)
		}
	}
	if ttl < 1 {
		ttl = 1
	}
	if err := pc.SetMulticastTTL(ttl); err != nil {
		return fmt.Errorf("media: multicast ttl: %w", err)
	}
	// A listener on this host (the tests) should not be needed; the sender
	// never hears its own page.
	_ = pc.SetMulticastLoopback(false)
	f.mc.Store(&multicastOut{addr: addr, pt: pt})
	return nil
}

// multicastPacket is out (the stream of f.pt) as payload type pt.
func (f *Fanout) multicastPacket(out []byte, pt uint8) []byte {
	if pt == f.pt {
		return out
	}
	p, ok := ParseRTP(out)
	if !ok {
		return out
	}
	var payload []byte
	switch {
	case f.pt == 0 && pt == 8:
		payload = EncodePCMA(DecodePCMU(p.Payload))
	case f.pt == 8 && pt == 0:
		payload = EncodePCMU(DecodePCMA(p.Payload))
	default:
		return out
	}
	return BuildRTP(pt, p.Sequence, p.Timestamp, p.SSRC, p.Marker, payload)
}
