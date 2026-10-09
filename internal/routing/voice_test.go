package routing

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// traceTexts flattens a Trace to its step texts.
func traceTexts(tr Trace) []string {
	out := make([]string, len(tr))
	for i, s := range tr {
		out[i] = s.Text
	}
	return out
}

// voiceFixture is the fixture configuration with an enabled agent, a
// disabled agent, an agent extension and an inbound route to the agent.
func voiceFixture(t testing.TB) Config {
	cfg := fixtureConfig()
	cfg.VoiceSIPAddress = fixtureVoiceAddr
	cfg.VoiceAgents = []VoiceAgent{
		{Name: vaSupport, SIPUser: supportSIPUser, Enabled: true},
		{Name: vaAfterHours, SIPUser: "00000002", Enabled: false},
	}
	cfg.VoiceAgentExtensions = map[string]string{"700": vaSupport}
	cfg.Inbound = append(cfg.Inbound, InboundRoute{
		ID: 11, Position: 25, Name: "Support agent", DIDKind: "exact", DID: "97142221000",
		DestinationKind: "voice_agent", Destination: vaSupport, Enabled: true,
	})
	return cfg
}

const (
	// The fixture's talking-agent address, and vaSupport's generated sip
	// user (the Request-URI user Hello sends).
	fixtureVoiceAddr = "192.0.2.10:5060"
	supportSIPUser   = "00000001"
	// The fixture's agent names.
	vaSupport    = "support"     // enabled
	vaAfterHours = "after-hours" // disabled
)

func TestVoiceAgentRouting(t *testing.T) {
	cfg := voiceFixture(t)
	tbl := mustCompile(t, cfg)

	t.Run("decide inbound", func(t *testing.T) {
		d := tbl.Decide(Call{FromTrunk: tPrimary, Number: "97142221000", CallerID: "+971501234567", At: voiceAt(t)}, nil)
		if d.Kind != KindInbound || d.SIPURI != "sip:"+supportSIPUser+"@"+fixtureVoiceAddr {
			t.Fatalf("decision = %v %q, want inbound with the agent's sip_user URI", d.Kind, d.SIPURI)
		}
		if d.VoiceAgent == nil || *d.VoiceAgent != (VoiceRef{Name: vaSupport, SIPUser: supportSIPUser}) {
			t.Fatalf("VoiceAgent = %v, want support/%s", d.VoiceAgent, supportSIPUser)
		}
		trace := traceTexts(d.Trace)
		want := `Destination: voice agent "support" (sip:00000001@192.0.2.10:5060)`
		if !slices.Contains(trace, want) {
			t.Fatalf("trace = %v, want step %q", trace, want)
		}
	})

	t.Run("compile validation", func(t *testing.T) {
		cases := []struct {
			name string
			mut  func(*Config)
			want string
		}{
			{"missing", func(c *Config) { c.Inbound[10].Destination = "no-such-agent" },
				`inbound[10].destination: voice agent "no-such-agent" does not exist`},
			{"disabled", func(c *Config) { c.Inbound[10].Destination = vaAfterHours },
				`inbound[10].destination: voice agent "after-hours" is disabled`},
			{"unconfigured", func(c *Config) { c.VoiceSIPAddress = "" },
				"inbound[10].destination: " + voiceNotConfigured},
			{"duplicate agent", func(c *Config) {
				c.VoiceAgents = append(c.VoiceAgents, VoiceAgent{Name: vaSupport, SIPUser: "00000009", Enabled: true})
			}, `voiceAgents[2].name: duplicate voice agent name "support"`},
			{"extension clash", func(c *Config) { c.VoiceAgentExtensions["101"] = vaSupport },
				`voiceAgents[?].extension: extension "101" is both a voice agent's and an extension-table number`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				c := voiceFixture(t)
				tc.mut(&c)
				tbl, errs := Compile(c)
				if tbl != nil || len(errs) != 1 || errs[0].Path+": "+errs[0].Message != tc.want {
					t.Fatalf("Compile = %v, %v; want nil, one error %q", tbl != nil, errs, tc.want)
				}
			})
		}
	})

	t.Run("extension dial resolves to the agent", func(t *testing.T) {
		d := tbl.Decide(Call{FromExtension: "101", Number: "700", At: voiceAt(t)}, nil)
		if d.Kind != KindInbound || d.SIPURI != "sip:"+supportSIPUser+"@"+fixtureVoiceAddr || d.Extension != "" {
			t.Fatalf("dialling 700 = %v %q ext %q, want the agent's inbound decision", d.Kind, d.SIPURI, d.Extension)
		}
		if d.VoiceAgent == nil || d.VoiceAgent.Name != vaSupport {
			t.Fatalf("VoiceAgent = %v, want support", d.VoiceAgent)
		}
		if !slices.Contains(traceTexts(d.Trace), `Internal extension lookup "700" -> voice agent "support" (sip:00000001@192.0.2.10:5060)`) {
			t.Fatalf("trace = %v, want the voice agent lookup step", traceTexts(d.Trace))
		}
	})

	t.Run("schedule and trunk filters still apply", func(t *testing.T) {
		// A schedule-closed and a trunk-restricted route to the agent both
		// skip, and the DID still reaches the agent by the later route.
		c := voiceFixture(t)
		c.Inbound = append(c.Inbound,
			InboundRoute{ID: 12, Position: 5, Name: "Agent office hours", DIDKind: "exact", DID: "97142221000",
				Schedule:        &Schedule{TimeZone: "Asia/Dubai", Windows: []Window{{Days: []time.Weekday{time.Saturday}, Start: "09:00", End: "17:00"}}},
				DestinationKind: "voice_agent", Destination: vaSupport, Enabled: true},
			InboundRoute{ID: 13, Position: 6, Name: "Agent on primary only", DIDKind: "exact", DID: "97142221000",
				TrunkID:         tPrimary,
				DestinationKind: "voice_agent", Destination: vaSupport, Enabled: true})
		tbl := mustCompile(t, c)
		d := tbl.Decide(Call{FromTrunk: tBackup, Number: "97142221000", At: voiceAt(t)}, nil)
		trace := traceTexts(d.Trace)
		for _, want := range []string{
			`Route "Agent office hours" skipped: schedule closed (`,
			`Route "Agent on primary only" skipped: only for trunk 1, call is from trunk carrier-backup`,
		} {
			found := false
			for _, got := range trace {
				if strings.HasPrefix(got, want) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("trace = %v, want a step %q...", trace, want)
			}
		}
		if last := trace[len(trace)-1]; !strings.HasPrefix(last, `Destination: voice agent "support" (sip:`) {
			t.Fatalf("last trace step = %q, want the agent destination", last)
		}
	})
}

