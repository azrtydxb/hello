// Command udpsend sends its stdin as one UDP datagram to the address in its
// only argument and prints the first reply (waiting up to 3s). The lab tests
// copy it into a container on the edge network to probe sockets that are not
// published to the host.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: udpsend host:port < datagram")
		os.Exit(2)
	}
	msg, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	conn, err := net.Dial("udp", os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write(msg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "no reply:", err)
		os.Exit(3)
	}
	_, _ = os.Stdout.Write(buf[:n])
}
