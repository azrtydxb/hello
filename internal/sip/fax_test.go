package sip

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/fax"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
)

// --- fakes ------------------------------------------------------------------

// fakeHelloFax is the hello-fax control API on loopback, with a UDP socket
// standing in for the engine's media port. A test finishes the session by
// calling finish.
type fakeHelloFax struct {
	t   *testing.T
	srv *httptest.Server
	udp *net.UDPConn

	mu       sync.Mutex
	starts   []map[string]string
	body     int // bytes of PDF posted with the last start
	pdf      []byte
	pages    int
	finished bool
	ok       bool
	t38      atomic.Int64
	stops    atomic.Int64
	forgets  atomic.Int64
}

func newFakeHelloFax(t *testing.T) *fakeHelloFax {
	t.Helper()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeHelloFax{t: t, udp: udp, pdf: []byte("%PDF-1.4 received fax"), pages: 2, ok: true}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		q := map[string]string{}
		for k := range r.URL.Query() {
			q[k] = r.URL.Query().Get(k)
		}
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.starts, f.body = append(f.starts, q), len(b)
		f.mu.Unlock()
		f.reply(w, false)
	})
	mux.HandleFunc("GET /v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) { f.reply(w, false) })
	mux.HandleFunc("POST /v1/sessions/{id}/t38", func(w http.ResponseWriter, r *http.Request) {
		f.t38.Add(1)
		f.reply(w, false)
	})
	mux.HandleFunc("POST /v1/sessions/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		f.stops.Add(1)
		f.reply(w, false)
	})
	mux.HandleFunc("GET /v1/sessions/{id}/pdf", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(f.pdf) })
	mux.HandleFunc("DELETE /v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.forgets.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(func() { f.srv.Close(); _ = udp.Close() })
	return f
}

func (f *fakeHelloFax) reply(w http.ResponseWriter, wantT38 bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info := fax.SessionInfo{ID: "s1", Host: "127.0.0.1", Port: f.udp.LocalAddr().(*net.UDPAddr).Port, Mode: "g711", State: "running"}
	// Like hello-fax: an outbound session allowed T.38 asks for it until it has it.
	info.WantT38 = wantT38 || (len(f.starts) > 0 && f.starts[0]["direction"] == fax.SessionSend && f.starts[0]["t38"] == "1" && f.t38.Load() == 0)
	if f.finished {
		info.State, info.Success, info.Pages, info.BitRate, info.ECM, info.Text = "done", f.ok, f.pages, 14400, true, "OK"
		info.HasPDF = len(f.starts) > 0 && f.starts[0]["direction"] == fax.SessionReceive
		if !f.ok {
			info.Code, info.Text = 22, "The call dropped prematurely"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

func (f *fakeHelloFax) finish(ok bool) {
	f.mu.Lock()
	f.finished, f.ok = true, ok
	f.mu.Unlock()
}

func (f *fakeHelloFax) client() *fax.Client {
	return &fax.Client{Addr: strings.TrimPrefix(f.srv.URL, "http://")}
}

func (f *fakeHelloFax) start(i int) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.starts) {
		return nil
	}
	return f.starts[i]
}

// fakeFaxStore is the fax table in memory.
type fakeFaxStore struct {
	mu       sync.Mutex
	next     int64
	begun    map[int64]fax.Receive
	finished map[int64]fax.Finish
	queue    []fax.Fax
	retried  map[int64]time.Duration
	linked   map[int64]string
	cancel   atomic.Bool
}

func newFakeFaxStore() *fakeFaxStore {
	return &fakeFaxStore{begun: map[int64]fax.Receive{}, finished: map[int64]fax.Finish{}, retried: map[int64]time.Duration{}, linked: map[int64]string{}}
}

func (s *fakeFaxStore) BeginReceive(_ context.Context, in fax.Receive) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	s.begun[s.next] = in
	return s.next, nil
}
func (s *fakeFaxStore) Finish(_ context.Context, id int64, f fax.Finish) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished[id] = f
	return nil
}
func (s *fakeFaxStore) ClaimOutbound(_ context.Context, _ string, _ time.Duration, _ int) ([]fax.Fax, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queue
	s.queue = nil
	return q, nil
}
func (s *fakeFaxStore) Renew(context.Context, int64, string, time.Duration) (bool, error) {
	return !s.cancel.Load(), nil
}
func (s *fakeFaxStore) Retry(_ context.Context, id int64, after time.Duration, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried[id] = after
	return nil
}
func (s *fakeFaxStore) Link(_ context.Context, id int64, corr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linked[id] = corr
	return nil
}

