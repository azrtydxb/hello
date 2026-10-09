// Fake voice agent behaviours (spec voice-agents): what a talking-agent
// stand-in checks about the INVITE Hello sends it, so tests prove the
// X-Hello contract the same way the real agent will.
package sipua

import (
	"fmt"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/voice"
	"github.com/emiago/sipgo/sip"
)

// VerifyHello checks an INVITE as a voice agent must (S-15, S-16): every
// X-Hello header present, the caller origin one of the two values, the
// timestamp within skew, and X-Hello-Auth the valid signature over
// tenant|agent|sipUser|correlation|ts with the shared key. It fails when
// any header is missing and when a caller-planted header value does not
// verify.
func VerifyHello(req *sip.Request, key voice.Key, tenant, agent, sipUser string, now time.Time) error {
	want := map[string]string{
		voice.HeaderTenant:       tenant,
		voice.HeaderAgent:        agent,
		voice.HeaderCallerOrigin: "",
		voice.HeaderTs:           "",
		voice.HeaderAuth:         "",
	}
	for name, v := range want {
		h := req.GetHeader(name)
		if h == nil {
			return fmt.Errorf("sipua: missing header %s", name)
		}
		if v != "" && h.Value() != v {
			return fmt.Errorf("sipua: header %s = %q, want %q", name, h.Value(), v)
		}
	}
	if req.GetHeader(voice.HeaderCorrelation) == nil {
		return fmt.Errorf("sipua: missing header %s", voice.HeaderCorrelation)
	}
	if req.GetHeader(voice.HeaderCaller) == nil {
		return fmt.Errorf("sipua: missing header %s", voice.HeaderCaller)
	}
	if req.GetHeader(voice.HeaderCalled) == nil {
		return fmt.Errorf("sipua: missing header %s", voice.HeaderCalled)
	}
	switch o := req.GetHeader(voice.HeaderCallerOrigin).Value(); o {
	case voice.OriginExtension, voice.OriginTrunk:
	default:
		return fmt.Errorf("sipua: caller origin %q", o)
	}
	ts, err := voice.ParseTs(req.GetHeader(voice.HeaderTs).Value())
	if err != nil {
		return fmt.Errorf("sipua: bad timestamp: %w", err)
	}
	if d := now.Sub(ts); d > voice.MaxSkew || d < -voice.MaxSkew {
		return fmt.Errorf("sipua: timestamp %v outside the skew", ts)
	}
	auth := req.GetHeader(voice.HeaderAuth).Value()
	correlation := req.GetHeader(voice.HeaderCorrelation).Value()
	if err := voice.Verify([]voice.Key{key}, auth, now, tenant, agent, sipUser, correlation, ts); err != nil {
		return fmt.Errorf("sipua: signature: %w", err)
	}
	return nil
}

// OfferAudio reads the INVITE's SDP offer the way a voice agent would: the
// audio payload types offered, the ptime, and whether the body is SDP at
// all (an offer-less INVITE reports ok false).
func OfferAudio(req *sip.Request) (pts []string, ptime string, ok bool, err error) {
	body := req.Body()
	if len(body) == 0 {
		return nil, "", false, nil
	}
	if ct := req.ContentType(); ct == nil || !strings.HasPrefix(ct.Value(), "application/sdp") {
		return nil, "", false, fmt.Errorf("spt: body is not SDP: %s", string(body))
	}
	inAudio := false
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "m=audio"):
			inAudio = true
			pts = strings.Fields(line)[3:]
		case inAudio && strings.HasPrefix(line, "a=ptime:"):
			ptime = strings.TrimPrefix(line, "a=ptime:")
		}
	}
	if pts == nil {
		return nil, "", false, fmt.Errorf("spt: no audio section in %q", string(body))
	}
	return pts, ptime, true, nil
}
