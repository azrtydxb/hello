package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestHelloSocketRefusesNonHelloSource sends a SIP request to Kamailio's
// internal hello socket (UDP 5070) from a lab container that is on the edge
// network but is not a hello-sip node (a fake carrier). Kamailio trusts what
// arrives there as Hello, so it must answer 403 and not process the request.
func TestHelloSocketRefusesNonHelloSource(t *testing.T) {
	labUp(t)

	// 5070 is not published to the host, so the probe runs inside the edge
	// network: a static Go sender copied into the carrier container.
	bin := filepath.Join(t.TempDir(), "udpsend")
	build := exec.Command("go", "build", "-o", bin, "./test/udpsend")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build udpsend: %v\n%s", err, out)
	}
	if out, err := compose("cp", bin, "carrier-primary:/tmp/udpsend").CombinedOutput(); err != nil {
		t.Fatalf("copy udpsend: %v\n%s", err, out)
	}

	req := strings.Join([]string{
		"OPTIONS sip:kamailio@hello.edge SIP/2.0",
		"Via: SIP/2.0/UDP 10.89.53.99:5099;rport;branch=z9hG4bK-nothello",
		"Max-Forwards: 70",
		"From: <sip:intruder@hello.lab>;tag=nothello",
		"To: <sip:kamailio@hello.edge>",
		"Call-ID: nothello-5070@lab",
		"CSeq: 1 OPTIONS",
		"Content-Length: 0",
		"", "",
	}, "\r\n")
	cmd := compose("exec", "-T", "carrier-primary", "/tmp/udpsend", kamailioHelloAddr)
	cmd.Stdin = strings.NewReader(req)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "SIP/2.0 403") {
		logs, _ := compose("logs", "--no-log-prefix", "--since", "30s", "kamailio").CombinedOutput()
		t.Logf("kamailio log:\n%s", logs)
		t.Fatalf("hello socket answer to a non-Hello source = %q (%v), want SIP/2.0 403", out, err)
	}
}
