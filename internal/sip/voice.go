// The call leg to a voice agent (spec voice-agents S-14 to S-17, S-35): the
// existing ringURI leg of the B2BUA carrying the signed X-Hello header set,
// a G.711-only offer, the shared capacity count and the S-14 refusal of the
// agent's address as a caller. Everything here reads the compiled routing
// decision and this node's configuration only — the database is never on
// this path.
package sip

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/voice"
	"github.com/emiago/sipgo/sip"
)

// VoiceConfig is the talking-agent leg's configuration, what cmd/hello-sip
// passes from config.Voice (the values must match hello-control's).
type VoiceConfig struct {
	// SIPAddress (HELLO_VOICE_SIP_ADDRESS) is talking-agent's host:port.
	// Empty disables the leg; routing refuses agent routes then (S-17).
	SIPAddress string
	// Tenant (HELLO_VOICE_TENANT) goes out as X-Hello-Tenant.
	Tenant string
	// Key signs X-Hello-Auth (HELLO_VOICE_SIP_SECRET); rotation is a
	// verifier concern, Hello always signs with the primary.
	Key voice.Key
	// MaxCalls (HELLO_VOICE_MAX_CALLS) caps simultaneous calls to agents in
	// total, 0 = unlimited.
	MaxCalls int
	// RingTimeout is how long an agent that says nothing at all may ring
	// before its leg fails 408 (spec S-15: 15 s).
	RingTimeout time.Duration
}

// VoiceSlots is the shared voice capacity state (S-35); *livestate.Store
// satisfies it. Nil disables capacity counting (tests without it).
type VoiceSlots interface {
	AcquireAgentCall(ctx context.Context, agent, call string, max, totalMax int, ttl time.Duration) (livestate.AgentCallFull, error)
	RefreshAgentCall(ctx context.Context, agent, call string, max, totalMax int, ttl time.Duration) (livestate.AgentSlotRefresh, error)
	ReleaseAgentCall(ctx context.Context, agent, call string) error
}

// VoiceLimits reports an agent's max_concurrent (S-2). It is served from a
// cache loaded off the call path (cmd/hello-sip refreshes it); nil means
// unlimited.
type VoiceLimits interface {
	AgentMaxConcurrent(agent string) int
}

// voiceRef is the call's voice agent destination: the CDR names it, and the
// capacity slots and hello_voice_* metrics follow it.
type voiceRef struct {
	Name    string
	SIPUser string
}

// voiceLeg is what an agent leg adds to its INVITE: the signed header set
// of S-15, fixed per leg so a resend carries the same values. offer is the
// caller's SDP rewritten to G.711 only (S-15), set only when the caller
// offered audio at all.
type voiceLeg struct {
	agent   string
	sipUser string
	origin  string
	ts      int64
	auth    string
	offer   []byte
}

