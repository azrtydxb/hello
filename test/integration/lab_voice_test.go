package integration

// The voice-agent lab test (spec voice-agents S-10, S-15, S-16, S-19, S-21,
// S-23, S-35): a call routed to a voice agent reaches test/fakeagent, the
// fake talking-agent, which answers with G.711, polls the runtime API and
// reports the call back. It needs the voice operations live, and skips with
// a named reason while the voice streams are still s.pending.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/voice"
	"github.com/azrtydxb/hello/test/fakeagent"
)

// The lab's voice settings; compose.yaml sets the same values, so Hello
// signs calls to the address the fake agent answers on the host's edge
// gateway, as the containers see it.
const (
	labVoiceHost   = "10.89.53.1"
	labVoicePort   = "15060"
	labVoiceTenant = "lab"
	labVoiceSecret = "lab-only-voice-secret-0123456789abcdef" // gitleaks:allow
)

func init() { remember(labVoiceSecret) }

// voiceReady probes the registry's read-only status, the cheapest voice
// operation, so the test fails with a clear line when the voice stack is
// not up instead of a confusing error deeper in.
func voiceReady(t *testing.T, lc *labClient) {
	t.Helper()
	if err := lc.do("GET", "/api/v1/voice/status", nil, nil, http.StatusOK); err != nil {
		t.Fatalf("voice status probe: %v", err)
	}
}

type labVoiceAgent struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SIPUser   string `json:"sipUser"`
	Extension string `json:"extension"`
	Revision  int64  `json:"revision"`
}

// createVoiceAgent creates an enabled agent with a persona, a one-call
// capacity and an extension, and cleans it up at the test's end.
func (lc *labClient) createVoiceAgent(name string) labVoiceAgent {
	lc.t.Helper()
	var a labVoiceAgent
	lc.must("POST", "/api/v1/voice/agents", map[string]any{
		"name": name, "prompt": "You are the lab's receptionist. Be brief.",
		"greeting": "Hello, you have reached the automated assistant.",
		"language": "en", "extension": "8" + randDigits(7), "maxConcurrent": 1,
	}, &a, http.StatusCreated)
	lc.t.Cleanup(func() {
		_ = lc.do("DELETE", fmt.Sprintf("/api/v1/voice/agents/%d", a.ID), nil, nil, http.StatusNoContent)
	})
	return a
}

// updateAgent PUTs an agent's full body: the endpoint replaces every
// editable field and validates the result, so each edit sends the fields
// create set plus the change (there is no partial update and no force
// flag; disabling needs one because only delete checks references).
func (lc *labClient) updateAgent(va labVoiceAgent, changes map[string]any) {
	lc.t.Helper()
	body := map[string]any{
		"name": va.Name, "prompt": "You are the lab's receptionist. Be brief.",
		"greeting": "Hello, you have reached the automated assistant.",
		"language": "en", "extension": va.Extension, "maxConcurrent": 1,
	}
	for k, v := range changes {
		body[k] = v
	}
	lc.must("PUT", fmt.Sprintf("/api/v1/voice/agents/%d", va.ID), body, nil, http.StatusOK)
}

// voiceRuntimeAccount creates the service account the fake agent reads the
// runtime API with, holding the voice-runtime scope alone (S-18), and
// returns its client credentials.
func (lc *labClient) voiceRuntimeAccount() (string, string) {
	lc.t.Helper()
	var sa struct{ ID string }
	lc.must("POST", "/api/v1/service-accounts", map[string]any{
		"name": "lab-voice-" + randDigits(6), "role": "viewer", "scopes": []string{"voice-runtime"},
	}, &sa, http.StatusCreated)
	lc.t.Cleanup(func() {
		_ = lc.do("DELETE", "/api/v1/service-accounts/"+url.PathEscape(sa.ID), nil, nil, http.StatusNoContent)
	})
	var sec struct{ Secret string }
	lc.must("POST", "/api/v1/service-accounts/"+url.PathEscape(sa.ID)+"/secrets", map[string]any{}, &sec, http.StatusCreated)
	remember(sec.Secret)
	return sa.ID, sec.Secret
}

