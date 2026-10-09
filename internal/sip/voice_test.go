package sip

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/voice"
	"github.com/azrtydxb/hello/test/sipua"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// --- voice capacity fakes ---------------------------------------------------

// fakeVoiceSlots is the shared voice capacity state in memory, with the
// same admit rule as livestate's script.
type fakeVoiceSlots struct {
	mu    sync.Mutex
	agent map[string]map[string]bool // agent -> members
	total map[string]bool
	down  bool
}

func newFakeVoiceSlots() *fakeVoiceSlots {
	return &fakeVoiceSlots{agent: map[string]map[string]bool{}, total: map[string]bool{}}
}

func (f *fakeVoiceSlots) AcquireAgentCall(_ context.Context, agent, call string, max, totalMax int, _ time.Duration) (livestate.AgentCallFull, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return 0, errDown
	}
	if f.agent[agent] == nil {
		f.agent[agent] = map[string]bool{}
	}
	if !f.agent[agent][call] && max > 0 && len(f.agent[agent]) >= max {
		return livestate.AgentFull, nil
	}
	if !f.total[call] && totalMax > 0 && len(f.total) >= totalMax {
		return livestate.AgentTotalFull, nil
	}
	f.agent[agent][call] = true
	f.total[call] = true
	return livestate.AgentCallAdmitted, nil
}

func (f *fakeVoiceSlots) RefreshAgentCall(context.Context, string, string, int, int, time.Duration) (livestate.AgentSlotRefresh, error) {
	return livestate.AgentSlotRefreshed, nil
}

func (f *fakeVoiceSlots) ReleaseAgentCall(_ context.Context, agent, call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.agent[agent], call)
	delete(f.total, call)
	return nil
}

// mapLimits is a VoiceLimits served from a fixed set.
type mapLimits map[string]int

func (l mapLimits) AgentMaxConcurrent(agent string) int { return l[agent] }

// --- harness ----------------------------------------------------------------

// voiceTestKey is a throwaway signing key, the way the tests' nonce secret
// is (testSecret): long enough for the config's own rule, committed as a
// repeated filler so no real-looking value lands in history.
var voiceTestKey = voice.NewKey(strings.Repeat("v", 40))

// agentSDP is the SDP the fake agent answers with.
const agentSDP = "v=0\r\no=agent 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 0\r\n"

// startAgent starts the fake talking-agent on the reserved socket (so its
// exact address can go into the voice configuration before the PBX
// starts) and returns it.
func startAgent(t *testing.T, conn net.PacketConn, proxy string) *sipua.Phone {
	t.Helper()
	ag, err := sipua.New(sipua.Options{User: "talking-agent", Domain: testDomain, Proxy: proxy, Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ag.Close)
	return ag
}

// reserveAgentSocket binds the agent's UDP socket and returns it with its
// address, before the PBX that will be configured with it exists.
func reserveAgentSocket(t *testing.T) (net.PacketConn, string) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return conn, conn.LocalAddr().String()
}

// agentNext waits for the agent's next incoming call.
func agentNext(t *testing.T, ag *sipua.Phone) *sipua.Incoming {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	in, err := ag.Next(ctx)
	if err != nil {
		t.Fatalf("agent saw no incoming call: %v", err)
	}
	return in
}

// startVoicePBX starts a PBX whose routing sends every external-looking
// number to the voice agent "support" (sip user 2201) at the agent's
// address, plus the fake agent itself. Extra opts refine the voice
// configuration, the capacity state and the limits.
func startVoicePBX(t *testing.T, extra ...pbxOpt) (*testPBX, *sipua.Phone, *fakeRouter) {
	t.Helper()
	conn, addr := reserveAgentSocket(t)
	r := &fakeRouter{trunks: []routing.Trunk{{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true}}}
	r.decide = func(c routing.Call, _ routing.TrunkUsability) routing.Decision {
		name, sipUser := "support", "2201"
		if c.Number == "8600" {
			name, sipUser = "reception", "2202"
		}
		var d routing.Decision
		d.Kind, d.Route = routing.KindInbound, name+" agent"
		d.SIPURI = "sip:" + sipUser + "@" + addr
		d.VoiceAgent = &routing.VoiceRef{Name: name, SIPUser: sipUser}
		d.Trace.Add(`Route "` + name + ` agent" -> voice agent "` + name + `" (sip:` + sipUser + `@` + addr + `)`)
		return d
	}
	opts := append([]pbxOpt{
		withRouter(r, nil),
		trunkCfg(newFakeTrunkState()),
		func(c *Config, _ *Deps) {
			c.Voice = VoiceConfig{SIPAddress: addr, Tenant: "lab", Key: voiceTestKey, RingTimeout: 300 * time.Millisecond}
		},
	}, extra...)
	pbx := startPBX(t, callerDevices(), opts...)
	ag := startAgent(t, conn, pbx.addr)
	return pbx, ag, r
}