// ringVoiceAgent is ringURI for a voice agent destination: the same leg as
// ringURI, with the signed header set, a G.711-only offer and the capacity
// gate of S-35.
func (c *call) ringVoiceAgent(req *sip.Request, tx sip.ServerTransaction, ref *routing.VoiceRef, raw string) {
	s := c.s
	v := s.cfg.Voice
	if v.SIPAddress == "" {
		// Routing validates agent routes only when HELLO_VOICE_SIP_ADDRESS
		// is set (S-17); this guard is a last resort, not the check.
		s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable")
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "voice agents not configured", ResultUnavailable)
		return
	}
	if v.Key.Secret == nil {
		// Config validation requires the secret with the address; refuse
		// rather than send an unsigned INVITE.
		s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable")
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "voice secret not configured", ResultUnavailable)
		return
	}
	var u sip.Uri
	if err := sip.ParseUri(strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">"), &u); err != nil {
		s.respond(tx, req, sip.StatusInternalServerError, "Server Internal Error")
		c.record(sip.StatusInternalServerError, cdr.SideSystem, "bad SIP URI destination", ResultFailed)
		return
	}
	if u.IsEncrypted() {
		// sips: requires TLS end to end; Hello sends SIP over UDP only.
		c.addTrace(fmt.Sprintf("Voice agent %s: %s uses sips:, which needs TLS; Hello only sends UDP", ref.Name, raw))
		s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable")
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "sips: destinations are not supported", ResultFailed)
		return
	}
	// The caller's offer must carry a codec Hello can transcode... (kept: PCMU/PCMA)
	body := req.Body()
	if len(body) > 0 {
		if _, err := media.ParseAudioSDP(body); err != nil {
			c.addTrace(fmt.Sprintf("Voice agent %s: the caller's offer has no G.711 codec (488)", ref.Name))
			s.respond(tx, req, sip.StatusNotAcceptableHere, "Not Acceptable Here")
			c.record(sip.StatusNotAcceptableHere, cdr.SideSystem, "no G.711 codec in the caller's offer", ResultFailed)
			return
		}
	}
	c.mu.Lock()
	c.voice = &voiceRef{Name: ref.Name, SIPUser: ref.SIPUser}
	c.mu.Unlock()
	if !c.begin(req, tx) {
		return
	}
	c.addTrace(fmt.Sprintf("Voice agent %s (sip:%s@%s)", ref.Name, ref.SIPUser, v.SIPAddress))
	// Capacity (S-35): counted like trunk max_calls, shared across nodes.
	if full, err := c.acquireVoice(ref.Name); err != nil {
		// The shared state is unreachable: refuse like a full source trunk
		// (503), never send an uncounted call.
		s.log.Warn("voice state unavailable", "op", "acquire_slot", "agent", ref.Name, "error", err)
		c.respondA(sip.StatusServiceUnavailable, "Service Unavailable")
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "voice state unavailable", ResultUnavailable)
		return
	} else if full != livestate.AgentCallAdmitted {
		// Treated exactly as if the agent had answered 486: the ring
		// group's next step or failure target, or 486 to the caller,
		// applies through the normal exhaustion path.
		s.m.VoiceUnreachable.WithLabelValues("capacity").Inc()
		c.mu.Lock()
		l := c.addLeg(&leg{})
		l.abandoned = true
		c.mu.Unlock()
		l.cancel()
		c.report(legEvent{leg: l, kind: evFailed, code: sip.StatusBusyHere, reason: "agent at capacity"})
		c.setup(1)
		return
	}
	vl := c.newVoiceLeg(ref)
	c.mu.Lock()
	c.voiceStart = time.Now()
	l := c.addLeg(&leg{uri: &u, voice: vl})
	c.mu.Unlock()
	s.m.VoiceActive.WithLabelValues(ref.Name).Inc()
	go l.run()
	c.setup(1)
}

// newVoiceLeg builds the leg's signed header set (S-15, S-16): tenant,
// agent, correlation id and the Unix second it was signed at.
func (c *call) newVoiceLeg(ref *routing.VoiceRef) *voiceLeg {
	s := c.s
	vl := &voiceLeg{agent: ref.Name, sipUser: ref.SIPUser}
	vl.ts = time.Now().Unix()
	vl.auth = voice.Sign(s.cfg.Voice.Key, s.cfg.Voice.Tenant, ref.Name, ref.SIPUser, c.id, time.Unix(vl.ts, 0))
	c.mu.Lock()
	tr := c.trunkName
	c.mu.Unlock()
	vl.origin = voice.OriginExtension
	if tr != "" {
		vl.origin = voice.OriginTrunk
	}
	if body := c.inv.Body(); len(body) > 0 {
		vl.offer = g711Only(body)
	}
	return vl
}

// voiceStartAt is when the INVITE towards the agent went out, for
// hello_voice_call_setup_seconds (S-32); false when this call did not send
// one (capacity refused it first, or it is not an agent call).
func (c *call) voiceStartAt() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.voiceStart, !c.voiceStart.IsZero()
}

// callerHeaderValue builds X-Hello-Caller (S-15): the presented caller
// number, or anonymous when there is none.
func (c *call) callerHeaderValue() string {
	c.mu.Lock()
	cn := c.callerNum
	c.mu.Unlock()
	if cn == "" {
		return "anonymous"
	}
	return cn
}

// voiceSlot is the capacity slot a call holds on its agent (and on the
// total), released when the attempt ends.
type voiceSlot struct {
	agent  string
	member string
	max    int
}

