package mailer

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestBuildFaxMessageRefusesInjection fails if a line break in an address, or
// a quote or line break in the attachment name, reaches the message headers.
func TestBuildFaxMessageRefusesInjection(t *testing.T) {
	ok := FaxMessage{To: "a@b.test", Peer: "+4144", Pages: 1, Received: time.Unix(0, 0), Name: "fax-1.pdf", PDF: []byte("%PDF")}
	if _, err := BuildFaxMessage("hello@pbx.test", ok); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*FaxMessage){
		"to":   func(m *FaxMessage) { m.To = "a@b.test\r\nBcc: x@y.test" },
		"name": func(m *FaxMessage) { m.Name = "x\"; filename=\"y" },
		"nl":   func(m *FaxMessage) { m.Name = "x\ny" },
	} {
		m := ok
		mod(&m)
		if _, err := BuildFaxMessage("hello@pbx.test", m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := BuildFaxMessage("", ok); err == nil {
		t.Error("accepted an empty From")
	}
	if _, err := BuildFaxMessage("a@b.test\r\nX: y", ok); err == nil {
		t.Error("accepted a From with a line break")
	}
	msg, _ := BuildFaxMessage("hello@pbx.test", FaxMessage{To: "a@b.test", Peer: "=?utf-8?q?evil?=", Name: "n.pdf", PDF: bytes.Repeat([]byte{1}, 200)})
	if strings.Count(string(msg), "\r\nBcc:") != 0 {
		t.Error("a header was injected")
	}
}
