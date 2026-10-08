package integration

import (
	"strings"
	"testing"
)

// TestHelloSocketRefusesNonHelloSource sends a SIP request to Kamailio's
// internal hello socket (UDP 5070) from a lab container that is on the edge
// network but is not a hello-sip node (a fake carrier). Kamailio trusts what
// arrives there as Hello, so it must answer 403 and not process the request.
func TestHelloSocketRefusesNonHelloSource(t *testing.T) {
	labUp(t)
	req := strings.Join([]string{
		"OPTIONS sip:kamailio@hello.edge SIP/2.0",
		"Via: SIP/2.0/UDP @IP@:5099;rport;branch=z9hG4bK-nothello",
		"Max-Forwards: 70",
		"From: <sip:intruder@hello.lab>;tag=nothello",
		"To: <sip:kamailio@hello.edge>",
		"Call-ID: nothello-5070@lab",
		"CSeq: 1 OPTIONS",
		"Content-Length: 0",
		"", "",
	}, "\r\n")
	out, err := compose("exec", "-T", "carrier-primary", "sh", "-c",
		"printf '%s' \"$0\" | sed \"s/@IP@/$(hostname -i)/\" | nc -u -p 5099 -w 3 "+strings.TrimSuffix(kamailioHelloAddr, ":5070")+" 5070", req).CombinedOutput()
	// busybox nc exits non-zero when its -w timeout ends the wait; only the
	// answer matters.
	if err != nil && len(out) == 0 {
		t.Fatalf("send to the hello socket: %v (no answer)", err)
	}
	if !strings.HasPrefix(string(out), "SIP/2.0 403") {
		logs, _ := compose("logs", "--no-log-prefix", "--tail", "30", "kamailio").CombinedOutput()
		t.Logf("kamailio log tail:\n%s", logs)
		t.Fatalf("hello socket answer to a non-Hello source = %q, want SIP/2.0 403", out)
	}
}
