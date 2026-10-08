// The fake talking-agent's tests: the signature check against the shared
// vectors, the refusals, the tone and the report, and the runtime reload.
package fakeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"

	"github.com/azrtydxb/hello/internal/voice"
)

// The test constants. The secret has at least 32 bytes, as Hello's config
// validation demands.
const (
	agentSecret  = "fakeagent-test-secret-0123456789abcd" // gitleaks:allow
	agentTenant  = "lab"
	agentDomain  = "hello.test"
	agentSIPUser = "2201"
	agentName    = "reception"
	agentGreet   = "Hello, Hello here."
)

// testKey is the key the tests sign with and the agents verify with.
var testKey = voice.Key{ID: "0a1b2c3d", Secret: []byte(agentSecret)}

// tag returns fresh From/To tag params.
func tag() sip.HeaderParams {
	p := sip.NewParams()
	p.Add("tag", sip.GenerateTagN(8))
	return p
}

// sendInvite places a raw INVITE at the agent and returns the final
// response. Empty tenant, agent or corr leave that header off; auth "" sends
// no X-Hello-Auth at all.
func sendInvite(t *testing.T, a *Agent, sipUser, tenant, agent, corr string, ts time.Time, auth string) *sip.Response {
	t.Helper()
	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientNAT())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ua.Close(); _ = client.Close() }()
	host, port, _ := net.SplitHostPort(a.Addr())
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: sipUser, Host: host})
	req.SetDestination(a.Addr())
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{User: "+971500000001", Host: agentDomain}, Params: tag()})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{User: sipUser, Host: agentDomain}})
	req.AppendHeader(sip.NewHeader("Contact", "<sip:caller@"+net.JoinHostPort(host, port)+">"))
	mf := sip.MaxForwardsHeader(70)
	req.AppendHeader(&mf)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE})
	cid := sip.CallIDHeader(sip.GenerateTagN(24))
	req.AppendHeader(&cid)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody([]byte(sdpOfferText(40000)))
	req.AppendHeader(sip.NewHeader(voice.HeaderCaller, "+971500000001"))
	req.AppendHeader(sip.NewHeader(voice.HeaderCalled, "9011"))
	req.AppendHeader(sip.NewHeader(voice.HeaderCallerOrigin, "extension"))
	if tenant != "" {
		req.AppendHeader(sip.NewHeader(voice.HeaderTenant, tenant))
	}
	if agent != "" {
		req.AppendHeader(sip.NewHeader(voice.HeaderAgent, agent))
	}
	if corr != "" {
		req.AppendHeader(sip.NewHeader(voice.HeaderCorrelation, corr))
	}
	req.AppendHeader(sip.NewHeader(voice.HeaderTs, strconv.FormatInt(ts.Unix(), 10)))
	if auth != "" {
		req.AppendHeader(sip.NewHeader(voice.HeaderAuth, auth))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := client.Do(ctx, req)
	if err != nil {
		t.Fatalf("INVITE %s: %v", corr, err)
	}
	return res
}

// sdpOfferText is a G.711 offer advertising port at the given address.
func sdpOfferText(port int) string {
	return fmt.Sprintf("v=0\r\no=c 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\n"+
		"t=0 0\r\nm=audio %d RTP/AVP 0 8\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\n", port)
}

// runtimeServer is a fake Hello runtime API over the fixed contract: the
// view with its ETag and long poll, the ack, and the call reports.
type runtimeServer struct {
	revision int64
	agents   []voice.RuntimeAgent
	changed  chan struct{} // closed and replaced on every revision change
	token    string
	acks     []int64
	reports  []map[string]any

	// lastETag is the If-None-Match of the most recent fetch; fetches counts
	// them, so a test can see the conditional request.
	lastETag string
	fetches  int
	// issued are the tokens the fake token endpoint granted.
	issued map[string]bool

	mu sync.Mutex
}