// agentAnswer answers the agent's next incoming call as soon as it
// arrives; the channel yields the call once it is up.
func agentAnswer(t *testing.T, ag *sipua.Phone) <-chan *sipua.Incoming {
	t.Helper()
	ch := make(chan *sipua.Incoming, 1)
	go func() {
		in := agentNext(t, ag)
		if err := in.Answer([]byte(agentSDP)); err != nil {
			t.Error(err)
		}
		ch <- in
	}()
	return ch
}

// waitEnded waits for the agent's dialog to end, after the caller hung up.
func waitEnded(t *testing.T, in *sipua.Incoming) {
	t.Helper()
	end := in.Ended()
	select {
	case <-end:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent leg did not end")
	}
}

// dialAgent places a registered phone's call to the agent destination
// ("8500" reaches support, "8600" reception).
func dialAgent(t *testing.T, p *phone, number string) callResult {
	t.Helper()
	return waitCall(t, dial(t.Context(), p, number))
}

// histCount is a histogram's observation count.
func histCount(t *testing.T, pbx *testPBX, name string) float64 {
	t.Helper()
	mfs, err := pbx.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return float64(mf.GetMetric()[0].GetHistogram().GetSampleCount())
		}
	}
	return 0
}

// --- tests ------------------------------------------------------------------

