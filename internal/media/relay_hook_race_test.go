package media

import (
	"net"
	"sync"
	"testing"
	"time"
)

// TestRelayHookSetWhileRunning fails under -race if the receive loop reads the
// packet or DTMF hook without the relay lock while OnPacket/OnDTMF set it.
func TestRelayHookSetWhileRunning(t *testing.T) {
	r, err := NewRelay(23200, 23299)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	port, err := r.AddLeg("a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddLeg("b"); err != nil {
		t.Fatal(err)
	}
	r.SetTarget("a", udp("127.0.0.1", 4000))
	r.Start()
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		pkt := make([]byte, 172)
		pkt[0] = 0x80
		for i := 0; i < 300; i++ {
			pkt[3] = byte(i)
			_, _ = conn.Write(pkt)
			time.Sleep(time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			r.OnPacket(func(string, *RTPPacket) {})
			r.OnDTMF(func(string, byte) {})
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()
}