func (s *fakeFaxStore) result(id int64) (fax.Finish, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.finished[id]
	return f, ok
}

func withFax(fd *FaxDeps) pbxOpt {
	return func(c *Config, d *Deps) {
		d.Fax = fd
		c.FaxInterval = 50 * time.Millisecond
	}
}

func faxDestinations(ds ...snapshot.FaxDestination) pbxOpt {
	return withFeatures(func(s *snapshot.Snapshot) { s.WithFaxDestinations(ds) })
}

// faxRig is a PBX with a trunk whose DID goes to the fax destination "main".
type faxRig struct {
	t       *testing.T
	pbx     *testPBX
	hf      *fakeHelloFax
	store   *fakeFaxStore
	objs    *fax.MemObjects
	carrier *phone
	rtp     *net.UDPConn
	router  *fakeRouter
}

func newFaxRig(t *testing.T, mode string, deps func(*FaxDeps), extra ...pbxOpt) *faxRig {
	t.Helper()
	rig := &faxRig{t: t, hf: newFakeHelloFax(t), store: newFakeFaxStore(), objs: &fax.MemObjects{}}
	rig.router = &fakeRouter{extensions: map[string]bool{"100": true}, sources: map[netip.Addr]int64{},
		trunks: []routing.Trunk{{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true, FaxMode: mode}}}
	rig.router.decide = func(c routing.Call, _ routing.TrunkUsability) routing.Decision {
		d := routing.Decision{Kind: routing.KindInbound, DestKind: "fax", DestTarget: "main", Route: "Fax DID", CallerID: "+971555000111"}
		d.Trace.Add(`Inbound route "Fax DID" matched (exact ` + c.Number + `)`)
		return d
	}
	fd := &FaxDeps{Enabled: true, Client: rig.hf.client(), Store: rig.store, Objects: rig.objs}
	if deps != nil {
		deps(fd)
	}
	opts := append([]pbxOpt{trunkCfg(newFakeTrunkState()), withRouter(rig.router, nil), withMedia(false),
		faxDestinations(snapshot.FaxDestination{ID: 5, Name: "main", Extension: "100", Email: "fax@hello.test"}), withFax(fd)}, extra...)
	rig.pbx = startPBX(t, callerDevices(), opts...)
	rig.carrier = newPhone(t, rig.pbx, "carrier", "")
	rig.rtp = rtpSocket(t, rig.carrier)
	ap, _ := netip.ParseAddrPort(rig.carrier.addr)
	rig.router.setSource(ap.Addr(), 7)
	return rig
}

// call places the carrier's INVITE with body (the phone's own audio SDP when
// empty) and returns the answered dialog.
func (r *faxRig) call(body string) (*sipgo.DialogClientSession, []byte) {
	r.t.Helper()
	inv := carrierInvite(r.carrier, r.pbx, "+97140000100")
	if body != "" {
		inv.SetBody([]byte(body))
	}
	dcs, err := r.carrier.dua.WriteInvite(r.t.Context(), inv)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := dcs.WaitAnswer(r.t.Context(), sipgo.AnswerOptions{}); err != nil {
		r.t.Fatalf("the fax call was not answered: %v", err)
	}
	if err := dcs.Ack(r.t.Context()); err != nil {
		r.t.Fatal(err)
	}
	return dcs, dcs.InviteResponse.Body()
}