// TestVoiceAgentInvite fails if the INVITE towards the agent lacks any
// header of S-15, carries a signature that does not verify with the shared
// key, offers anything but PCMU at ptime 20, or if the call does not end
// cleanly with the agent named on the CDR.
func TestVoiceAgentInvite(t *testing.T) {
	pbx, ag, _ := startVoicePBX(t)
	caller := newPhone(t, pbx, "a1", "pa")
	caller.register(t)

	inCh := agentAnswer(t, ag)
	r := dialAgent(t, caller, "8500")
	if r.err != nil {
		t.Fatalf("call to the agent: %v", r.err)
	}
	in := <-inCh
	inv := in.Request
	if inv.Recipient.User != "2201" {
		t.Fatalf("R-URI user = %s, want the sip user 2201", inv.Recipient.User)
	}
	key := voiceTestKey
	if err := sipua.VerifyHello(inv, key, "lab", "support", "2201", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := inv.GetHeader(voice.HeaderCaller).Value(); got != "100" {
		t.Fatalf("X-Hello-Caller = %q, want the presented extension", got)
	}
	if got := inv.GetHeader(voice.HeaderCalled).Value(); got != "8500" {
		t.Fatalf("X-Hello-Called = %q, want the dialled number", got)
	}
	if got := inv.GetHeader(voice.HeaderCallerOrigin).Value(); got != voice.OriginExtension {
		t.Fatalf("caller origin = %q, want extension", got)
	}
	pts, ptime, ok, err := sipua.OfferAudio(inv)
	if err != nil || !ok {
		t.Fatalf("offer: %v (offered %v)", err, ok)
	}
	if len(pts) != 1 || pts[0] != "0" {
		t.Fatalf("offered %v, want PCMU only (the caller's telephone-event is not G.711)", pts)
	}
	if ptime != "20" {
		t.Fatalf("ptime = %q, want 20", ptime)
	}
	if err := in.Answer([]byte(agentSDP)); err != nil {
		t.Fatal(err)
	}
	if got := pbx.metric(t, "hello_voice_active_calls", map[string]string{"agent": "support"}); got != 1 {
		t.Fatalf("active calls = %v, want 1 while the call is up", got)
	}
	correlation := inv.GetHeader(voice.HeaderCorrelation).Value()
	hangup(t, r.dcs)
	waitEnded(t, in)
	cd := pbx.nextCDR(t)
	if got := pbx.metric(t, "hello_voice_active_calls", map[string]string{"agent": "support"}); got != 0 {
		t.Fatalf("active calls = %v after the call, want 0", got)
	}
	if got := histCount(t, pbx, "hello_voice_call_setup_seconds"); got != 1 {
		t.Fatalf("setup observations = %v, want 1", got)
	}
	if cd.VoiceAgent != "support" {
		t.Fatalf("CDR names agent %q, want support", cd.VoiceAgent)
	}
	if cd.CorrelationID != correlation {
		t.Fatalf("CDR correlation %q, want the header's %q", cd.CorrelationID, correlation)
	}
	wantTrace(t, cd.Trace, "Call established")
}

// TestVoiceAgentInviteFromTrunk fails if a trunk call to an agent keeps any
// X-Hello header the carrier planted, or does not present the trunk as the
// caller origin (S-36: a trunk caller is never verified by number).
func TestVoiceAgentInviteFromTrunk(t *testing.T) {
	pbx, ag, r := startVoicePBX(t)
	from := newPhone(t, pbx, "carrier", "")
	r.setSource(mustAddr(t, from.addr), 7)
	req := carrierInvite(from, pbx, "+97140000100")
	for name, value := range map[string]string{
		voice.HeaderTenant: "evil", voice.HeaderAgent: "evil", voice.HeaderCorrelation: "evil",
		voice.HeaderCaller: "evil", voice.HeaderCalled: "evil", voice.HeaderCallerOrigin: voice.OriginExtension,
		voice.HeaderTs: "123", voice.HeaderAuth: "v1:evil:bad",
	} {
		req.AppendHeader(sip.NewHeader(name, value))
	}
	inCh := agentAnswer(t, ag)
	dcs, err := from.dua.WriteInvite(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("trunk call to the agent: %v", err)
	}
	_ = dcs.Ack(t.Context())
	in := <-inCh
	key := voiceTestKey
	if err := sipua.VerifyHello(in.Request, key, "lab", "support", "2201", time.Now()); err != nil {
		t.Fatalf("planted header survived: %v", err)
	}
	if got := in.Request.GetHeader(voice.HeaderCallerOrigin).Value(); got != voice.OriginTrunk {
		t.Fatalf("caller origin = %q, want trunk", got)
	}
	hangup(t, dcs)
	<-in.Ended()
	cd := pbx.nextCDR(t)
	if cd.Direction != cdr.DirectionInbound || cd.VoiceAgent != "support" {
		t.Fatalf("CDR = %+v", cd)
	}
}

// TestVoiceAgentResponses fails if the agent's 404, 403, 486, 503 and
// silence are not each mapped to the failure path with the right result and
// metric reason (S-15, S-32).
func TestVoiceAgentResponses(t *testing.T) {
	cases := []struct {
		name       string
		reject     func(*sipua.Incoming) error
		callerCode int
		result     string
		reason     string
		metric     string
		trace      string
	}{
		{name: "404 unknown persona", reject: func(i *sipua.Incoming) error { return i.Reject(404, "Not Found") },
			callerCode: 480, result: ResultUnavailable, reason: "Temporarily Unavailable", metric: "404", trace: "404 Not Found"},
		{name: "403 rejected", reject: func(i *sipua.Incoming) error { return i.Reject(403, "Forbidden") },
			callerCode: 480, result: ResultUnavailable, reason: "Temporarily Unavailable", metric: "403", trace: "403 Forbidden"},
		{name: "486 full", reject: func(i *sipua.Incoming) error { return i.Reject(486, "Busy Here") },
			callerCode: 486, result: ResultBusy, reason: "Busy Here", metric: "486", trace: "486 Busy Here"},
		{name: "503 down", reject: func(i *sipua.Incoming) error { return i.Reject(503, "Service Unavailable") },
			callerCode: 480, result: ResultUnavailable, reason: "Temporarily Unavailable", metric: "503", trace: "503 Service Unavailable"},
		{name: "silence", reject: func(*sipua.Incoming) error { return nil },
			callerCode: 480, result: ResultUnavailable, reason: "Temporarily Unavailable", metric: "timeout", trace: "408 "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pbx, ag, _ := startVoicePBX(t)
			caller := newPhone(t, pbx, "a1", "pa")
			caller.register(t)
			go func() {
				if in := agentNext(t, ag); tc.reject != nil {
					_ = tc.reject(in)
				}
			}()
			r := dialAgent(t, caller, "8500")
			if code := responseCode(r.err); code != tc.callerCode {
				t.Fatalf("caller got %v, want %d", r.err, tc.callerCode)
			}
			if got := pbx.metric(t, "hello_voice_agent_unreachable_total", map[string]string{"reason": tc.metric}); got != 1 {
				t.Fatalf("unreachable[%s] = %v, want 1", tc.metric, got)
			}
			cd := pbx.nextCDR(t)
			if cd.VoiceAgent != "support" || cd.FinalStatus != tc.callerCode || cd.FailureReason != tc.reason {
				t.Fatalf("CDR = %+v, want agent support, %d, %q", cd, tc.callerCode, tc.reason)
			}
			switch tc.result {
			case ResultBusy:
				if cd.TerminationSide != cdr.SideCallee {
					t.Fatalf("busy CDR = %+v", cd)
				}
			}
			wantTrace(t, cd.Trace, "Voice agent support -> "+tc.trace)
		})
	}
}