// acquireVoice counts the call against the agent and the total (S-35). The
// agent's max_concurrent comes from the limits cache, the total cap from
// this node's configuration.
func (c *call) acquireVoice(agent string) (livestate.AgentCallFull, error) {
	if c.s.deps.VoiceSlots == nil {
		// No shared state (tests): the active-calls gauge still counts.
		c.mu.Lock()
		c.voiceCounted = true
		c.mu.Unlock()
		return livestate.AgentCallAdmitted, nil
	}
	max := 0
	if c.s.deps.VoiceLimits != nil {
		max = c.s.deps.VoiceLimits.AgentMaxConcurrent(agent)
	}
	ctx, cancel := c.s.stateCtx()
	full, err := c.s.deps.VoiceSlots.AcquireAgentCall(ctx, agent, c.id, max, c.s.cfg.Voice.MaxCalls, c.s.cfg.CallTTL)
	cancel()
	if err != nil {
		return 0, err
	}
	if full != livestate.AgentCallAdmitted {
		return full, nil
	}
	c.mu.Lock()
	c.voiceSlots = append(c.voiceSlots, voiceSlot{agent: agent, member: c.id, max: max})
	c.voiceCounted = true
	c.mu.Unlock()
	return livestate.AgentCallAdmitted, nil
}

// refreshVoiceSlots extends every held voice slot with the call heartbeat.
func (c *call) refreshVoiceSlots() {
	c.mu.Lock()
	slots := append([]voiceSlot(nil), c.voiceSlots...)
	c.mu.Unlock()
	if len(slots) == 0 || c.s.deps.VoiceSlots == nil {
		return
	}
	total := c.s.cfg.Voice.MaxCalls
	for _, h := range slots {
		ctx, cancel := c.s.stateCtx()
		r, err := c.s.deps.VoiceSlots.RefreshAgentCall(ctx, h.agent, h.member, h.max, total, c.s.cfg.CallTTL)
		cancel()
		switch {
		case err != nil:
			c.s.log.Warn("could not refresh voice slot", "correlation_id", c.id, "agent", h.agent, "error", err)
		case r == livestate.AgentSlotOvercommitted:
			c.s.log.Info("voice slot was lost and has been taken again over the agent cap", "correlation_id", c.id, "agent", h.agent)
		case r == livestate.AgentTotalSlotOvercommitted:
			c.s.log.Warn("voice slot was lost and the total is full: keeping the call over HELLO_VOICE_MAX_CALLS", "correlation_id", c.id, "agent", h.agent)
		}
	}
}

// releaseVoiceSlots frees every voice slot the call holds; record and the
// HA yield path call it on every way an attempt ends.
func (c *call) releaseVoiceSlots() {
	c.mu.Lock()
	gone := c.voiceSlots
	c.voiceSlots = nil
	counted := c.voiceCounted
	agent := ""
	if c.voice != nil {
		agent = c.voice.Name
	}
	c.mu.Unlock()
	if len(gone) == 0 && !counted {
		return
	}
	if counted && agent != "" {
		c.s.m.VoiceActive.WithLabelValues(agent).Dec()
	}
	if c.s.deps.VoiceSlots == nil || len(gone) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, h := range gone {
		if err := c.s.deps.VoiceSlots.ReleaseAgentCall(ctx, h.agent, h.member); err != nil {
			c.s.log.Warn("could not release voice slot", "correlation_id", c.id, "agent", h.agent, "error", err)
		}
	}
}

// voiceRingDeadline wraps l.ctx with the agent ring timeout (S-15: 15 s):
// an agent that stays silent fails its leg 408 instead of holding the
// caller's ring stage open for the full RingTimeout. The cancel it returns
// releases the timer; the leg's drive defers it.
func (l *leg) voiceRingDeadline() context.CancelFunc {
	if l.voice == nil || l.c.s.cfg.Voice.RingTimeout <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithTimeout(l.ctx, l.c.s.cfg.Voice.RingTimeout)
	l.mu.Lock()
	l.ctx = ctx
	l.mu.Unlock()
	return cancel
}

// voiceUnreachableLabel maps an agent leg's failure to the
// hello_voice_agent_unreachable_total reason (S-32): timeout, the response
// codes Hello maps, capacity, or failed for anything else.
func voiceUnreachableLabel(code int, err error) string {
	switch {
	case errors.Is(err, sip.ErrTransactionTimeout), errors.Is(err, context.DeadlineExceeded), code == sip.StatusRequestTimeout:
		return "timeout"
	case code == 403 || code == 404 || code == 486 || code == 503:
		return strconv.Itoa(code)
	default:
		return "failed"
	}
}

