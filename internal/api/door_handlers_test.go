package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// doorFake adds the door-open store calls and extension lookup to the
// account fake.
type doorFake struct {
	*acctFake
	dmu   sync.Mutex
	kinds map[int64]string // extension id -> kind
	open  map[int64]store.DoorOpen
	codes map[int64]string // what the store was given, for the leak checks
}

func (f *doorFake) GetExtension(_ context.Context, id int64) (store.Extension, error) {
	f.dmu.Lock()
	defer f.dmu.Unlock()
	k, ok := f.kinds[id]
	if !ok {
		return store.Extension{}, store.ErrNotFound
	}
	return store.Extension{ID: id, Number: "9", Kind: k}, nil
}

func (f *doorFake) GetDoorOpen(_ context.Context, id int64) (store.DoorOpen, error) {
	f.dmu.Lock()
	defer f.dmu.Unlock()
	return f.open[id], nil
}

func (f *doorFake) SetDoorOpen(_ context.Context, _ string, id int64, c store.DoorOpenChange, _ store.Check) (store.DoorOpen, error) {
	f.dmu.Lock()
	defer f.dmu.Unlock()
	d := f.open[id]
	if c.Code != nil {
		f.codes[id] = *c.Code
		d.CodeSet = *c.Code != ""
	}
	if c.HangupAfterOpen != nil {
		d.HangupAfterOpen = *c.HangupAfterOpen
	}
	f.open[id] = d
	return d, nil
}

// doorLive is the live call list.
type doorLive struct {
	noLive
	calls []livestate.Call
}

func (l *doorLive) Calls(context.Context) ([]livestate.Call, error) { return l.calls, nil }

// doorCtl plays the hello-sip node: it records the commands and answers each
// as reply says (nothing at all when silent).
type doorCtl struct {
	mu     sync.Mutex
	sent   []livestate.ParkCommand
	nodes  []string
	reply  livestate.ParkReply
	silent bool
	got    map[string]livestate.ParkReply
}

func (c *doorCtl) ParkSend(_ context.Context, node string, cmd livestate.ParkCommand) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, cmd)
	c.nodes = append(c.nodes, node)
	if !c.silent {
		r := c.reply
		r.ID = cmd.ID
		c.got[cmd.ID] = r
	}
	return nil
}

func (c *doorCtl) ParkGetReply(_ context.Context, id string) (livestate.ParkReply, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.got[id]
	return r, ok, nil
}

func (c *doorCtl) commands() []livestate.ParkCommand {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]livestate.ParkCommand(nil), c.sent...)
}