// TestVoiceAgentNoG711 fails if a caller whose offer has neither PCMU nor
// PCMA gets anything but 488 and no INVITE goes to the agent (S-15).
func TestVoiceAgentNoG711(t *testing.T) {
	pbx, ag, r := startVoicePBX(t)
	from := newPhone(t, pbx, "carrier", "")
	r.setSource(mustAddr(t, from.addr), 7)
	from.sdp = "v=0\r\no=c 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio 4000 RTP/AVP 9 101\r\na=rtpmap:9 G722/8000\r\na=rtpmap:101 telephone-event/8000\r\n"
	dcs, err := from.dua.WriteInvite(t.Context(), carrierInvite(from, pbx, "+97140000100"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); responseCode(err) != 488 {
		t.Fatalf("G.722-only caller = %v, want 488", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, err := ag.Next(ctx); err == nil {
		t.Fatal("the agent was offered a call Hello must refuse")
	}
	cd := pbx.nextCDR(t)
	if cd.FinalStatus != 488 || cd.FailureReason != "no G.711 codec in the caller's offer" {
		t.Fatalf("CDR = %+v", cd)
	}
}

// TestVoiceAgentRefusedAddress fails if an INVITE or REGISTER that
// originates from the voice agent's address is accepted as a caller (S-14).
func TestVoiceAgentRefusedAddress(t *testing.T) {
	pbx, ag, _ := startVoicePBX(t)
	// INVITE: the fake agent places a call like a phone would.
	out, err := ag.Dial(t.Context(), "100", []byte(agentSDP))
	if err != nil {
		t.Fatalf("dial from the agent: %v", err)
	}
	if out.Status != 403 {
		t.Fatalf("INVITE from the agent's address = %d, want 403", out.Status)
	}
	// REGISTER from the same address.
	if res, err := ag.Register(t.Context(), time.Minute); err != nil || res.StatusCode != 403 {
		t.Fatalf("REGISTER from the agent's address = %d, %v; want 403", res.StatusCode, err)
	}
	// And the same source is still a fine callee: a call to the agent works.
	caller := newPhone(t, pbx, "a1", "pa")
	caller.register(t)
	inCh := agentAnswer(t, ag)
	r := dialAgent(t, caller, "8500")
	if r.err != nil {
		t.Fatalf("call to the agent: %v", r.err)
	}
	in := <-inCh
	hangup(t, r.dcs)
	waitEnded(t, in)
	pbx.nextCDR(t)
}

// TestVoiceAgentDisabled fails if a call that reaches the leg while
// HELLO_VOICE_SIP_ADDRESS is unset is not refused (S-17 guard).
func TestVoiceAgentDisabled(t *testing.T) {
	pbx, ag, _ := startVoicePBX(t, func(c *Config, _ *Deps) { c.Voice = VoiceConfig{} })
	caller := newPhone(t, pbx, "a1", "pa")
	caller.register(t)
	r := dialAgent(t, caller, "8500")
	if code := responseCode(r.err); code != 503 {
		t.Fatalf("call with voice disabled = %v, want 503", r.err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, err := ag.Next(ctx); err == nil {
		t.Fatal("the agent was offered a call while voice agents are disabled")
	}
	pbx.nextCDR(t)
}

// TestVoiceCapacity fails if an INVITE is sent after the agent's
// max_concurrent or HELLO_VOICE_MAX_CALLS is reached, if the busy call is
// not treated as a 486, or if the count does not drop when a call ends
// (S-35).
func TestVoiceCapacity(t *testing.T) {
	pbx, ag, _ := startVoicePBX(t,
		withVoiceSlots(newFakeVoiceSlots()),
		withVoiceLimits(mapLimits{"support": 1, "reception": 5}),
		func(c *Config, _ *Deps) { c.Voice.MaxCalls = 1 })
	first := newPhone(t, pbx, "a1", "pa")
	second := newPhone(t, pbx, "b1", "pb1")
	first.register(t)
	second.register(t)

	inCh := agentAnswer(t, ag)
	r1 := dialAgent(t, first, "8500")
	if r1.err != nil {
		t.Fatalf("first call: %v", r1.err)
	}
	in1 := <-inCh

	// The agent's cap: no second INVITE, and the caller is treated as if
	// the agent had answered 486.
	r2 := dialAgent(t, second, "8500")
	if code := responseCode(r2.err); code != 486 {
		t.Fatalf("call over max_concurrent = %v, want 486", r2.err)
	}
	cd := pbx.nextCDR(t)
	if cd.VoiceAgent != "support" || cd.FailureReason != "Busy Here" {
		t.Fatalf("capacity CDR = %+v", cd)
	}
	if got := pbx.metric(t, "hello_voice_agent_unreachable_total", map[string]string{"reason": "capacity"}); got != 1 {
		t.Fatalf("unreachable[capacity] = %v, want 1", got)
	}

	// The count drops when the call ends: the next call connects.
	hangup(t, r1.dcs)
	waitEnded(t, in1)
	pbx.nextCDR(t) // the first call's CDR
	inCh = agentAnswer(t, ag)
	r3 := dialAgent(t, first, "8500")
	if r3.err != nil {
		t.Fatalf("call after the slot was freed: %v", r3.err)
	}
	in3 := <-inCh

	// The total cap: a second agent with free per-agent slots still refuses
	// while the one total slot is held.
	r4 := dialAgent(t, second, "8600")
	if code := responseCode(r4.err); code != 486 {
		t.Fatalf("call over HELLO_VOICE_MAX_CALLS = %v, want 486", r4.err)
	}
	cd = pbx.nextCDR(t)
	if cd.VoiceAgent != "reception" {
		t.Fatalf("total-cap CDR names agent %q, want reception", cd.VoiceAgent)
	}
	if got := pbx.metric(t, "hello_voice_agent_unreachable_total", map[string]string{"reason": "capacity"}); got != 2 {
		t.Fatalf("unreachable[capacity] = %v, want 2", got)
	}
	hangup(t, r3.dcs)
	waitEnded(t, in3)
}

// TestVoiceStateDownRefuses fails if the leg sends an uncounted INVITE when
// the shared voice state is unreachable.
func TestVoiceStateDownRefuses(t *testing.T) {
	fs := newFakeVoiceSlots()
	fs.down = true
	pbx, ag, _ := startVoicePBX(t, withVoiceSlots(fs), withVoiceLimits(mapLimits{"support": 1}))
	caller := newPhone(t, pbx, "a1", "pa")
	caller.register(t)
	r := dialAgent(t, caller, "8500")
	if code := responseCode(r.err); code != 503 {
		t.Fatalf("call with the voice state down = %v, want 503", r.err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if _, err := ag.Next(ctx); err == nil {
		t.Fatal("the agent was offered an uncounted call")
	}
	pbx.nextCDR(t)
}

func withVoiceSlots(fs *fakeVoiceSlots) pbxOpt {
	return func(_ *Config, d *Deps) { d.VoiceSlots = fs }
}

func withVoiceLimits(l mapLimits) pbxOpt {
	return func(_ *Config, d *Deps) { d.VoiceLimits = l }
}

// TestVoiceCallAuth proves the SIP half of the X-Hello contract (S-16): the
// INVITE's signature verifies with a key shared through the vector file, a
// field change or a tampered signature breaks it, both keys of a rotation
// verify, Hello's Sign reproduces every shared vector, and neither the
// secret nor the signature reaches a log line.
func TestVoiceCallAuth(t *testing.T) {
	var vec struct {
		Keys []struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		} `json:"keys"`
		Vectors []struct {
			Tenant      string `json:"tenant"`
			Agent       string `json:"agent"`
			SIPUser     string `json:"sipUser"`
			Correlation string `json:"correlation"`
			Ts          int64  `json:"ts"`
			KeyID       string `json:"keyId"`
			AuthHeader  string `json:"authHeader"`
		} `json:"vectors"`
	}
	raw, err := os.ReadFile("../../internal/voice/testdata/voice_auth_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &vec); err != nil {
		t.Fatal(err)
	}
	keyByID := map[string]voice.Key{}
	for _, k := range vec.Keys {
		keyByID[k.ID] = voice.NewKey(k.Secret)
		if keyByID[k.ID].ID != k.ID {
			t.Fatalf("vector key id derivation changed: got %s, want %s", keyByID[k.ID].ID, k.ID)
		}
	}
	// Hello's Sign reproduces every shared vector, whichever key signs.
	for i, v := range vec.Vectors {
		key, ok := keyByID[v.KeyID]
		if !ok {
			t.Fatalf("vector %d names unknown key %s", i, v.KeyID)
		}
		got := voice.Sign(key, v.Tenant, v.Agent, v.SIPUser, v.Correlation, time.Unix(v.Ts, 0))
		if got != v.AuthHeader {
			t.Fatalf("vector %d: Sign = %s, want %s", i, got, v.AuthHeader)
		}
	}

	// The INVITE the leg sends verifies as the agent will verify it.
	pbx, ag, _ := startVoicePBX(t, func(c *Config, _ *Deps) { c.Voice.Key = voice.NewKey(vec.Keys[0].Secret) })
	caller := newPhone(t, pbx, "a1", "pa")
	caller.register(t)
	inCh := agentAnswer(t, ag)
	r := dialAgent(t, caller, "8500")
	if r.err != nil {
		t.Fatalf("call to the agent: %v", r.err)
	}
	in := <-inCh
	now := time.Now()
	key0, key1 := keyByID[vec.Keys[0].ID], keyByID[vec.Keys[1].ID]
	if err := sipua.VerifyHello(in.Request, key0, "lab", "support", "2201", now); err != nil {
		t.Fatal(err)
	}
	// A field change breaks the signature.
	if err := sipua.VerifyHello(in.Request, key0, "lab", "someone-else", "2201", now); err == nil {
		t.Fatal("a signature over other fields verified")
	}
	// Rotation: a signature made with the secondary key verifies against
	// both, and a tampered one does not verify at all.
	both := []voice.Key{key0, key1}
	hdr := voice.Sign(key1, "lab", "support", "2201", "corr-rotation", now)
	if err := voice.Verify(both, hdr, now, "lab", "support", "2201", "corr-rotation", now); err != nil {
		t.Fatalf("secondary key refused during rotation: %v", err)
	}
	if err := voice.Verify(both, hdr, now, "lab", "support", "2201", "corr-other", now); !errors.Is(err, voice.ErrBadSignature) {
		t.Fatalf("tampered correlation = %v, want ErrBadSignature", err)
	}
	wrong := "v1:" + key0.ID + ":" + strings.Repeat("ab", 32)
	if err := voice.Verify(both, wrong, now, "lab", "support", "2201", "x", now); !errors.Is(err, voice.ErrBadSignature) {
		t.Fatalf("wrong signature = %v, want ErrBadSignature", err)
	}
	hangup(t, r.dcs)
	waitEnded(t, in)
	pbx.nextCDR(t)
	// Neither the secret nor a signature survives the log redaction.
	signed := voice.Sign(key0, "lab", "support", "2201", "corr-log", now)
	line := "INVITE sip:2201@agent SIP/2.0\r\nX-Hello-Auth: " + signed + "\r\n\r\n"
	if out := RedactSIP(line); strings.Contains(out, signed) || strings.Contains(out, "v1:") {
		t.Fatalf("signature not redacted from logs: %q", out)
	}
	if out := RedactSIP("X-Hello-Auth: " + vec.Keys[0].Secret); strings.Contains(out, vec.Keys[0].Secret) {
		t.Fatalf("secret not redacted from logs: %q", out)
	}
}