// refused places the INVITE and returns the final status.
func (r *faxRig) refused(body string) int {
	r.t.Helper()
	inv := carrierInvite(r.carrier, r.pbx, "+97140000100")
	if body != "" {
		inv.SetBody([]byte(body))
	}
	return r.carrier.do(inv).StatusCode
}

func (r *faxRig) rtpPacket(seq uint16, payload []byte, to int) {
	pkt := media.BuildRTP(0, seq, uint32(seq)*160, 0xfa5, seq == 1, payload)
	_, _ = r.rtp.WriteToUDP(pkt, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: to})
}

func readUDP(t *testing.T, c *net.UDPConn, what string) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	n, _, err := c.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return buf[:n]
}

// cngPCMU is 20 ms of the 1100 Hz calling tone, G.711 u-law.
func cngPCMU() []byte {
	pcm := make([]byte, 320)
	for i := 0; i < 160; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*1100*float64(i)/8000))
		pcm[2*i], pcm[2*i+1] = byte(v), byte(uint16(v)>>8)
	}
	return media.EncodePCMU(pcm)
}

// --- inbound ----------------------------------------------------------------

// TestFaxInboundG711 fails if a call routed to a fax destination is not
// answered with G.711 only, if its media does not cross the relay to the
// engine both ways, if the engine session is not opened as a receive on the
// relay's engine leg, or if the result is not stored as a PDF named by the
// fax id with the row, the email address, the CDR kind and a BYE to the
// carrier.
func TestFaxInboundG711(t *testing.T) {
	r := newFaxRig(t, "", nil)
	dcs, answer := r.call("")
	ans, err := media.ParseAudioSDP(answer)
	if err != nil || len(ans.Codecs) != 1 || ans.Codecs[0].PT != 0 {
		t.Fatalf("answer = %q (%v), want PCMU only", answer, err)
	}
	if strings.Contains(string(answer), "telephone-event") {
		t.Errorf("a fax answer offers DTMF events: %s", answer)
	}
	eventually(t, "the engine session", func() bool { return r.hf.start(0) != nil })
	st := r.hf.start(0)
	if st["direction"] != "receive" || st["mode"] != "g711" || st["pt"] != "0" || st["ident"] != "+97140000100" {
		t.Fatalf("session start = %v", st)
	}
	_, enginePort, _ := net.SplitHostPort(st["remote"])
	if enginePort == strconv.Itoa(ans.Port) {
		t.Fatalf("the engine leg shares the carrier's port %d", ans.Port)
	}

	// Carrier to engine, then engine to carrier: through the relay.
	payload := make([]byte, 160)
	for i := range payload {
		payload[i] = 0xD5
	}
	r.rtpPacket(1, payload, ans.Port)
	got := readUDP(t, r.hf.udp, "carrier audio at the engine")
	if p, ok := media.ParseRTP(got); !ok || len(p.Payload) != 160 || p.Payload[0] != 0xD5 {
		t.Fatalf("the engine received %v", got)
	}
	ep, _ := strconv.Atoi(enginePort)
	_, _ = r.hf.udp.WriteToUDP(media.BuildRTP(0, 1, 160, 0xe9, true, payload), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ep})
	back := readUDP(t, r.rtp, "engine audio at the carrier")
	if p, ok := media.ParseRTP(back); !ok || len(p.Payload) != 160 {
		t.Fatalf("the carrier received %v", back)
	}

	r.hf.finish(true)
	waitReq(t, r.carrier.byes, "BYE to the carrier when T.30 ends")
	_ = dcs
	cd := r.pbx.nextCDR(t)
	if cd.Kind != cdr.KindFax || cd.Direction != "inbound" || cd.Trunk != "carrier-in" || cd.FinalStatus != 200 {
		t.Fatalf("CDR = %+v", cd)
	}
	if !strings.Contains(cd.FailureReason, "") || cd.Source != "+971555000111" {
		t.Errorf("CDR source = %q", cd.Source)
	}
	eventually(t, "the fax row", func() bool { _, ok := r.store.result(1); return ok })
	fin, _ := r.store.result(1)
	key := fax.ObjectKey(1, time.Now())
	if fin.Status != fax.StatusReceived || fin.Pages != 2 || fin.Mode != "g711" || fin.BitRate != 14400 || fin.ObjectKey != key || fin.MailTo != "fax@hello.test" {
		t.Fatalf("finished row = %+v", fin)
	}
	if b, err := r.objs.Get(t.Context(), key); err != nil || string(b) != "%PDF-1.4 received fax" {
		t.Fatalf("stored PDF = %q (%v)", b, err)
	}
	begun := r.store.begun[1]
	if begun.PeerNumber != "+971555000111" || begun.DestinationID != 5 || begun.OwnerExtension != "100" || begun.CorrelationID == "" {
		t.Fatalf("begun = %+v", begun)
	}
	eventually(t, "the session is forgotten", func() bool { return r.hf.forgets.Load() > 0 })
}