// TestVoiceDisabledWithoutAddress is the routing half of S-17's story (the
// config half is internal/config's): a route to an agent does not compile
// without HELLO_VOICE_SIP_ADDRESS, and dialling an agent's extension is
// refused instead of sent to a dead address.
func TestVoiceDisabledWithoutAddress(t *testing.T) {
	c := voiceFixture(t)
	c.VoiceSIPAddress = ""
	tbl, errs := Compile(c)
	if tbl != nil || len(errs) != 1 || errs[0].Path != "inbound[10].destination" || errs[0].Message != voiceNotConfigured {
		t.Fatalf("Compile = %v, %v; want nil, one voice_not_configured error", tbl != nil, errs)
	}
	// The extension dial decides at run time and is refused.
	noRoute := voiceFixture(t)
	noRoute.Inbound = noRoute.Inbound[:len(noRoute.Inbound)-1]
	noRoute.VoiceSIPAddress = ""
	d := mustCompile(t, noRoute).Decide(Call{FromExtension: "101", Number: "700", At: voiceAt(t)}, nil)
	if d.Kind != KindReject || d.RejectCode != 503 || !strings.Contains(d.Reason, "not configured") {
		t.Fatalf("extension dial without an address = %v %d %q, want reject 503 not configured", d.Kind, d.RejectCode, d.Reason)
	}
}

// voiceAt is the fixture's Monday 10:00, when every schedule is open.
func voiceAt(t testing.TB) time.Time {
	t.Helper()
	return at(t, "2026-10-05 10:00")
}