// doorCallRoutes covers the door-open routes through the whole handler: the
// settings (admin only, code write-only), the user's own door calls, and the
// open-door request, which only the user who answered may make (spec S-47).
func doorCallRoutes(t *testing.T) {
	af, _, _, _ := newAcctFake(t)
	f := &doorFake{acctFake: af, kinds: map[int64]string{7: store.ExtensionDoor, 8: store.ExtensionUser},
		open: map[int64]store.DoorOpen{}, codes: map[int64]string{}}
	started := time.Now()
	live := &doorLive{calls: []livestate.Call{
		{ID: "c-door", From: "900", To: "800", Callee: "101", Door: "900", State: "connected", Node: "n1", StartedAt: started},
		{ID: "c-ring", From: "900", To: "800", Door: "900", State: "ringing", Node: "n1", StartedAt: started},
		{ID: "c-bob", From: "900", To: "800", Callee: "102", Door: "900", State: "connected", Node: "n2", StartedAt: started},
		{ID: "c-plain", From: "101", To: "102", Callee: "102", State: "connected", Node: "n1", StartedAt: started},
		{ID: "c-out", From: "101", To: "900", Callee: "900", Door: "900", State: "connected", Node: "n1", StartedAt: started},
	}}
	ctl := &doorCtl{got: map[string]livestate.ParkReply{}, reply: livestate.ParkReply{OK: true}}
	logs := &bytes.Buffer{}
	h := Handler(Config{Store: f, Live: live, DoorCalls: ctl, DoorOpenWait: 300 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(logs, nil)), SessionTTL: time.Hour})

	t.Run("the settings are admin only and the code is write-only", func(t *testing.T) {
		const code = "9*4#"
		get := func(who, id string) *httptest.ResponseRecorder {
			return acctCall(h, who, "GET", "/api/v1/extensions/"+id+"/door-open", nil)
		}
		if rec := get("dave", "7"); rec.Code != http.StatusOK {
			t.Fatalf("viewer GET = %d %s", rec.Code, rec.Body)
		}
		if rec := get("dave", "8"); rec.Code != http.StatusConflict {
			t.Errorf("GET on a user extension = %d, want 409", rec.Code)
		}
		if rec := get("dave", "99"); rec.Code != http.StatusNotFound {
			t.Errorf("GET on a missing extension = %d, want 404", rec.Code)
		}
		put := func(who, id string, body any) *httptest.ResponseRecorder {
			return acctCall(h, who, "PUT", "/api/v1/extensions/"+id+"/door-open", body)
		}
		if rec := put("dave", "7", map[string]any{"code": code}); rec.Code != http.StatusForbidden {
			t.Errorf("viewer PUT = %d, want 403", rec.Code)
		}
		if rec := put("alice", "7", map[string]any{"code": code}); rec.Code != http.StatusForbidden {
			t.Errorf("user PUT = %d, want 403", rec.Code)
		}
		rec := put("carol", "7", map[string]any{"code": code, "hangupAfterOpen": true})
		got := wantJSON(t, rec, http.StatusOK, "set")
		if got["codeSet"] != true || got["hangupAfterOpen"] != true {
			t.Fatalf("after set = %v", got)
		}
		if strings.Contains(rec.Body.String(), code) {
			t.Error("the response carries the door-open code")
		}
		if got := wantJSON(t, get("carol", "7"), http.StatusOK, "get"); got["codeSet"] != true || len(got) != 2 {
			t.Errorf("GET = %v, want only codeSet and hangupAfterOpen", got)
		}
		for name, body := range map[string]any{
			"empty":      map[string]any{},
			"too long":   map[string]any{"code": "123456789"},
			"letters":    map[string]any{"code": "12ab"},
			"unknown":    map[string]any{"pin": "1"},
			"wrong type": map[string]any{"hangupAfterOpen": "yes"},
		} {
			if rec := put("carol", "7", body); rec.Code != http.StatusBadRequest {
				t.Errorf("PUT %s = %d %s, want 400", name, rec.Code, rec.Body)
			}
		}
		if rec := put("carol", "8", map[string]any{"code": "1"}); rec.Code != http.StatusConflict {
			t.Errorf("PUT on a user extension = %d, want 409", rec.Code)
		}
		if rec := put("carol", "99", map[string]any{"code": "1"}); rec.Code != http.StatusNotFound {
			t.Errorf("PUT on a missing extension = %d, want 404", rec.Code)
		}
		// An empty code clears it.
		if got := wantJSON(t, put("carol", "7", map[string]any{"code": ""}), http.StatusOK, "clear"); got["codeSet"] != false {
			t.Errorf("after clear = %v", got)
		}
		if strings.Contains(logs.String(), code) {
			t.Error("the door-open code reached the log")
		}
		for _, a := range af.audit {
			if strings.Contains(a, code) {
				t.Errorf("audit carries the code: %q", a)
			}
		}
	})

	t.Run("a user lists only the door calls they answered", func(t *testing.T) {
		got := wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/me/door-calls", nil), http.StatusOK, "alice")
		its := nextOf(got)
		if len(its) != 2 || its[0]["id"] != "c-door" || its[0]["door"] != "900" || its[1]["id"] != "c-out" {
			t.Errorf("alice's door calls = %v, want c-door and c-out only", its)
		}
		got = wantJSON(t, acctCall(h, "bob", "GET", "/api/v1/me/door-calls", nil), http.StatusOK, "bob")
		if its := nextOf(got); len(its) != 1 || its[0]["id"] != "c-bob" {
			t.Errorf("bob's door calls = %v, want c-bob only", its)
		}
		rec := acctCall(h, "noext", "GET", "/api/v1/me/door-calls", nil)
		if rec.Code != http.StatusForbidden || acctErrCode(rec) != "no_extension" {
			t.Errorf("a user with no extension: %d %s, want 403 no_extension", rec.Code, rec.Body)
		}
	})

	open := func(who, id string) *httptest.ResponseRecorder {
		return acctCall(h, who, "POST", "/api/v1/calls/"+id+"/door-open", nil)
	}
	t.Run("only the answering user opens the door", func(t *testing.T) {
		if rec := open("alice", "c-door"); rec.Code != http.StatusOK {
			t.Fatalf("the answering user: %d %s, want 200", rec.Code, rec.Body)
		}
		cmds := ctl.commands()
		if len(cmds) != 1 || cmds[0].Op != livestate.ParkOpDoorOpen || cmds[0].Correlation != "c-door" || cmds[0].Retriever != "101" {
			t.Fatalf("queued commands = %+v, want one door_open for c-door by 101", cmds)
		}
		if ctl.nodes[0] != "n1" {
			t.Errorf("the command went to node %q, want the call's node n1", ctl.nodes[0])
		}
		// The user who placed the call to a station may open it too.
		if rec := open("alice", "c-out"); rec.Code != http.StatusOK {
			t.Errorf("the caller of a station: %d %s, want 200", rec.Code, rec.Body)
		}
		before := len(ctl.commands())
		for _, c := range []struct {
			who, id string
			want    int
		}{
			{"bob", "c-door", http.StatusForbidden},  // another user's call
			{"alice", "c-bob", http.StatusForbidden}, // answered by someone else
			{"alice", "c-ring", http.StatusConflict}, // not answered yet
			{"alice", "c-plain", http.StatusConflict},
			{"alice", "nope", http.StatusNotFound},
			{"noext", "c-door", http.StatusForbidden},
		} {
			if rec := open(c.who, c.id); rec.Code != c.want {
				t.Errorf("%s %s: %d %s, want %d", c.who, c.id, rec.Code, rec.Body, c.want)
			}
		}
		if n := len(ctl.commands()); n != before {
			t.Errorf("%d commands were queued for refused requests", n-before)
		}
		for _, a := range af.audit {
			if strings.Contains(a, "door_open call c-door") {
				return
			}
		}
		t.Errorf("no audit row for the door open: %v", af.audit)
	})

	t.Run("the node's refusals and silence", func(t *testing.T) {
		ctl.mu.Lock()
		ctl.reply = livestate.ParkReply{Code: 409, Reason: "the door station has no door-open code"}
		ctl.mu.Unlock()
		rec := open("alice", "c-door")
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no door-open code") {
			t.Errorf("node 409: %d %s, want 409 with the reason", rec.Code, rec.Body)
		}
		ctl.mu.Lock()
		ctl.reply = livestate.ParkReply{Code: 404, Reason: "the call is gone"}
		ctl.mu.Unlock()
		if rec := open("alice", "c-door"); rec.Code != http.StatusNotFound {
			t.Errorf("node 404: %d, want 404", rec.Code)
		}
		ctl.mu.Lock()
		ctl.reply = livestate.ParkReply{Code: 403}
		ctl.mu.Unlock()
		if rec := open("alice", "c-door"); rec.Code != http.StatusForbidden {
			t.Errorf("node 403: %d, want 403", rec.Code)
		}
		ctl.mu.Lock()
		ctl.reply = livestate.ParkReply{Code: 500}
		ctl.mu.Unlock()
		if rec := open("alice", "c-door"); rec.Code != http.StatusBadGateway {
			t.Errorf("node failure: %d, want 502", rec.Code)
		}
		ctl.mu.Lock()
		ctl.silent = true
		ctl.mu.Unlock()
		if rec := open("alice", "c-door"); rec.Code != http.StatusGatewayTimeout {
			t.Errorf("no answer: %d, want 504", rec.Code)
		}
	})

	t.Run("no Valkey control answers 503", func(t *testing.T) {
		h2 := Handler(Config{Store: f, Live: live, Log: slog.New(slog.NewTextHandler(logs, nil)), SessionTTL: time.Hour})
		if rec := acctCall(h2, "alice", "POST", "/api/v1/calls/c-door/door-open", nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("without DoorCalls: %d, want 503", rec.Code)
		}
	})
}