// TestFaxUsesOwnersAddressWithoutDestinationEmail fails if a destination with
// no address does not send the PDF to the owner's users, or a failed fax with
// no pages is mailed.
func TestFaxMailTargets(t *testing.T) {
	r := newFaxRig(t, "", nil, faxDestinations(snapshot.FaxDestination{ID: 6, Name: "main", Extension: "100"}))
	r.call("")
	eventually(t, "the engine session", func() bool { return r.hf.start(0) != nil })
	r.hf.finish(false) // the call dropped; two pages came in
	waitReq(t, r.carrier.byes, "BYE")
	eventually(t, "the fax row", func() bool { _, ok := r.store.result(1); return ok })
	fin, _ := r.store.result(1)
	if fin.Status != fax.StatusPartial || fin.ResultText != "The call dropped prematurely" || fin.ResultCode != 22 {
		t.Fatalf("a fax that failed after pages must be partial with the result code: %+v", fin)
	}
	if fin.MailTo != "@owner" {
		t.Fatalf("mail_to = %q, want the owner sentinel", fin.MailTo)
	}
}

// TestFaxCallerHangsUpKeepsPages fails if a caller who hangs up mid-fax loses
// what arrived: the session is stopped and the pages stored as partial.
func TestFaxCallerHangsUpKeepsPages(t *testing.T) {
	r := newFaxRig(t, "", nil)
	dcs, _ := r.call("")
	eventually(t, "the engine session", func() bool { return r.hf.start(0) != nil })
	// Let the PBX see the ACK before the BYE: a BYE that overtakes the ACK is
	// a different case (the call was never established).
	time.Sleep(200 * time.Millisecond)
	r.hf.finish(false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := dcs.Bye(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the fax row", func() bool { _, ok := r.store.result(1); return ok })
	fin, _ := r.store.result(1)
	if fin.Status != fax.StatusPartial || fin.ObjectKey == "" {
		t.Fatalf("row = %+v", fin)
	}
	if r.hf.stops.Load() == 0 {
		t.Error("the engine session was not stopped when the call ended")
	}
}

// TestFaxRefusals fails if a fax call that cannot be taken is not refused with
// the documented status.
func TestFaxRefusals(t *testing.T) {
	t.Run("fax off on the trunk", func(t *testing.T) {
		r := newFaxRig(t, fax.ModeOff, nil)
		if code := r.refused(""); code != 488 {
			t.Fatalf("status %d, want 488", code)
		}
	})
	t.Run("engine not configured", func(t *testing.T) {
		r := newFaxRig(t, "", func(fd *FaxDeps) { fd.Enabled = false })
		if code := r.refused(""); code != 488 {
			t.Fatalf("status %d, want 488", code)
		}
	})
	t.Run("engine down", func(t *testing.T) {
		r := newFaxRig(t, "", func(fd *FaxDeps) { fd.Client = &fax.Client{Addr: "127.0.0.1:1"} })
		if code := r.refused(""); code != 488 {
			t.Fatalf("status %d, want 488 (a down engine never takes the call)", code)
		}
		if n := len(r.store.begun); n != 0 {
			t.Errorf("%d fax rows for a refused call", n)
		}
	})
	t.Run("unknown destination", func(t *testing.T) {
		r := newFaxRig(t, "", nil, faxDestinations())
		r.router.setDecide(func(c routing.Call, _ routing.TrunkUsability) routing.Decision {
			return routing.Decision{Kind: routing.KindInbound, DestKind: "fax", DestTarget: "nope"}
		})
		if code := r.refused(""); code != 404 {
			t.Fatalf("status %d, want 404", code)
		}
	})
	t.Run("no G.711 in the offer", func(t *testing.T) {
		r := newFaxRig(t, "", nil)
		offer := "v=0\r\no=c 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 4000 RTP/AVP 9\r\na=rtpmap:9 G722/8000\r\n"
		if code := r.refused(offer); code != 488 {
			t.Fatalf("status %d, want 488", code)
		}
	})
	t.Run("T.38 offer on a passthrough trunk", func(t *testing.T) {
		r := newFaxRig(t, "", nil)
		if code := r.refused(string(media.BuildT38SDP("127.0.0.1", 4000))); code != 488 {
			t.Fatalf("status %d, want 488", code)
		}
	})
}

// --- T.38 -------------------------------------------------------------------

// TestFaxT38OfferedByTheCarrier fails if a T.38 INVITE on a T.38 trunk is not
// answered with an image stream and the standard T.38 attributes, if the
// engine session is not a T.38 terminal, or if UDPTL does not cross the relay
// byte for byte in both directions.
func TestFaxT38OfferedByTheCarrier(t *testing.T) {
	r := newFaxRig(t, fax.ModeT38, nil)
	offer := string(media.BuildT38SDP("127.0.0.1", r.rtp.LocalAddr().(*net.UDPAddr).Port))
	_, answer := r.call(offer)
	t38, ok := media.ParseT38SDP(answer)
	if !ok || t38.Port == 0 || !t38.T38Acceptable() || t38.MaxBitRate != 14400 || t38.RateManagement != "transferredTCF" || t38.UDPEC != "t38UDPRedundancy" {
		t.Fatalf("answer = %q", answer)
	}
	eventually(t, "the engine session", func() bool { return r.hf.start(0) != nil })
	if st := r.hf.start(0); st["mode"] != "t38" || st["direction"] != "receive" {
		t.Fatalf("session start = %v", st)
	}
	udptl, err := fax.UDPTL{Seq: 1, Primary: []byte{0x00, 0x01, 0x02}}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.rtp.WriteToUDP(udptl, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: t38.Port})
	if got := readUDP(t, r.hf.udp, "UDPTL at the engine"); string(got) != string(udptl) {
		t.Fatalf("UDPTL changed at the relay: % x -> % x", udptl, got)
	}
	_, enginePort, _ := net.SplitHostPort(r.hf.start(0)["remote"])
	ep, _ := strconv.Atoi(enginePort)
	reply, _ := fax.UDPTL{Seq: 9, Primary: []byte{0x07}, Secondary: [][]byte{{0x06}}}.Marshal()
	_, _ = r.hf.udp.WriteToUDP(reply, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ep})
	if got := readUDP(t, r.rtp, "UDPTL at the carrier"); string(got) != string(reply) {
		t.Fatalf("UDPTL changed at the relay: % x -> % x", reply, got)
	}
	r.hf.finish(true)
	waitReq(t, r.carrier.byes, "BYE")
	eventually(t, "the fax row", func() bool { _, ok := r.store.result(1); return ok })
	if fin, _ := r.store.result(1); fin.Mode != "t38" || fin.Status != fax.StatusReceived {
		t.Fatalf("row = %+v", fin)
	}
}