// g711Only rewrites an SDP offer to offer PCMU and PCMA only (S-15): the
// first audio section keeps just payload types 0 and 8, rtpmap/fmtp lines
// of dropped codecs go, later media sections are dropped, and ptime is 20.
// The caller's offer has already been checked to carry one of the two.
func g711Only(offer []byte) []byte {
	var (
		b     strings.Builder
		state int  // 0 session, 1 in first audio section, 2 done
		ptime bool // an a=ptime was rewritten to 20
	)
	b.Grow(len(offer))
	for _, raw := range strings.Split(string(offer), "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "m="):
			if state != 0 {
				state = 2
				continue
			}
			f := strings.Fields(line[2:]) // m=<media> <port> <transport> <fmt>...
			if len(f) < 4 || f[0] != "audio" || f[2] != "RTP/AVP" {
				state = 2
				continue
			}
			state = 1
			b.WriteString("m=audio ")
			b.WriteString(f[1])
			b.WriteString(" RTP/AVP")
			for _, pt := range f[3:] {
				if pt == "0" || pt == "8" {
					b.WriteString(" ")
					b.WriteString(pt)
				}
			}
			b.WriteString("\r\n")
		case state == 2:
			// Later sections are dropped: Hello offers one audio stream.
		case state == 0:
			// Session-level lines (v=, o=, s=, c=, t=) are kept as they are.
			b.WriteString(line)
			b.WriteString("\r\n")
		case state == 1:
			switch {
			case strings.HasPrefix(line, "a=rtpmap:"):
				pt, _, _ := strings.Cut(strings.TrimPrefix(line, "a=rtpmap:"), " ")
				if keep := pt == "0" || pt == "8"; keep {
					b.WriteString(line)
					b.WriteString("\r\n")
				}
			case strings.HasPrefix(line, "a=fmtp:"):
				pt, _, _ := strings.Cut(strings.TrimPrefix(line, "a=fmtp:"), " ")
				if pt == "0" || pt == "8" {
					b.WriteString(line)
					b.WriteString("\r\n")
				}
			case strings.HasPrefix(line, "a=ptime:"):
				b.WriteString("a=ptime:20\r\n")
				ptime = true
			case strings.HasPrefix(line, "a=mid:"), strings.HasPrefix(line, "a=extmap:"):
				// Bound to sections Hello dropped: gone with them.
			default:
				b.WriteString(line)
				b.WriteString("\r\n")
			}
		}
	}
	if state == 1 && !ptime {
		b.WriteString("a=ptime:20\r\n")
	}
	return []byte(b.String())
}

// fromVoiceAgent reports whether req comes from the voice agent's SIP
// address (S-14): talking-agent sends from the socket Hello calls, so the
// literal ip:port of HELLO_VOICE_SIP_ADDRESS identifies it. A hostname
// address never matches, and neither does any other peer on the same IP.
func (s *Server) fromVoiceAgent(req *sip.Request) bool {
	if !s.voiceAddr.IsValid() {
		return false
	}
	src, err := netip.ParseAddrPort(req.Source())
	if err != nil {
		return false
	}
	return src == s.voiceAddr
}

// applyVoiceHeaders appends the X-Hello set (S-15) to the leg's INVITE and
// strips any of those header names arriving from the caller: none of them
// is ever copied from the caller's INVITE.
func (l *leg) applyVoiceHeaders(req *sip.Request) {
	vl := l.voice
	if vl == nil {
		return
	}
	v := l.c.s.cfg.Voice
	// Strip first (S-15: a caller-supplied X-Hello-* must not survive).
	for _, h := range []string{voice.HeaderTenant, voice.HeaderAgent, voice.HeaderCorrelation, voice.HeaderCaller,
		voice.HeaderCalled, voice.HeaderCallerOrigin, voice.HeaderTs, voice.HeaderAuth} {
		req.RemoveHeader(h)
	}
	req.AppendHeader(sip.NewHeader(voice.HeaderTenant, v.Tenant))
	req.AppendHeader(sip.NewHeader(voice.HeaderAgent, vl.agent))
	req.AppendHeader(sip.NewHeader(voice.HeaderCorrelation, l.c.id))
	req.AppendHeader(sip.NewHeader(voice.HeaderCaller, l.c.callerHeaderValue()))
	req.AppendHeader(sip.NewHeader(voice.HeaderCalled, l.c.dialled))
	req.AppendHeader(sip.NewHeader(voice.HeaderCallerOrigin, vl.origin))
	req.AppendHeader(sip.NewHeader(voice.HeaderTs, strconv.FormatInt(vl.ts, 10)))
	req.AppendHeader(sip.NewHeader(voice.HeaderAuth, vl.auth))
}