func newRuntimeServer(t *testing.T) (*runtimeServer, *httptest.Server) {
	rs := &runtimeServer{revision: 1, changed: make(chan struct{}), issued: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/voice-runtime/agents", rs.serveAgents)
	mux.HandleFunc("POST /api/v1/voice-runtime/ack", rs.serveAck)
	mux.HandleFunc("POST /api/v1/voice-runtime/calls", rs.serveCalls)
	mux.HandleFunc("POST /oauth/token", rs.serveToken)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return rs, srv
}

// serveToken is the client-credentials endpoint (RFC 6749 4.4).
func (rs *runtimeServer) serveToken(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if !ok || id != "hello_sa_lab" || secret != "its-secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	tok := "issued-" + strconv.Itoa(len(rs.issued))
	rs.mu.Lock()
	rs.issued[tok] = true
	rs.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 3600})
}

// publish swaps the view and wakes every long poll.
func (rs *runtimeServer) publish(revision int64, agents []voice.RuntimeAgent) {
	rs.mu.Lock()
	rs.revision, rs.agents = revision, agents
	close(rs.changed)
	rs.changed = make(chan struct{})
	rs.mu.Unlock()
}

func (rs *runtimeServer) serveAgents(w http.ResponseWriter, r *http.Request) {
	rs.mu.Lock()
	token, revision := rs.token, rs.revision
	rs.mu.Unlock()
	bearer := r.Header.Get("Authorization")
	rs.mu.Lock()
	ok := token != "" && bearer == "Bearer "+token || rs.issued[strings.TrimPrefix(bearer, "Bearer ")]
	rs.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	rs.mu.Lock()
	rs.lastETag, rs.fetches = r.Header.Get("If-None-Match"), rs.fetches+1
	rs.mu.Unlock()
	etag := voice.ETag(revision)
	if match := r.Header.Get("If-None-Match"); match == etag {
		// Hold the long poll until the revision moves or the wait is up; the
		// fake answers within a second, which keeps the two-second reload
		// budget for Hello and the agent.
		rev, _ := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
		deadline := time.Now().Add(time.Second)
		moved := false
		for time.Now().Before(deadline) {
			rs.mu.Lock()
			cur := rs.revision
			changed := rs.changed
			rs.mu.Unlock()
			if cur != rev {
				moved = true
				revision, etag = cur, voice.ETag(cur)
				break
			}
			select {
			case <-changed:
			case <-time.After(50 * time.Millisecond):
			}
		}
		if !moved {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-store")
	rs.mu.Lock()
	agents, tenant := rs.agents, agentTenant
	rs.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(voice.View{Tenant: tenant, Revision: revision, Agents: agents})
}

func (rs *runtimeServer) serveAck(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	var body struct {
		Revision int64 `json:"revision"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	rs.mu.Lock()
	rs.acks = append(rs.acks, body.Revision)
	rs.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (rs *runtimeServer) serveCalls(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	rs.mu.Lock()
	rs.reports = append(rs.reports, body)
	rs.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// waitView waits until the agent's view holds one agent and returns it.
func waitView(t *testing.T, a *Agent, revision int64) voice.RuntimeAgent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v := a.View()
		if v.Revision == revision && len(v.Agents) == 1 {
			return v.Agents[0]
		}
		if errs := a.Errors(); len(errs) > 0 {
			t.Fatalf("agent runtime errors: %v", errs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("view never reached revision %d (last: %+v, errors: %v)", revision, a.View(), a.Errors())
	return voice.RuntimeAgent{}
}

// TestFakeAgentAnswersAndReports drives the whole call: a signed INVITE
// reaches an agent the runtime view named, the agent answers with G.711 on
// the address its SDP named, speaks a tone the offer's address receives,
// acknowledges the loaded view, and reports the call when it ends (S-19 to
// S-21).
func TestFakeAgentAnswersAndReports(t *testing.T) {
	rs, srv := newRuntimeServer(t)
	rs.token = "runtime-token-1"
	rs.publish(3, []voice.RuntimeAgent{{
		Name: agentName, SIPUser: agentSIPUser, Greeting: agentGreet, Language: "en",
	}})
	a := start(t, Options{
		Listen: "127.0.0.1:0", ContactHost: "127.0.0.1",
		Keys: []voice.Key{testKey}, Tenant: agentTenant,
		Base: srv.URL, Token: rs.token, Version: "fake-1",
	})
	defer a.Close()
	waitView(t, a, 3)

	// The caller's media socket: the offer names it, so the tone lands here.
	tone, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tone.Close() }()
	_, portStr, _ := net.SplitHostPort(tone.LocalAddr().String())
	tonePort, _ := strconv.Atoi(portStr)

	ts := time.Now()
	sess := dialSigned(t, a, agentSIPUser, "corr-e2e", ts, []byte(sdpOfferText(tonePort)))
	opts := sipgo.AnswerOptions{}
	if err := sess.WaitAnswer(ctxOf(10*time.Second), opts); err != nil {
		t.Fatalf("answer: %v", err)
	}
	ans := string(sess.InviteResponse.Body())
	if !strings.Contains(ans, "c=IN IP4 127.0.0.1") {
		t.Fatalf("answer SDP %q lacks the agent's address", ans)
	}
	if !strings.Contains(ans, "PCMU/8000") && !strings.Contains(ans, "PCMA/8000") {
		t.Fatalf("answer SDP %q lacks a G.711 codec", ans)
	}
	if err := sess.Ack(ctxOf(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// The tone lands on the offer's address.
	buf := make([]byte, 512)
	if err := tone.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tone.ReadFrom(buf); err != nil {
		t.Fatalf("no RTP tone on the offer address: %v", err)
	}
	// Hang up; the agent must report the call to the runtime API.
	if err := sess.Bye(ctxOf(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(rs.reportsSnapshot()) == 0 || len(a.Reports()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no call report (agent saw %d reports, server %d): errors %v",
				len(a.Reports()), len(rs.reportsSnapshot()), a.Errors())
		}
		time.Sleep(20 * time.Millisecond)
	}
	report := rs.reportsSnapshot()[0]
	if report["correlationId"] != "corr-e2e" || report["outcome"] != "answered" {
		t.Fatalf("report = %+v", report)
	}
	// The call carried the signed header set; the agent recorded it.
	calls := a.Calls()
	if len(calls) != 1 || calls[0].Tenant != agentTenant || calls[0].Agent != agentName ||
		calls[0].SIPUser != agentSIPUser || calls[0].Origin != "extension" || calls[0].Called != "9011" {
		t.Fatalf("agent recorded call %+v", calls)
	}
	// The reload was acknowledged.
	if acks := rs.acksSnapshot(); len(acks) == 0 || acks[len(acks)-1] != 3 {
		t.Fatalf("acks = %v, want the view's revision 3", acks)
	}
}

// ctxOf is a timeout context for one SIP step.
func ctxOf(d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ctx
}

// acksSnapshot copies the ack revisions.
func (rs *runtimeServer) acksSnapshot() []int64 {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]int64(nil), rs.acks...)
}

// reportsSnapshot copies the reports the server received.
func (rs *runtimeServer) reportsSnapshot() []map[string]any {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]map[string]any(nil), rs.reports...)
}

// start starts an agent and fails the test instead of returning an error.
func start(t *testing.T, opts Options) *Agent {
	t.Helper()
	a, err := Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

// TestFakeAgentRefuses pins down the refusal path: a call without the signed
// header set, with a broken signature, a stale timestamp or a replayed
// correlation id is 403, an unknown SIP user is 404, and a behavior code is
// answered verbatim (spec S-14 to S-16).
func TestFakeAgentRefuses(t *testing.T) {
	rs, srv := newRuntimeServer(t)
	rs.token = "runtime-token-2"
	rs.publish(2, []voice.RuntimeAgent{{Name: agentName, SIPUser: agentSIPUser}})
	a := start(t, Options{
		Listen: "127.0.0.1:0", ContactHost: "127.0.0.1",
		Keys: []voice.Key{testKey}, Tenant: agentTenant,
		Base: srv.URL, Token: rs.token,
	})
	defer a.Close()
	waitView(t, a, 2)
	// The default behavior refuses with 486, so an accepted signature shows
	// as 486 and a refused one as 403: nothing here needs an ACK.
	a.SetBehavior("", func(Call) int { return sip.StatusBusyHere })
	now := time.Now()
	corr := "corr-refuse"
	accepted := func(res *sip.Response, what string) {
		t.Helper()
		if res.StatusCode != sip.StatusBusyHere {
			t.Fatalf("%s = %d, want 486 (accepted)", what, res.StatusCode)
		}
	}
	refused := func(res *sip.Response, what string) {
		t.Helper()
		if res.StatusCode != sip.StatusForbidden {
			t.Fatalf("%s = %d, want 403 (refused)", what, res.StatusCode)
		}
	}

	// A good signature is accepted.
	accepted(sendInvite(t, a, agentSIPUser, agentTenant, agentName, corr, now,
		voice.Sign(testKey, agentTenant, agentName, agentSIPUser, corr, now)), "signed invite")

	// A missing, malformed or corrupted X-Hello-Auth is refused.
	refused(sendInvite(t, a, agentSIPUser, agentTenant, agentName, corr, now, ""), "no auth")
	refused(sendInvite(t, a, agentSIPUser, agentTenant, agentName, corr, now, "v1:junk"), "malformed auth")
	bad := voice.Sign(testKey, agentTenant, agentName, agentSIPUser, corr+"-x", now)
	refused(sendInvite(t, a, agentSIPUser, agentTenant, agentName, corr, now, bad), "wrong-field signature")
	spoiled := voice.Sign(testKey, agentTenant, agentName, agentSIPUser, corr, now)
	spoiled = spoiled[:len(spoiled)-4] + "dead"
	refused(sendInvite(t, a, agentSIPUser, agentTenant, agentName, corr, now, spoiled), "spoiled signature")

	// A timestamp more than the skew away is refused.
	old := now.Add(-31 * time.Second)
	stale := voice.Sign(testKey, agentTenant, agentName, agentSIPUser, corr+"-stale", old)
	refused(sendInvite(t, a, agentSIPUser, agentTenant, agentName, corr+"-stale", old, stale), "stale timestamp")

	// A replayed correlation id is refused: the first signed call goes
	// through, the second with the same correlation does not.
	dup := voice.Sign(testKey, agentTenant, agentName, agentSIPUser, corr+"-replay", now)
	accepted(sendInvite(t, a, agentSIPUser, agentTenant, agentName, corr+"-replay", now, dup), "first replay invite")
	refused(sendInvite(t, a, agentSIPUser, agentTenant, agentName, corr+"-replay", now, dup), "replayed correlation")

	// A SIP user the loaded view does not name is 404, not 403.
	unknown := voice.Sign(testKey, agentTenant, agentName, "9999", "corr-unknown", now)
	res := sendInvite(t, a, "9999", agentTenant, agentName, "corr-unknown", now, unknown)
	if res.StatusCode != sip.StatusNotFound {
		t.Fatalf("unknown sip user = %d, want 404", res.StatusCode)
	}
}

// TestFakeAgentSharedVectors runs the agent's check over every vector of the
// file Hello shares with talking-agent: each signature must be accepted
// (shown by the 486 of the busy behavior) and each tampered field refused.
func TestFakeAgentSharedVectors(t *testing.T) {
	vectors := loadVectors(t)
	keys := make([]voice.Key, 0, len(vectors.Keys))
	for _, k := range vectors.Keys {
		keys = append(keys, voice.Key{ID: k.ID, Secret: []byte(k.Secret)})
	}
	// clock follows each vector's timestamp: Verify allows thirty seconds
	// of skew and the vectors are far apart. It is guarded, because the
	// agent reads it from its SIP goroutine.
	var clockMu sync.Mutex
	clock := time.Unix(1767225660, 0)
	setClock := func(ts time.Time) { clockMu.Lock(); clock = ts; clockMu.Unlock() }
	a := start(t, Options{
		Listen: "127.0.0.1:0", ContactHost: "127.0.0.1",
		Keys: keys, Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return clock
		},
	})
	defer a.Close()
	a.SetBehavior("", func(Call) int { return sip.StatusBusyHere })
	for _, v := range vectors.Vectors {
		setClock(time.Unix(v.Ts, 0))
		ts := time.Unix(v.Ts, 0)
		res := sendInvite(t, a, v.SIPUser, v.Tenant, v.Agent, v.Correlation, ts, v.AuthHeader)
		if res.StatusCode != sip.StatusBusyHere {
			t.Fatalf("vector %s/%s = %d, want 486 (accepted)", v.Tenant, v.Correlation, res.StatusCode)
		}
	}
	// One tampered field breaks the signature.
	v := vectors.Vectors[0]
	ts := time.Unix(v.Ts, 0)
	res := sendInvite(t, a, v.SIPUser, v.Tenant, "not-"+v.Agent, v.Correlation, ts, v.AuthHeader)
	if res.StatusCode != sip.StatusForbidden {
		t.Fatalf("tampered agent field = %d, want 403", res.StatusCode)
	}
}

// vectorsFile is the shared signature vectors, unmarshalled.
type vectorsFile struct {
	Keys []struct {
		ID, Secret string
	} `json:"keys"`
	Vectors []struct {
		Tenant, Agent, SIPUser, Correlation string
		Ts                                  int64
		KeyID, AuthHeader                   string
	} `json:"vectors"`
}

// loadVectors reads internal/voice/testdata/voice_auth_vectors.json.
func loadVectors(t *testing.T) vectorsFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "voice", "testdata", "voice_auth_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f vectorsFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestFakeAgentRuntimeReload proves the runtime half: the agent fetches the
// view with its bearer credential, acknowledges it, answers 304 with the
// ETag it was given, and applies a persona edit within two seconds of the
// revision change (S-19, S-20, and the two-second budget).
func TestFakeAgentRuntimeReload(t *testing.T) {
	rs, srv := newRuntimeServer(t)
	rs.token = "runtime-token-3"
	agent := voice.RuntimeAgent{Name: agentName, SIPUser: agentSIPUser, Greeting: agentGreet}
	rs.publish(7, []voice.RuntimeAgent{agent})
	a := start(t, Options{
		Listen: "127.0.0.1:0", ContactHost: "127.0.0.1",
		Keys: []voice.Key{testKey}, Tenant: agentTenant,
		Base: srv.URL, Token: rs.token,
	})
	defer a.Close()
	waitView(t, a, 7)

	// The client-credentials path: same agent, fresh one, no static token.
	b := start(t, Options{
		Listen: "127.0.0.1:0", ContactHost: "127.0.0.1",
		Keys: []voice.Key{testKey}, Tenant: agentTenant,
		Base: srv.URL, ClientID: "hello_sa_lab", ClientSecret: "its-secret",
	})
	defer b.Close()

	// A persona edit bumps the revision; the view reaches the agent within
	// two seconds.
	edited := agent
	edited.Greeting = "Updated greeting."
	started := time.Now()
	rs.publish(8, []voice.RuntimeAgent{edited})
	deadline := started.Add(2 * time.Second)
	for a.View().Revision != 8 {
		if time.Now().After(deadline) {
			t.Fatalf("greeting edit did not reach the agent within 2s (view %+v, errors %v)",
				a.View(), a.Errors())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := a.View().Agents[0].Greeting; got != edited.Greeting {
		t.Fatalf("agent view greeting = %q, want the edit", got)
	}
	if lag := time.Since(started); lag > 2*time.Second {
		t.Fatalf("reload took %s, want under 2s", lag)
	}
	// Both agents acknowledged what they loaded.
	deadline = time.Now().Add(3 * time.Second)
	for {
		acks := rs.acksSnapshot()
		if len(acks) >= 3 && acks[0] == 7 && acks[len(acks)-1] == 8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("acks = %v, want both agents' loads of 7 and 8", acks)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if errs := b.Errors(); len(errs) > 0 {
		t.Fatalf("client-credentials agent errors: %v", errs)
	}
	if v := b.View(); v.Revision != 8 {
		t.Fatalf("client-credentials agent view revision = %d, want 8", v.Revision)
	}
	// The fetches carried the ETag of the loaded revision and the long-poll
	// parameters of the contract.
	rs.mu.Lock()
	etag := rs.lastETag
	rs.mu.Unlock()
	if etag != `"8"` {
		t.Fatalf("last fetch If-None-Match = %q, want the strong revision tag", etag)
	}
}

// TestFakeAgentBusyBehavior proves the busy behavior is answered verbatim
// (486), which Hello maps like its own busy (S-35).
func TestFakeAgentBusyBehavior(t *testing.T) {
	a := start(t, Options{
		Listen: "127.0.0.1:0", ContactHost: "127.0.0.1",
		Keys: []voice.Key{testKey}, Tenant: agentTenant,
	})
	defer a.Close()
	a.SetBehavior("", func(Call) int { return sip.StatusBusyHere })
	now := time.Now()
	res := sendInvite(t, a, agentSIPUser, agentTenant, agentName, "corr-busy", now,
		voice.Sign(testKey, agentTenant, agentName, agentSIPUser, "corr-busy", now))
	if res.StatusCode != sip.StatusBusyHere {
		t.Fatalf("busy behavior = %d, want 486", res.StatusCode)
	}
	// A per-agent behavior wins over the default one: here 403, the reject
	// path (a failed destination for Hello).
	a.SetBehavior(agentName, func(Call) int { return sip.StatusForbidden })
	res = sendInvite(t, a, agentSIPUser, agentTenant, agentName, "corr-reject", now,
		voice.Sign(testKey, agentTenant, agentName, agentSIPUser, "corr-reject", now))
	if res.StatusCode != sip.StatusForbidden {
		t.Fatalf("per-agent behavior = %d, want 403", res.StatusCode)
	}
}

// dialSigned places a signed INVITE with offer as its SDP and waits for the
// agent's answer.
func dialSigned(t *testing.T, a *Agent, sipUser, corr string, ts time.Time, offer []byte) *sipgo.DialogClientSession {
	t.Helper()
	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientNAT())
	if err != nil {
		t.Fatal(err)
	}
	host, _, _ := net.SplitHostPort(a.Addr())
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: sipUser, Host: host})
	req.SetDestination(a.Addr())
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{User: "+971500000001", Host: agentDomain}, Params: tag()})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{User: sipUser, Host: agentDomain}})
	req.AppendHeader(sip.NewHeader("Contact", "<sip:caller@"+a.Addr()+">"))
	mf := sip.MaxForwardsHeader(70)
	req.AppendHeader(&mf)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE})
	cid := sip.CallIDHeader(sip.GenerateTagN(24))
	req.AppendHeader(&cid)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(offer)
	req.AppendHeader(sip.NewHeader(voice.HeaderCaller, "+971500000001"))
	req.AppendHeader(sip.NewHeader(voice.HeaderCalled, "9011"))
	req.AppendHeader(sip.NewHeader(voice.HeaderCallerOrigin, "extension"))
	req.AppendHeader(sip.NewHeader(voice.HeaderTenant, agentTenant))
	req.AppendHeader(sip.NewHeader(voice.HeaderAgent, agentName))
	req.AppendHeader(sip.NewHeader(voice.HeaderCorrelation, corr))
	req.AppendHeader(sip.NewHeader(voice.HeaderTs, strconv.FormatInt(ts.Unix(), 10)))
	req.AppendHeader(sip.NewHeader(voice.HeaderAuth,
		voice.Sign(testKey, agentTenant, agentName, agentSIPUser, corr, ts)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := (&sipgo.DialogUA{Client: client, ContactHDR: sip.ContactHeader{
		Address: sip.Uri{User: "caller", Host: host},
	}, RewriteContact: true}).WriteInvite(ctx, req)
	if err != nil {
		_ = ua.Close()
		t.Fatalf("signed INVITE: %v", err)
	}
	t.Cleanup(func() { _ = ua.Close() })
	return sess
}