// TestFaxSwitchesToT38OnCNG fails if CNG on an answered fax call on a T.38
// trunk does not re-INVITE the carrier to an image stream and move the engine
// to a T.38 terminal on the same port.
func TestFaxSwitchesToT38OnCNG(t *testing.T) {
	r := newFaxRig(t, fax.ModeT38, nil)
	_, answer := r.call("")
	ans, _ := media.ParseAudioSDP(answer)
	eventually(t, "the engine session", func() bool { return r.hf.start(0) != nil })
	go func() {
		for i := uint16(1); i < 60; i++ { // 1.2 s of the tone
			r.rtpPacket(i, cngPCMU(), ans.Port)
			time.Sleep(20 * time.Millisecond)
		}
	}()
	re := waitReq(t, r.carrier.reinvites, "T.38 re-INVITE")
	if t38, ok := media.ParseT38SDP(re.Body()); !ok || t38.Port == 0 || !strings.Contains(string(re.Body()), "T38FaxUdpEC:t38UDPRedundancy") {
		t.Fatalf("re-INVITE body = %q", re.Body())
	}
	eventually(t, "the engine switched to T.38", func() bool { return r.hf.t38.Load() == 1 })
}

// TestFaxT38RefusedStaysOnG711 fails if a carrier that answers the T.38
// re-INVITE with 488 does not leave the fax running on G.711 passthrough, with
// the engine untouched and the result still stored.
func TestFaxT38RefusedStaysOnG711(t *testing.T) {
	r := newFaxRig(t, fax.ModeT38, nil)
	r.carrier.mu.Lock()
	r.carrier.reinviteStatus = 488
	r.carrier.mu.Unlock()
	_, answer := r.call("")
	ans, _ := media.ParseAudioSDP(answer)
	eventually(t, "the engine session", func() bool { return r.hf.start(0) != nil })
	go func() {
		for i := uint16(1); i < 60; i++ {
			r.rtpPacket(i, cngPCMU(), ans.Port)
			time.Sleep(20 * time.Millisecond)
		}
	}()
	waitReq(t, r.carrier.reinvites, "T.38 re-INVITE")
	time.Sleep(300 * time.Millisecond)
	if n := r.hf.t38.Load(); n != 0 {
		t.Fatalf("the engine was switched to T.38 %d time(s) after the carrier said 488", n)
	}
	// Audio still crosses the relay.
	r.rtpPacket(100, make([]byte, 160), ans.Port)
	readUDP(t, r.hf.udp, "audio after the refused switch")
	r.hf.finish(true)
	waitReq(t, r.carrier.byes, "BYE")
	eventually(t, "the fax row", func() bool { _, ok := r.store.result(1); return ok })
	if fin, _ := r.store.result(1); fin.Mode != "g711" || fin.Status != fax.StatusReceived {
		t.Fatalf("row = %+v", fin)
	}
}

