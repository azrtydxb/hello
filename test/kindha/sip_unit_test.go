package kindha

import (
	"strings"
	"testing"
)

// The toolkit's own tests run everywhere (no cluster): a failover test whose
// parser is wrong would fail for the wrong reason.

const sampleInvite = "INVITE sip:bob@hello.kind SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.2:5060;branch=z9hG4bKaaa;rport, SIP/2.0/UDP 10.0.0.9:1;branch=z9hG4bKbbb\r\n" +
	"Record-Route: <sip:10.0.0.1:5070;r2=on;lr>\r\n" +
	"Record-Route: <sip:10.0.0.1:30508;r2=on;lr>\r\n" +
	"f: <sip:alice@hello.kind>;tag=abc\r\n" +
	"To: <sip:bob@hello.kind>\r\n" +
	"i: call-1@x\r\n" +
	"CSeq: 7 INVITE\r\n" +
	"Contact: <sip:alice@10.0.0.9:5060;alias=10.0.0.9~5060~1>\r\n" +
	"Content-Length: 4\r\n\r\nbody"

func TestParse(t *testing.T) {
	m, ok := parseMsg([]byte(sampleInvite), nil)
	if !ok {
		t.Fatal("not parsed")
	}
	if !m.isReq() || m.method() != "INVITE" || m.ruri() != "sip:bob@hello.kind" {
		t.Fatalf("request line: %q %q", m.method(), m.ruri())
	}
	if got := m.all("Via"); len(got) != 2 || branchOf(got[0]) != "z9hG4bKaaa" {
		t.Fatalf("Via = %v", got)
	}
	if got := m.get("Call-ID"); got != "call-1@x" {
		t.Fatalf("compact Call-ID = %q", got)
	}
	if tagOf(m.get("From")) != "abc" {
		t.Fatalf("From tag = %q", tagOf(m.get("From")))
	}
	rr := m.all("Record-Route")
	if len(rr) != 2 {
		t.Fatalf("Record-Route = %v", rr)
	}
	if h, p := hostPort(rr[0]); h != "10.0.0.1" || p != 5070 {
		t.Fatalf("hostPort(%q) = %s:%d", rr[0], h, p)
	}
	if got := uriOf(m.get("Contact")); got != "sip:alice@10.0.0.9:5060;alias=10.0.0.9~5060~1" {
		t.Fatalf("uriOf(Contact) = %q", got)
	}
	if h, p := hostPort("sip:alice@10.0.0.9:5060;alias=1~2~3"); h != "10.0.0.9" || p != 5060 {
		t.Fatalf("hostPort with user = %s:%d", h, p)
	}
	if h, p := hostPort("<sip:10.0.0.1;lr>"); h != "10.0.0.1" || p != 5060 {
		t.Fatalf("hostPort default port = %s:%d", h, p)
	}
	if string(m.body) != "body" {
		t.Fatalf("body = %q", m.body)
	}
}

func TestResponseShape(t *testing.T) {
	req, _ := parseMsg([]byte(sampleInvite), nil)
	// A reply copies the Vias, the Record-Routes of an INVITE and adds the tag.
	r := &msg{first: "SIP/2.0 200 OK"}
	for _, h := range req.h {
		switch strings.ToLower(h.k) {
		case "via", "record-route", "from", "call-id", "cseq":
			r.h = append(r.h, h)
		}
	}
	back, ok := parseMsg(r.bytes(), nil)
	if !ok || back.status() != 200 || back.method() != "INVITE" {
		t.Fatalf("round trip: %v %d %q", ok, back.status(), back.method())
	}
	if got := reversed(back.all("Record-Route")); len(got) != 2 || !strings.Contains(got[0], "30508") {
		t.Fatalf("reversed route set = %v", got)
	}
}

func TestDialogDestination(t *testing.T) {
	d := &dialog{target: "sip:alice@10.0.0.9:5060", routes: []string{"<sip:10.0.0.1:5070;r2=on;lr>", "<sip:10.0.0.1:30508;r2=on;lr>"}}
	if got := d.dst(); got != "10.0.0.1:5070" {
		t.Fatalf("dst with a route set = %s", got)
	}
	d.routes = nil
	if got := d.dst(); got != "10.0.0.9:5060" {
		t.Fatalf("dst without = %s", got)
	}
}
