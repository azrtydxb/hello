package replay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// users is an auth.Lookup holding only users: an agent replay carries no
// credential, so only UserActor is ever asked.
type users struct {
	mu sync.Mutex
	m  map[int64]auth.Actor
}

func (u *users) UserActor(_ context.Context, id int64) (auth.Actor, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if a, ok := u.m[id]; ok {
		return a, nil
	}
	return auth.Actor{}, auth.ErrNoCredentials
}

func (u *users) set(id int64, a auth.Actor) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.m[id] = a
}

func (u *users) remove(id int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.m, id)
}

func (*users) SessionActor(context.Context, []byte) (auth.Actor, error) {
	return auth.Actor{}, auth.ErrNoCredentials
}

func (*users) TokenActor(context.Context, []byte) (auth.Actor, error) {
	return auth.Actor{}, auth.ErrNoCredentials
}

// syncBuffer is a log sink the replay goroutine and the test share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestAgentIdentity (replay half, spec ai-agent S-7) sends agent replays
// through Do into the real auth middleware. It fails if a request header can
// set the agent marker, an agent replay gets any scope but read, a deleted
// user's agent replay is admitted, a demotion does not apply on the next
// call, a replay can start a replay, or an agent read's log line lacks
// via=ai-assistant. The middleware half is TestAgentIdentity in
// internal/auth.
func TestAgentIdentity(t *testing.T) {
	l := &users{m: map[int64]auth.Actor{1: {UserID: 1, Username: "ann", Role: auth.RoleOperator}}}
	var logs syncBuffer
	var (
		mu     sync.Mutex
		seen   auth.Actor
		nested error
	)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, _ := auth.ActorFrom(r.Context())
		// A replayed request tries to replay again, as an injected tool
		// call would.
		_, err := Do(r.Context(), http.NotFoundHandler(), Request{Method: http.MethodGet, Path: "/api/v1/users"})
		mu.Lock()
		seen, nested = a, err
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	h := auth.Middleware(l, auth.Options{Cookies: true}, slog.New(slog.NewTextHandler(&logs, nil)))(inner)
	agent := auth.WithAgent(context.Background(), auth.Agent{UserID: 1, TaskID: "task-1"})
	read := func(ctx context.Context, hdr http.Header) (int, auth.Actor, error) {
		t.Helper()
		mu.Lock()
		seen, nested = auth.Actor{}, nil
		mu.Unlock()
		res, err := Do(ctx, h, Request{Method: http.MethodGet, Path: "/api/v1/extensions", Header: hdr})
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return res.Status, seen, nested
	}

	t.Run("no header sets the marker", func(t *testing.T) {
		hdr := http.Header{}
		for _, k := range []string{"X-Hello-Agent", "X-Hello-Agent-User", "X-Hello-Task", "Via", "X-Hello-Replay"} {
			hdr.Set(k, "1")
		}
		if code, a, _ := read(context.Background(), hdr); code != http.StatusUnauthorized || a.Kind == auth.KindAgent {
			t.Fatalf("replay with agent-looking headers = %d as %+v, want 401", code, a)
		}
	})

	t.Run("read only, as the user, logged with the via", func(t *testing.T) {
		code, a, _ := read(agent, nil)
		if code != http.StatusOK || a.Kind != auth.KindAgent || a.UserID != 1 || a.Role != auth.RoleOperator {
			t.Fatalf("agent replay = %d as %+v", code, a)
		}
		if len(a.Scopes) != 1 || !a.Scopes.Has(auth.ScopeRead) {
			t.Errorf("agent scopes = %v, want read only", a.Scopes)
		}
		var line string
		for l := range strings.SplitSeq(logs.String(), "\n") {
			if strings.Contains(l, "path=/api/v1/extensions") {
				line = l
			}
		}
		if !strings.Contains(line, "via=ai-assistant") || !strings.Contains(line, "actor=user:ann") || !strings.Contains(line, "task=task-1") {
			t.Errorf("agent read log line = %q, want via=ai-assistant with the user and task", line)
		}
	})

	t.Run("a replay cannot start a replay", func(t *testing.T) {
		if _, _, err := read(agent, nil); !errors.Is(err, ErrNested) {
			t.Fatalf("replay from inside a replay = %v, want ErrNested", err)
		}
	})

	t.Run("demotion applies on the next call", func(t *testing.T) {
		l.set(1, auth.Actor{UserID: 1, Username: "ann", Role: auth.RoleViewer})
		if code, a, _ := read(agent, nil); code != http.StatusOK || a.Role != auth.RoleViewer {
			t.Fatalf("after demotion = %d as %+v, want the viewer role", code, a)
		}
	})

	t.Run("a deleted user's replay is refused", func(t *testing.T) {
		l.remove(1)
		if code, a, _ := read(agent, nil); code != http.StatusUnauthorized || a.Kind == auth.KindAgent {
			t.Fatalf("deleted user's agent replay = %d as %+v, want 401", code, a)
		}
	})
}