// TestFaxNoSwitchOnPassthroughTrunk fails if CNG moves a call on a
// passthrough trunk to T.38 (D14: T.38 only where the trunk allows it).
func TestFaxNoSwitchOnPassthroughTrunk(t *testing.T) {
	r := newFaxRig(t, fax.ModePassthrough, nil)
	_, answer := r.call("")
	ans, _ := media.ParseAudioSDP(answer)
	for i := uint16(1); i < 40; i++ {
		r.rtpPacket(i, cngPCMU(), ans.Port)
		time.Sleep(20 * time.Millisecond)
	}
	noReq(t, r.carrier.reinvites, 300*time.Millisecond, "a T.38 re-INVITE on a passthrough trunk")
}

// --- outbound ---------------------------------------------------------------

// faxOutRig is a PBX that sends a queued fax through a fake carrier.
func faxOutRig(t *testing.T, mode string) (*testPBX, *fakeHelloFax, *fakeFaxStore, *fax.MemObjects, *carrier, fax.Fax) {
	t.Helper()
	hf, st, objs := newFakeHelloFax(t), newFakeFaxStore(), &fax.MemObjects{}
	cr := newCarrier(t, "acct1", "pw")
	tr := cr.trunk(1, "carrier-out", "ip")
	tr.FaxMode = mode
	r := &fakeRouter{extensions: map[string]bool{"100": true}, trunks: []routing.Trunk{tr}}
	r.decide = outboundVia(r, "+41441234567", "+97140000100", false, nil, 1)
	_ = objs.Put(t.Context(), "fax/2026/9.pdf", []byte("%PDF-1.4 to send"))
	f := fax.Fax{ID: 9, Direction: fax.DirOutbound, PeerNumber: "+41441234567", LocalNumber: "+97140000100", ObjectKey: "fax/2026/9.pdf", Attempts: 1, Status: fax.StatusSending}
	st.queue = []fax.Fax{f}
	fd := &FaxDeps{Enabled: true, Client: hf.client(), Store: st, Objects: objs}
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), withRouter(r, nil), withMedia(false), withFax(fd))
	return pbx, hf, st, objs, cr, f
}

