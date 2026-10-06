package sip

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// attemptState is the fake live state plus the optional REGISTER-attempt
// recorder the registrar writes to.
type attemptState struct {
	*fakeState
	mu       sync.Mutex
	attempts []livestate.RegisterAttempt
}

func (a *attemptState) RecordRegisterAttempt(_ context.Context, at livestate.RegisterAttempt) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attempts = append(a.attempts, at)
	return nil
}

func (a *attemptState) recorded() []livestate.RegisterAttempt {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]livestate.RegisterAttempt(nil), a.attempts...)
}

// TestRegisterAttemptsRecorded fails if the registrar does not record each
// REGISTER's final response for an enabled device (challenge, wrong
// password, success) with its source, node and whether it carried
// credentials, or if it records REGISTERs for a username that is not an
// enabled device (which would let clients grow the keyspace).
func TestRegisterAttemptsRecorded(t *testing.T) {
	st := &attemptState{fakeState: newFakeState()}
	pbx := startPBX(t, []snapshot.Device{dev(1, "alice", "100", "pw")}, func(_ *Config, d *Deps) { d.State = st })

	p := newPhone(t, pbx, "alice", "pw")
	if res := p.authDo(p.registerReq(300), AlgSHA256, "wrong"); res.StatusCode != 401 {
		t.Fatalf("wrong password = %d, want 401 (rechallenge)", res.StatusCode)
	}
	p.register(t)

	// The registrar records an attempt after sending its response, so the
	// last record can land just after the phone has its 200.
	got := st.recorded()
	for deadline := time.Now().Add(2 * time.Second); len(got) < 4 && time.Now().Before(deadline); got = st.recorded() {
		time.Sleep(5 * time.Millisecond)
	}
	// unauthenticated 401, credentials 401 (wrong password), unauthenticated 401, credentials 200
	want := []struct {
		code  int
		creds bool
	}{{401, false}, {401, true}, {401, false}, {200, true}}
	if len(got) != len(want) {
		t.Fatalf("recorded %d attempts %+v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		a := got[i]
		if a.Code != w.code || a.Credentials != w.creds || a.Device != "alice" || a.Node != "sip-test" ||
			a.Source != p.addr || a.IP != "127.0.0.1" || a.Stale || a.At.IsZero() {
			t.Fatalf("attempt %d = %+v, want code %d credentials %v from %s", i, a, w.code, w.creds, p.addr)
		}
	}

	m := newPhone(t, pbx, "mallory", "pw")
	if res := m.authDo(m.registerReq(300), AlgSHA256, "pw"); res.StatusCode != 403 {
		t.Fatalf("unknown device = %d, want 403", res.StatusCode)
	}
	if n := len(st.recorded()); n != len(want) {
		t.Fatalf("recorded %d attempts after an unknown device registered, want still %d", n, len(want))
	}
}