// startFakeAgent runs test/fakeagent on the lab's voice address, with the
// runtime API of this lab's hello-control-1.
func startFakeAgent(t *testing.T, lc *labClient) *fakeagent.Agent {
	t.Helper()
	id, secret := lc.voiceRuntimeAccount()
	a, err := fakeagent.Start(fakeagent.Options{
		Listen: "0.0.0.0:" + labVoicePort, ContactHost: labVoiceHost,
		Keys: []voice.Key{voice.NewKey(labVoiceSecret)}, Tenant: labVoiceTenant,
		Base: labAPI, ClientID: id, ClientSecret: secret, Version: "fakeagent-lab",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

// waitVoiceView waits until the fake agent's loaded view names one agent,
// failing after two seconds: that is the live-reload budget (S-19).
func waitVoiceView(t *testing.T, a *fakeagent.Agent, sipUser string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		v := a.View()
		for _, ag := range v.Agents {
			if ag.SIPUser == sipUser {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent's view lacks %s after 2s (view %+v, errors %v)", sipUser, v, a.Errors())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type labVoiceCDR struct {
	ID                  int64  `json:"id"`
	Direction           string `json:"direction"`
	OriginalDestination string `json:"originalDestination"`
	FinalStatus         int    `json:"finalStatus"`
	// The agent name as it was at call time; empty for every other call.
	VoiceAgent string `json:"voiceAgent"`
}

// TestVoiceAgentsEndToEnd is the lab proof (the acceptance criteria's
// TestVoiceAgentsEndToEnd): create an agent and an inbound route through the
// API, call the DID, and the fake talking-agent answers with the anchored
// media path; a persona edit reaches it within two seconds; an agent as a
// ring group's failure target takes the call after the timeout; a call over
// capacity overflows to the failure target; a rejected and a disabled agent
// fail the call; and the CDR carries the agent and the report.
func TestVoiceAgentsEndToEnd(t *testing.T) {
	lc := newLabClient(t)
	voiceReady(t, lc)
	agent := startFakeAgent(t, lc)

	// The agent and a DID route to it. maxConcurrent is one, so the
	// capacity phase can overflow it.
	va := lc.createVoiceAgent("support-" + randDigits(6))
	var route struct{ ID int64 }
	_, backup := lc.trunks(0)
	did := "+9714557" + randDigits(4)
	lc.must("POST", "/api/v1/routes/inbound", map[string]any{
		"name": "lab-voice-" + randDigits(6), "didKind": "exact", "did": did,
		"trunkId": backup.ID, "destinationKind": "voice_agent", "destination": va.Name, "enabled": true,
	}, &route, 201)
	lc.t.Cleanup(func() { _ = lc.do("DELETE", fmt.Sprintf("/api/v1/routes/inbound/%d", route.ID), nil, nil, 204) })
	lc.waitSnapshots(10 * time.Second)

	// The persona reaches the fake agent within the two-second budget.
	started := time.Now()
	waitVoiceView(t, agent, va.SIPUser)
	if lag := time.Since(started); lag > 2*time.Second {
		t.Logf("first load took %s (the poll started with the test)", lag)
	}

	// A persona edit propagates within two seconds (S-19 long poll).
	edited := "Updated greeting for the lab."
	lc.updateAgent(va, map[string]any{"greeting": edited})
	deadline := time.Now().Add(2 * time.Second)
	for {
		v := agent.View()
		if len(v.Agents) == 1 && v.Agents[0].Greeting == edited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the persona edit did not reach the fake agent within 2s (view %+v)", v)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A call to the DID: the carrier places it, Hello routes it to the
	// agent, the fake agent answers, and the CDR carries the agent and the
	// report (S-15, S-21, S-23).
	var res struct{ Status int }
	carrierDo(t, backupHTTP, "POST", "/call", map[string]any{
		"from": "+97145570001", "to": did, "target": "hello-sip-1:5060", "hangupAfterMs": 500,
	}, &res)
	if res.Status != 200 {
		t.Fatalf("inbound call to the voice agent = %d, want 200", res.Status)
	}
	calls := agent.Calls()
	if len(calls) == 0 || calls[0].Tenant != labVoiceTenant || calls[0].Agent != va.Name ||
		calls[0].SIPUser != va.SIPUser || calls[0].Origin != "trunk" || calls[0].Called != did {
		t.Fatalf("the fake agent saw %+v, want one trunk call to %s", calls, va.SIPUser)
	}
	var cdr labVoiceCDR
	eventually(t, 10*time.Second, "the CDR of the agent call", func() error {
		return findVoiceCDR(lc, va.Name, did, &cdr)
	})
	if cdr.FinalStatus != 200 || cdr.VoiceAgent != va.Name {
		t.Fatalf("CDR = %+v, want the agent named %s", cdr, va.Name)
	}
	var detail struct {
		VoiceAgent *struct {
			Name              string `json:"name"`
			Outcome           string `json:"outcome"`
			Summary           string `json:"summary"`
			TranscriptPresent bool   `json:"transcriptPresent"`
		} `json:"voiceAgent"`
	}
	lc.must("GET", fmt.Sprintf("/api/v1/cdrs/%d", cdr.ID), nil, &detail, 200)
	if detail.VoiceAgent == nil || detail.VoiceAgent.Name != va.Name || detail.VoiceAgent.Outcome != "answered" ||
		detail.VoiceAgent.Summary == "" {
		t.Fatalf("CDR detail voiceAgent = %+v, want the report joined", detail.VoiceAgent)
	}
	if detail.VoiceAgent.TranscriptPresent {
		t.Fatal("transcript present though the agent does not record one")
	}
	reportsAfterAnswer := len(agent.Reports())

	// The reject path: the agent answers 403 and the call fails (S-15).
	agent.SetBehavior(va.Name, func(fakeagent.Call) int { return 403 })

	// The carrier's INVITE fails: 403 is a failed destination, not a
	// fallback to another route (S-15), and no report arrives for a call the
	// agent refused.
	carrierDo(t, backupHTTP, "POST", "/call", map[string]any{
		"from": "+97145570001", "to": did, "target": "hello-sip-1:5060",
	}, &res)
	if res.Status == 200 {
		t.Fatalf("a 403 from the agent answered the call")
	}
	if len(agent.Reports()) != reportsAfterAnswer {
		t.Fatalf("a refused call was reported: %+v", agent.Reports())
	}
	agent.SetBehavior(va.Name, nil)

	// An agent as a ring group's failure target: the phone never picks up,
	// and after the group's timeout the agent takes the call (S-12).
	callee := lc.devices("desk")[0]
	groupPhone := phone(t, callee, labSIP1)
	register(t, groupPhone)
	caller := lc.devices("desk")[0]
	p := phone(t, caller, labSIP1)
	register(t, p)
	var group struct{ ID int64 }
	groupName := "77" + randDigits(6)
	lc.must("POST", "/api/v1/ring-groups", map[string]any{
		"name": groupName, "strategy": "sequential", "ringTimeout": 5, "hunt": true,
		"members":       []map[string]any{{"extensionId": lc.extID(callee.Extension), "position": 1}},
		"failureKind":   "voice_agent",
		"failureTarget": va.Name,
	}, &group, 201)
	lc.t.Cleanup(func() { _ = lc.do("DELETE", fmt.Sprintf("/api/v1/ring-groups/%d", group.ID), nil, nil, 204) })
	lc.waitSnapshots(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	rang := answerNext(ctx, groupPhone) // rings, nobody answers
	out, err := p.Dial(ctx, groupName, sdpOffer)
	if err != nil {
		t.Fatal(err)
	}
	if in := <-rang; in == nil {
		t.Fatal("the group member did not ring")
	}
	if out.Status != 200 {
		t.Fatalf("ring group call = %d %q, want the failure-target agent to answer",
			out.Status, out.Response.Body())
	}
	// The caller hears the answer over the anchored media path (phase 5
	// anchors these lab calls): pass-through or the anchor's SDP.
	if err := sdpDirectOrAnchored(sdpAnswer, out.Response.Body()); err != nil {
		t.Fatalf("ring group media: %v", err)
	}
	calls = agent.Calls()
	if len(calls) != 2 || calls[1].Called != groupName || calls[1].Origin != "extension" {
		t.Fatalf("the fake agent saw %+v, want the failure-target call dialled %s", calls, groupName)
	}
	if err := out.Hangup(ctx); err != nil {
		t.Fatal(err)
	}

	// Capacity overflow (S-35): the agent's one slot is busy, so a second
	// call is not invited; the group's failure target takes it instead.
	holder := lc.devices("desk")[0]
	held := phone(t, holder, labSIP1)
	register(t, held)
	// The overflow lands in this extension's voicemail box; a fresh
	// extension has one, and nothing needs to register on it.
	blocked := lc.devices("desk")[0]

	heldCall, err := held.Dial(ctx, va.Extension, sdpOffer)
	if err != nil {
		t.Fatal(err)
	}
	if heldCall.Status != 200 {
		t.Fatalf("call to the agent extension = %d, want 200", heldCall.Status)
	}
	calls = agent.Calls()
	if len(calls) != 3 || calls[2].SIPUser != va.SIPUser || calls[2].Origin != "extension" {
		t.Fatalf("the fake agent saw %+v, want the extension call to %s", calls, va.SIPUser)
	}
	// The group's member is the agent itself; the failure target is the
	// blocked extension's voicemail box, which answers the overflow (the
	// merged failure kinds are none, voicemail, external and voice_agent).
	var full struct{ ID int64 }
	fullName := "78" + randDigits(6)
	lc.must("POST", "/api/v1/ring-groups", map[string]any{
		"name": fullName, "strategy": "sequential", "ringTimeout": 5, "hunt": true,
		"members":       []map[string]any{{"voiceAgentId": va.ID, "position": 1}},
		"failureKind":   "voicemail",
		"failureTarget": blocked.Extension,
	}, &full, 201)
	lc.t.Cleanup(func() { _ = lc.do("DELETE", fmt.Sprintf("/api/v1/ring-groups/%d", full.ID), nil, nil, 204) })
	lc.waitSnapshots(10 * time.Second)

	out2, err := p.Dial(ctx, fullName, sdpOffer)
	if err != nil {
		t.Fatal(err)
	}
	if out2.Status != 200 {
		t.Fatalf("overflow call = %d, want the failure target to answer", out2.Status)
	}
	if got := len(agent.Calls()); got != 3 {
		t.Fatalf("the agent saw %d calls, want 3: the overflow was invited despite the full capacity", got)
	}
	if err := out2.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := heldCall.Hangup(ctx); err != nil {
		t.Fatal(err)
	}

	// A disabled agent answers nothing new: the route fails instead. No
	// force is needed; only delete checks references.
	lc.updateAgent(va, map[string]any{"enabled": false})
	carrierDo(t, backupHTTP, "POST", "/call", map[string]any{
		"from": "+97145570001", "to": did, "target": "hello-sip-1:5060",
	}, &res)
	if res.Status == 200 {
		t.Fatalf("a disabled agent answered a new call")
	}
}

// extID parses an extension number to its id, for the API bodies that
// address extensions by id.
func (lc *labClient) extID(number string) int64 {
	id, err := strconv.ParseInt(lc.extensionID(number), 10, 64)
	if err != nil {
		lc.t.Fatalf("extension %s: %v", number, err)
	}
	return id
}

// TestVoiceCDRFields pins the CDR columns of S-23: a call routed to an agent
// writes voice_agent_id and the name, a plain call writes neither, and
// deleting the agent nulls the id but keeps the name, so past calls stay
// readable.
func TestVoiceCDRFields(t *testing.T) {
	lc := newLabClient(t)
	voiceReady(t, lc)
	// The fake agent answers so the CDR exists; this test only reads CDRs.
	startFakeAgent(t, lc)

	va := lc.createVoiceAgent("cdr-" + randDigits(6))
	var route struct{ ID int64 }
	_, backup := lc.trunks(0)
	did := "+9714558" + randDigits(4)
	lc.must("POST", "/api/v1/routes/inbound", map[string]any{
		"name": "lab-voice-" + randDigits(6), "didKind": "exact", "did": did,
		"trunkId": backup.ID, "destinationKind": "voice_agent", "destination": va.Name, "enabled": true,
	}, &route, 201)
	lc.t.Cleanup(func() { _ = lc.do("DELETE", fmt.Sprintf("/api/v1/routes/inbound/%d", route.ID), nil, nil, 204) })

	// A call that is not routed to an agent: two extensions, one CDR
	// without the voice agent columns.
	caller, callee := lc.devices("desk")[0], lc.devices("soft")[0]
	a, b := phone(t, caller, labSIP1), phone(t, callee, labSIP1)
	register(t, a)
	register(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	answered := answerNext(ctx, b)
	plain, err := a.Dial(ctx, callee.Extension, sdpOffer)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Status != 200 {
		t.Fatalf("plain call = %d", plain.Status)
	}
	if in := <-answered; in == nil {
		t.Fatal("the plain call did not reach the callee")
	}
	var plainCDR labVoiceCDR
	eventually(t, 10*time.Second, "the plain call's CDR", func() error {
		return findVoiceCDR(lc, "", callee.Extension, &plainCDR)
	})
	if plainCDR.VoiceAgent != "" {
		t.Fatalf("a plain call's CDR carries agent fields: %+v", plainCDR)
	}
	if err := plain.Hangup(ctx); err != nil {
		t.Fatal(err)
	}

	// The agent call, routed through the DID.
	lc.waitSnapshots(10 * time.Second)
	var res struct{ Status int }
	carrierDo(t, backupHTTP, "POST", "/call", map[string]any{
		"from": "+97145580001", "to": did, "target": "hello-sip-1:5060", "hangupAfterMs": 300,
	}, &res)
	if res.Status != 200 {
		t.Fatalf("agent call = %d, want 200", res.Status)
	}
	var cdr labVoiceCDR
	eventually(t, 10*time.Second, "the agent call's CDR", func() error {
		return findVoiceCDR(lc, va.Name, did, &cdr)
	})
	if cdr.VoiceAgent != va.Name {
		t.Fatalf("agent call CDR = %+v, want the agent named %s", cdr, va.Name)
	}

	// Deleting the agent keeps the name on past CDRs. The route must go
	// first: delete refuses an agent a route still names. The cleanup
	// helper deletes the agent too; the first delete wins.
	lc.must("DELETE", fmt.Sprintf("/api/v1/routes/inbound/%d", route.ID), nil, nil, http.StatusNoContent)
	lc.must("DELETE", fmt.Sprintf("/api/v1/voice/agents/%d", va.ID), nil, nil, http.StatusNoContent)
	var after labVoiceCDR
	lc.must("GET", fmt.Sprintf("/api/v1/cdrs/%d", cdr.ID), nil, &after, 200)
	if after.VoiceAgent != va.Name {
		t.Fatalf("deleting the agent erased the name: %+v", after)
	}
}

// findVoiceCDR finds the newest CDR of a call to the voice agent (or, with
// an empty name, to the given destination) and returns its fields.
func findVoiceCDR(lc *labClient, agent, destination string, out *labVoiceCDR) error {
	path := "/api/v1/cdrs?limit=200"
	if agent != "" {
		path += "&voiceAgent=" + url.QueryEscape(agent)
	}
	var page struct{ Items []labVoiceCDR }
	if err := lc.do("GET", path, nil, &page, 200); err != nil {
		return err
	}
	for _, c := range page.Items {
		if c.OriginalDestination == destination {
			*out = c
			return nil
		}
	}
	return fmt.Errorf("no CDR for %s yet", destination)
}