// TestFaxSend fails if a queued fax is not claimed, dialled through the
// outbound route with a G.711-only offer, handed to the engine as a send with
// the stored PDF, and recorded as sent with the CDR kind, direction and a BYE
// to the carrier when T.30 ends.
func TestFaxSend(t *testing.T) {
	pbx, hf, st, _, cr, _ := faxOutRig(t, "")
	inv := waitReq(t, cr.invites, "INVITE to the carrier")
	if inv.To().Address.User != "+41441234567" {
		t.Errorf("dialled %s", inv.To().Address.User)
	}
	off, err := media.ParseAudioSDP(inv.Body())
	if err != nil || len(off.Codecs) != 2 || off.Codecs[0].PT != 0 || off.Codecs[1].PT != 8 {
		t.Fatalf("offer = %q (%v), want PCMU and PCMA only", inv.Body(), err)
	}
	eventually(t, "the engine session", func() bool { return hf.start(0) != nil })
	if s := hf.start(0); s["direction"] != "send" || s["mode"] != "g711" {
		t.Fatalf("session start = %v", s)
	}
	if hf.body == 0 {
		t.Fatal("the PDF was not posted to the engine")
	}
	hf.finish(true)
	waitReq(t, cr.byes, "BYE to the carrier when the fax is sent")
	cd := pbx.nextCDR(t)
	if cd.Kind != cdr.KindFax || cd.Direction != "outbound" || cd.Destination != "+41441234567" {
		t.Fatalf("CDR = %+v", cd)
	}
	eventually(t, "the fax row", func() bool { _, ok := st.result(9); return ok })
	if fin, _ := st.result(9); fin.Status != fax.StatusSent || fin.Pages != 2 {
		t.Fatalf("row = %+v", fin)
	}
	if st.linked[9] == "" {
		t.Error("the fax row is not linked to its CDR")
	}
}

// TestFaxSendRetriesWhenBusy fails if a busy number is not requeued five
// minutes later, or if the last attempt is retried again.
func TestFaxSendRetriesWhenBusy(t *testing.T) {
	_, _, st, _, cr, _ := faxOutRig(t, "")
	cr.inviteCode.Store(486)
	waitReq(t, cr.invites, "INVITE to the carrier")
	eventually(t, "the retry", func() bool { st.mu.Lock(); defer st.mu.Unlock(); _, ok := st.retried[9]; return ok })
	if got := st.retried[9]; got != 5*time.Minute {
		t.Fatalf("retry after %v, want 5m", got)
	}
}

func TestFaxSendGivesUpAfterTheLastAttempt(t *testing.T) {
	_, _, st, _, cr, f := faxOutRig(t, "")
	st.mu.Lock()
	f.Attempts = fax.MaxAttempts
	st.queue = []fax.Fax{f}
	st.mu.Unlock()
	cr.inviteCode.Store(486)
	waitReq(t, cr.invites, "INVITE to the carrier")
	eventually(t, "the fax row", func() bool { _, ok := st.result(9); return ok })
	if fin, _ := st.result(9); fin.Status != fax.StatusFailed {
		t.Fatalf("row = %+v", fin)
	}
	if _, retried := st.retried[9]; retried {
		t.Error("the last attempt was retried")
	}
}

