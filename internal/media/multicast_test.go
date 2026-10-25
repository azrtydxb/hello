package media

import (
	"net"
	"testing"
)

// TestMulticastFanout fails if EnableMulticast accepts a unicast address,
// a payload type that is not G.711 or an unknown interface, or if a stream
// sent to a PCMA multicast channel is not converted law by law with its
// sequence, timestamp and SSRC kept (and left alone for the same law).
func TestMulticastFanout(t *testing.T) {
	f, err := NewFanout(31400, 31500, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	group := &net.UDPAddr{IP: net.IPv4(239, 255, 0, 1), Port: 5004}
	if err := f.EnableMulticast(&net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5004}, 0, "", 1); err == nil {
		t.Error("a unicast address was accepted")
	}
	if err := f.EnableMulticast(group, 9, "", 1); err == nil {
		t.Error("G.722 was accepted")
	}
	if err := f.EnableMulticast(group, 0, "no-such-interface0", 1); err == nil {
		t.Error("an unknown interface was accepted")
	}
	if f.mc.Load() != nil {
		t.Fatal("a failed EnableMulticast left a destination set")
	}

	pcm := make([]byte, frameBytes)
	for i := 0; i+1 < len(pcm); i += 2 {
		pcm[i], pcm[i+1] = byte(i), byte(i>>4)
	}
	in := BuildRTP(0, 7, 1600, 42, true, EncodePCMU(pcm))
	if got := f.multicastPacket(in, 0); string(got) != string(in) {
		t.Error("the same law was rewritten")
	}
	got, ok := ParseRTP(f.multicastPacket(in, 8))
	if !ok || got.PayloadType != 8 || got.Sequence != 7 || got.Timestamp != 1600 || got.SSRC != 42 || !got.Marker {
		t.Fatalf("converted packet = %+v", got)
	}
	want := EncodePCMA(DecodePCMU(EncodePCMU(pcm)))
	if string(got.Payload) != string(want) {
		t.Error("payload is not the A-law of the same samples")
	}
}
