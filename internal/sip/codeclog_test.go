package sip

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/media"
)

const codecLogOffer = "v=0\r\no=- 1 1 IN IP4 10.0.0.5\r\ns=-\r\nc=IN IP4 10.0.0.5\r\nt=0 0\r\n" +
	"m=audio 4000 RTP/AVP 9 0 101\r\na=rtpmap:9 G722/8000\r\na=rtpmap:0 PCMU/8000\r\n" +
	"a=rtpmap:101 telephone-event/8000\r\na=ptime:30\r\n"

const codecLogCalleeAnswer = "v=0\r\no=- 1 1 IN IP4 10.0.0.6\r\ns=-\r\nc=IN IP4 10.0.0.6\r\nt=0 0\r\n" +
	"m=audio 5000 RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\n"

// codecLogCall is a call with an anchor relay and a log capturing its output.
func codecLogCall(t *testing.T) (*call, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	s := &Server{log: slog.New(slog.NewTextHandler(&buf, nil))}
	relay, err := media.NewRelay(40200, 40300)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relay.Close)
	for _, leg := range []string{legCaller, legCallee} {
		if _, err := relay.AddLeg(leg); err != nil {
			t.Fatal(err)
		}
	}
	return &call{s: s, id: "corr-1", callID: "call-1", callerNum: "100", relay: relay, anchorHost: "192.0.2.1"}, &buf
}

func codecLines(buf *bytes.Buffer) []string {
	var out []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "msg=\"call codec\"") {
			out = append(out, l)
		}
	}
	return out
}

func wantAll(t *testing.T, line string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(line, s) {
			t.Errorf("log line %q lacks %q", line, s)
		}
	}
}

// TestCodecLogAnchoredAnswer: an anchored call logs the codec the callee
// chose (PCMU, not the caller's preferred G.722) once per leg, and traces it.
// It fails if the anchored answer does not log the negotiated codec.
func TestCodecLogAnchoredAnswer(t *testing.T) {
	c, buf := codecLogCall(t)
	off, _ := media.ParseAudioSDP([]byte(codecLogOffer))
	ans, _ := media.ParseAudioSDP([]byte(codecLogCalleeAnswer))
	c.anchoredAnswerA(off, ans)

	lines := codecLines(buf)
	if len(lines) != 2 {
		t.Fatalf("got %d codec lines, want 2 (caller, callee):\n%s", len(lines), buf)
	}
	wantAll(t, lines[0], "correlation_id=corr-1", "call_id=call-1", "leg=caller", "media_mode=anchored",
		"codec=PCMU", "clock_rate=8000", "payload_type=0", "ptime=30", "reason=answer")
	wantAll(t, lines[1], "leg=callee", "media_mode=anchored", "codec=PCMU")
	if strings.Contains(lines[0], "app=") {
		t.Errorf("bridged call logged an app: %s", lines[0])
	}
	if len(c.trace) == 0 || !strings.Contains(c.trace[0].Text, "Codec PCMU/8000 (pt 0) on the caller leg") {
		t.Errorf("trace = %v, want the codec noted", c.trace)
	}
}

// TestCodecLogTerminatedAnswer: a call Hello answers itself (IVR, queue,
// announcement) logs the codec Hello chose, G.722 for this caller, as
// terminated with the application's name. It fails if Hello-terminated
// answers are logged as anchored or without the application.
func TestCodecLogTerminatedAnswer(t *testing.T) {
	c, buf := codecLogCall(t)
	off, _ := media.ParseAudioSDP([]byte(codecLogOffer))
	c.anchoredAnswerAs("IVR", off, media.AudioSDP{})

	lines := codecLines(buf)
	if len(lines) != 1 {
		t.Fatalf("got %d codec lines, want 1:\n%s", len(lines), buf)
	}
	wantAll(t, lines[0], "leg=caller", "media_mode=terminated", "app=IVR", "codec=G722", "payload_type=9", "reason=answer")
}

// TestCodecLogReinvite: a hold re-INVITE that keeps the codec logs nothing;
// one that switches the codec logs the new one. It fails if every refresh
// is logged or a codec change is not.
func TestCodecLogReinvite(t *testing.T) {
	c, buf := codecLogCall(t)
	off, _ := media.ParseAudioSDP([]byte(codecLogOffer))
	ans, _ := media.ParseAudioSDP([]byte(codecLogCalleeAnswer))
	c.anchoredAnswerA(off, ans)
	buf.Reset()

	pcmu := off.Narrow(0)
	c.midCallOffer(pcmu)
	if n := len(codecLines(buf)); n != 0 {
		t.Fatalf("an unchanged codec logged %d lines:\n%s", n, buf)
	}
	// The re-INVITE drops PCMU: the call moves to its first supported codec.
	g722only := off.Narrow(9)
	g722only.Codecs = g722only.Codecs[:1]
	c.midCallOffer(g722only)
	lines := codecLines(buf)
	if len(lines) != 2 {
		t.Fatalf("got %d lines after a codec change, want 2:\n%s", len(lines), buf)
	}
	wantAll(t, lines[0], "codec=G722", "payload_type=9", "reason=re-invite")
}

// TestCodecLogDirect: a direct call logs the callee's answered codec for
// both legs. It fails if direct calls log nothing.
func TestCodecLogDirect(t *testing.T) {
	c, buf := codecLogCall(t)
	c.logDirectCodec("answer", []byte(codecLogCalleeAnswer))
	lines := codecLines(buf)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf)
	}
	wantAll(t, lines[0], "media_mode=direct", "codec=PCMU", "leg=caller")
	wantAll(t, lines[1], "media_mode=direct", "codec=PCMU", "leg=callee")
}

// TestCDRCodecNames: the codec each leg ended on is what the CDR stores, as
// "name/clock rate". An anchored call has both legs on the callee's choice, a
// call Hello ends itself has the caller leg only, a re-INVITE that switches
// the codec replaces it, and a call with no media has none. It fails if the
// CDR names a codec the log did not, keeps the first codec after a switch, or
// invents a callee leg.
func TestCDRCodecNames(t *testing.T) {
	c, _ := codecLogCall(t)
	if a, b := c.legCodecNames(); a != "" || b != "" {
		t.Fatalf("a call with no media has codecs %q / %q", a, b)
	}
	off, _ := media.ParseAudioSDP([]byte(codecLogOffer))
	ans, _ := media.ParseAudioSDP([]byte(codecLogCalleeAnswer))
	c.anchoredAnswerA(off, ans)
	if a, b := c.legCodecNames(); a != "PCMU/8000" || b != "PCMU/8000" {
		t.Fatalf("anchored call codecs = %q / %q, want PCMU/8000 on both", a, b)
	}
	g722only := off.Narrow(9)
	g722only.Codecs = g722only.Codecs[:1]
	c.midCallOffer(g722only)
	if a, b := c.legCodecNames(); a != "G722/8000" || b != "G722/8000" {
		t.Fatalf("codecs after a switch = %q / %q, want G722/8000 on both", a, b)
	}

	term, _ := codecLogCall(t)
	term.anchoredAnswerAs("IVR", off, media.AudioSDP{})
	if a, b := term.legCodecNames(); a != "G722/8000" || b != "" {
		t.Fatalf("terminated call codecs = %q / %q, want G722/8000 and none", a, b)
	}
}