// TestFaxSendCancelled fails if a fax an operator cancels while it is being
// sent keeps its call up.
func TestFaxSendCancelled(t *testing.T) {
	_, hf, st, _, cr, _ := faxOutRig(t, "")
	waitReq(t, cr.invites, "INVITE to the carrier")
	eventually(t, "the engine session", func() bool { return hf.start(0) != nil })
	st.cancel.Store(true)
	waitReq(t, cr.byes, "BYE after the cancel")
}

// TestFaxSendAsksForT38 fails if the engine's request for T.38 on a T.38
// trunk does not re-INVITE the carrier, and if the engine is asked to switch
// only when the carrier accepts.
func TestFaxSendAsksForT38(t *testing.T) {
	_, hf, _, _, cr, _ := faxOutRig(t, fax.ModeT38)
	waitReq(t, cr.invites, "INVITE to the carrier")
	eventually(t, "the engine session", func() bool { return hf.start(0) != nil })
	if hf.start(0)["t38"] != "1" {
		t.Fatalf("the engine was not allowed T.38: %v", hf.start(0))
	}
	re := waitReq(t, cr.reinvites, "T.38 re-INVITE")
	if !media.OffersT38(re.Body()) {
		t.Fatalf("re-INVITE body = %q", re.Body())
	}
}

// --- through an IVR key -------------------------------------------------------

// TestFaxFromIVRKey fails if an IVR key of kind fax does not move the
// answered call to G.711 with a re-INVITE, start the receiver on the same
// relay, and store the result like any received fax; or if a menu whose fax
// engine is down does not keep the caller (the choice is invalid, not a hang
// up).
func TestFaxFromIVRKey(t *testing.T) {
	hf, st, objs := newFakeHelloFax(t), newFakeFaxStore(), &fax.MemObjects{}
	fd := &FaxDeps{Enabled: true, Client: hf.client(), Store: st, Objects: objs}
	vm := newVMStore()
	menu := ivrMenu("main", "800", map[string]snapshot.IVRDest{"1": {Kind: snapshot.IVRFax, Target: "main"}})
	anns := &fakeObjects{}
	_ = anns.Put(t.Context(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000), 8000))
	_ = anns.Put(t.Context(), "ann/bad.wav", media.EncodeWAV(make([]byte, 3200), 8000))
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), withMedia(false),
		withFeatures(func(s *snapshot.Snapshot) {
			s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav", "bad": "ann/bad.wav"})
			s.WithIVRMenus([]snapshot.IVRMenu{menu})
			s.WithFaxDestinations([]snapshot.FaxDestination{{ID: 5, Name: "main", Extension: "100", Email: "fax@hello.test"}})
		}), withVoicemail(vm, anns), withFax(fd))
	a, _, _ := registerABC(t, pbx)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatalf("call to the menu: %v", r.err)
	}
	seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "1", &seq)

	re := waitReq(t, a.reinvites, "re-INVITE onto G.711 for the fax")
	off, err := media.ParseAudioSDP(re.Body())
	if err != nil || len(off.Codecs) != 1 || off.Codecs[0].PT != 0 {
		t.Fatalf("re-INVITE body = %q (%v), want PCMU only", re.Body(), err)
	}
	eventually(t, "the engine session", func() bool { return hf.start(0) != nil })
	if s := hf.start(0); s["direction"] != "receive" || s["mode"] != "g711" {
		t.Fatalf("session start = %v", s)
	}
	hf.finish(true)
	waitReq(t, a.byes, "BYE when the fax ends")
	eventually(t, "the fax row", func() bool { _, ok := st.result(1); return ok })
	if fin, _ := st.result(1); fin.Status != fax.StatusReceived || fin.MailTo != "fax@hello.test" || fin.ObjectKey == "" {
		t.Fatalf("row = %+v", fin)
	}
	if cd := lastCDRFor(t, pbx, "800"); cd.Kind != cdr.KindFax {
		t.Errorf("CDR kind = %q, want fax", cd.Kind)
	}
}
