package media

import (
	"net"
	"testing"
	"time"
)

func udp(ip string, port int) *net.UDPAddr { return &net.UDPAddr{IP: net.ParseIP(ip), Port: port} }

// TestRelaySourcePolicy fails if a leg takes media from an address that is
// neither the one its SDP names, one the signalling vouched for, nor (before
// the first packet, when the SDP address is unusable) the packet that pins
// it; or if, once pinned, a different address can take the leg over.
func TestRelaySourcePolicy(t *testing.T) {
	type step struct {
		from string
		want bool
	}
	cases := []struct {
		name   string
		target string // SDP address, "" for none
		extra  []string
		steps  []step
	}{
		{"public SDP, stranger first", "203.0.113.5", nil, []step{
			{"198.51.100.9", false}, {"203.0.113.5", true}, {"198.51.100.9", false}, {"203.0.113.5", true}}},
		{"spoof after the pin is ignored", "203.0.113.5", nil, []step{
			{"203.0.113.5", true}, {"198.51.100.9", false}, {"198.51.100.9", false}}},
		{"NAT: private SDP, nothing vouched, first packet pins", "192.168.1.10", nil, []step{
			{"198.51.100.7", true}, {"198.51.100.9", false}, {"198.51.100.7", true}}},
		{"NAT: private SDP, signalling address vouched", "192.168.1.10", []string{"198.51.100.7"}, []step{
			{"198.51.100.9", false}, {"198.51.100.7", true}, {"198.51.100.9", false}}},
		{"no SDP address, first packet pins", "", nil, []step{
			{"198.51.100.7", true}, {"198.51.100.9", false}}},
		{"hold address 0.0.0.0", "0.0.0.0", nil, []step{
			{"198.51.100.7", true}, {"198.51.100.9", false}}},
		{"vouched address may take over a pinned leg", "203.0.113.5", []string{"198.51.100.7"}, []step{
			{"203.0.113.5", true}, {"198.51.100.9", false}, {"198.51.100.7", true}, {"203.0.113.5", false}}},
		{"CGNAT SDP counts as private", "100.64.1.2", nil, []step{
			{"198.51.100.7", true}, {"198.51.100.9", false}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &relayLeg{}
			if c.target != "" {
				l.sdpIP = net.ParseIP(c.target)
			}
			for _, e := range c.extra {
				l.extraIPs = append(l.extraIPs, net.ParseIP(e))
			}
			for i, s := range c.steps {
				// A different port each time: only the address matters.
				if got := l.sourceOK(udp(s.from, 4000+i)); got != s.want {
					t.Errorf("step %d: %s accepted = %v, want %v", i, s.from, got, s.want)
				}
			}
		})
	}
}

// TestRelayNewTargetRestartsSourcePolicy fails if a re-INVITE (SetTarget)
// leaves the old pin in force, which would refuse the phone's new address,
// or if the vouched signalling addresses are forgotten by it.
func TestRelayNewTargetRestartsSourcePolicy(t *testing.T) {
	r, err := NewRelay(23000, 23999)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.AddLeg("a"); err != nil {
		t.Fatal(err)
	}
	r.SetTarget("a", udp("203.0.113.5", 4000))
	r.AllowSource("a", net.ParseIP("198.51.100.7"))
	l := r.findLeg("a")
	if !l.sourceOK(udp("203.0.113.5", 4000)) || l.sourceOK(udp("203.0.113.6", 4000)) {
		t.Fatal("first target not enforced")
	}
	r.SetTarget("a", udp("203.0.113.6", 4000))
	if !l.sourceOK(udp("203.0.113.6", 4000)) {
		t.Error("the re-INVITE's new address was refused")
	}
	if l.sourceOK(udp("203.0.113.5", 4000)) {
		t.Error("the old address still accepted after the re-INVITE")
	}
	if !l.sourceOK(udp("198.51.100.7", 4000)) {
		t.Error("the vouched address was forgotten by SetTarget")
	}
}

// TestRelayDropsForeignSource fails if a datagram from an address the leg
// does not expect is counted, latched or relayed: a third party sending to
// the relay port must neither inject audio into the call nor redirect the
// other side's audio to itself.
func TestRelayDropsForeignSource(t *testing.T) {
	r, pa, pb := testRelay(t)
	// Leg a's SDP names an address the peers (127.0.0.1) are not at.
	r.SetTarget("a", udp("192.0.2.1", 4000))
	payload := make([]byte, 160)
	pa.send(0, payload, false)
	_ = pb.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, rtpMaxPacket)
	if _, _, err := pb.conn.ReadFromUDP(buf); err == nil {
		t.Fatal("audio from a foreign source was relayed")
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.Metrics().Directions["a>b"].Rejected == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	st := r.Metrics().Directions["a>b"]
	if st.Rejected == 0 || st.Packets != 0 {
		t.Errorf("rejected %d, counted %d; want the packet rejected and not counted", st.Rejected, st.Packets)
	}
	if got := r.LegTarget("a"); got == nil || got.IP.String() != "192.0.2.1" {
		t.Errorf("a foreign packet moved the leg's target to %v", got)
	}
	// Vouched by the signalling: the same packets now flow.
	r.AllowSource("a", net.ParseIP("127.0.0.1"))
	pa.send(0, payload, false)
	pb.recv(t)
	// And once pinned, a source on another address cannot take the leg:
	// checked at the policy level (loopback has one usable address here).
	l := r.findLeg("a")
	if l.sourceOK(udp("198.51.100.9", 5000)) {
		t.Error("a spoofed source took over a pinned leg")
	}
}
