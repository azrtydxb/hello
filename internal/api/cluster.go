package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sync"

	"github.com/azrtydxb/hello/internal/cluster"
)

// ClusterStore is node membership and drain requests; *LazyValkey
// implements it.
type ClusterStore interface {
	Members(ctx context.Context) ([]cluster.Member, error)
	RequestDrain(ctx context.Context, id string) error
	CancelDrain(ctx context.Context, id string) error
	DrainRequested(ctx context.Context, id string) (bool, error)
	// LockDrains takes the cluster-wide drain lock, waiting for it until ctx
	// ends; unlock releases it. Drain requests check-and-set under it, so
	// two concurrent requests (on any hello-control node) cannot both pass
	// the last-READY-SIP-node guard.
	LockDrains(ctx context.Context) (unlock func(), err error)
}

// ValkeyStatus reports Valkey health for the cluster view.
type ValkeyStatus interface {
	ValkeyHealth(ctx context.Context) ValkeyHealth
}

// nodeIDRe is the shape of HELLO_NODE_ID values the API accepts in paths.
var nodeIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// memberView is a member with its configuration revision lag; the lag is
// absent when the current revision cannot be read.
type memberView struct {
	cluster.Member
	RevisionLag *int64 `json:"revisionLag,omitempty"`
}

type postgresHealth struct {
	Up    bool   `json:"up"`
	Error string `json:"error,omitempty"`
}

func views(ms []cluster.Member, rev *int64) []memberView {
	out := make([]memberView, 0, len(ms))
	for _, m := range ms {
		v := memberView{Member: m}
		if rev != nil {
			lag := *rev - m.ConfigRevision
			v.RevisionLag = &lag
		}
		out = append(out, v)
	}
	return out
}

// revision reads the configuration revision; its failure is PostgreSQL's
// health. The error detail goes to the log, not the response.
func (s *server) revision(ctx context.Context) (*int64, postgresHealth) {
	rev, err := s.Store.ConfigRevision(ctx)
	if err != nil {
		s.Log.Warn("cluster view: postgres unavailable", "error", err)
		return nil, postgresHealth{Error: "unavailable"}
	}
	return &rev, postgresHealth{Up: true}
}

// clusterOverview is GET /api/v1/cluster. It answers 200 even with a
// dependency down, because reporting that is the endpoint's job: members
// are empty while Valkey is unreachable, and revision and lags are absent
// while PostgreSQL is. The reads run concurrently, each under its own
// bounded context, so a slow one cannot starve the rest: a PostgreSQL-only
// outage must not make the endpoint report Valkey down and empty members.
func (s *server) clusterOverview(w http.ResponseWriter, r *http.Request) {
	var (
		wg     sync.WaitGroup
		rev    *int64
		pg     postgresHealth
		vh     = ValkeyHealth{Mode: "single", Error: "not configured"}
		ms     []cluster.Member
		memErr error
	)
	if s.Valkey != nil || s.Cluster != nil {
		ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
		defer cancel()
		// Each read bounds itself, so one dependency's slowness cannot
		// spend the whole budget before the others start.
		read := func(f func(context.Context)) {
			cctx, cancel := context.WithTimeout(ctx, liveTimeout)
			wg.Go(func() {
				defer cancel()
				f(cctx)
			})
		}
		read(func(cctx context.Context) { rev, pg = s.revision(cctx) })
		if s.Valkey != nil {
			read(func(cctx context.Context) { vh = s.Valkey.ValkeyHealth(cctx) })
		}
		if s.Cluster != nil {
			read(func(cctx context.Context) { ms, memErr = s.Cluster.Members(cctx) })
		}
	}
	wg.Wait()
	if memErr != nil {
		s.Log.Warn("cluster view: members unavailable", "error", memErr)
		// A members failure on a Valkey that is up is reported as Valkey's
		// loss; the health check's own error stands otherwise.
		if vh.Up {
			vh.Up, vh.Error = false, "members unavailable"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"members": views(ms, rev), "postgres": pg, "valkey": vh, "configRevision": rev,
	})
}

// members reads membership, answering 503 itself when Valkey is down.
func (s *server) members(w http.ResponseWriter, ctx context.Context) ([]cluster.Member, bool) {
	if s.Cluster == nil {
		s.liveDown(w, "cluster members", errors.New("no membership store configured"))
		return nil, false
	}
	ms, err := s.Cluster.Members(ctx)
	if err != nil {
		s.liveDown(w, "cluster members", err)
		return nil, false
	}
	return ms, true
}

func (s *server) clusterNodes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	ms, ok := s.members(w, ctx)
	if !ok {
		return
	}
	rev, _ := s.revision(ctx)
	writeJSON(w, http.StatusOK, items(views(ms, rev)))
}

// drainTarget finds node {id}: 404 when unknown, 409 when OFFLINE (a drain
// request has no TTL, so it would drain the node's next start).
func (s *server) drainTarget(w http.ResponseWriter, r *http.Request, ctx context.Context) (cluster.Member, []cluster.Member, bool) {
	id := r.PathValue("id")
	if !nodeIDRe.MatchString(id) {
		writeError(w, http.StatusNotFound, "not_found", "node: not found")
		return cluster.Member{}, nil, false
	}
	ms, ok := s.members(w, ctx)
	if !ok {
		return cluster.Member{}, nil, false
	}
	for _, m := range ms {
		if m.ID == id {
			return m, ms, true
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "node "+id+": not found")
	return cluster.Member{}, nil, false
}

// leavesNoReadySIP reports whether draining target would leave no READY
// SIP node: Kamailio would then have no destination and every new call
// would get 503. A node with a drain request is not counted as READY even
// while its published state still says so: it acts on the request only at
// its next heartbeat.
func (s *server) leavesNoReadySIP(ctx context.Context, target cluster.Member, ms []cluster.Member) (bool, error) {
	if target.Kind != cluster.KindSIP {
		return false, nil
	}
	for _, m := range ms {
		if m.Kind != cluster.KindSIP || m.State != cluster.Ready || m.ID == target.ID {
			continue
		}
		drained, err := s.Cluster.DrainRequested(ctx, m.ID)
		if err != nil {
			return false, err
		}
		if !drained {
			return false, nil
		}
	}
	return true, nil
}

// requestDrain is POST /api/v1/cluster/nodes/{id}/drain[?force=true].
func (s *server) requestDrain(w http.ResponseWriter, r *http.Request) {
	force := false
	switch r.URL.Query().Get("force") {
	case "":
	case "true":
		force = true
	default:
		writeFields(w, fieldErrs{{Path: "force", Message: `must be "true" or absent`}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	if !nodeIDRe.MatchString(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not_found", "node: not found")
		return
	}
	if s.Cluster == nil {
		s.liveDown(w, "request drain", errors.New("no membership store configured"))
		return
	}
	// Check and write under the drain lock, so a concurrent request cannot
	// pass the guard against the same membership.
	unlock, err := s.Cluster.LockDrains(ctx)
	if err != nil {
		s.liveDown(w, "drain lock", err)
		return
	}
	defer unlock()
	target, ms, ok := s.drainTarget(w, r, ctx)
	if !ok {
		return
	}
	if target.State == cluster.Offline {
		writeError(w, http.StatusConflict, "conflict", "node "+target.ID+" is OFFLINE; a drain request would drain its next start")
		return
	}
	action := "drain"
	last, err := s.leavesNoReadySIP(ctx, target, ms)
	if err != nil {
		s.liveDown(w, "drain requests", err)
		return
	}
	if last {
		if !force {
			writeError(w, http.StatusConflict, "conflict", "draining "+target.ID+
				" would leave no READY SIP node, so new calls would fail; repeat with ?force=true to drain anyway")
			return
		}
		action = "drain-force"
	}
	if err := s.Cluster.RequestDrain(ctx, target.ID); err != nil {
		s.liveDown(w, "request drain", err)
		return
	}
	// Every drain in effect is audited: if the audit row cannot be written,
	// the request is withdrawn and the call fails.
	if err := s.Store.Audit(r.Context(), actor(r).String(), action, "node", target.ID); err != nil {
		if cerr := s.Cluster.CancelDrain(context.WithoutCancel(r.Context()), target.ID); cerr != nil {
			s.Log.Error("drain: audit failed and the request could not be withdrawn", "node", target.ID, "error", cerr)
		}
		s.internal(w, "drain: audit", err)
		return
	}
	s.Log.Info("drain requested", "node", target.ID, "actor", actor(r).String(), "forced", action == "drain-force")
	w.WriteHeader(http.StatusNoContent)
}

// cancelDrain is DELETE /api/v1/cluster/nodes/{id}/drain. It does not
// look the node up: a request for a node whose record and tombstone have
// expired (killed while draining) must still be removable. 409 when there
// is no request to withdraw, e.g. a node draining from SIGTERM.
func (s *server) cancelDrain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	id := r.PathValue("id")
	if !nodeIDRe.MatchString(id) {
		writeError(w, http.StatusNotFound, "not_found", "node: not found")
		return
	}
	if s.Cluster == nil {
		s.liveDown(w, "cancel drain", errors.New("no membership store configured"))
		return
	}
	requested, err := s.Cluster.DrainRequested(ctx, id)
	if err != nil {
		s.liveDown(w, "cancel drain", err)
		return
	}
	if !requested {
		writeError(w, http.StatusConflict, "conflict", "node "+id+" is not drained by request; nothing to cancel")
		return
	}
	if err := s.Cluster.CancelDrain(ctx, id); err != nil {
		s.liveDown(w, "cancel drain", err)
		return
	}
	// As with drain: an unaudited cancel is undone.
	if err := s.Store.Audit(r.Context(), actor(r).String(), "undrain", "node", id); err != nil {
		if rerr := s.Cluster.RequestDrain(context.WithoutCancel(r.Context()), id); rerr != nil {
			s.Log.Error("undrain: audit failed and the drain could not be restored", "node", id, "error", rerr)
		}
		s.internal(w, "undrain: audit", err)
		return
	}
	s.Log.Info("drain cancelled", "node", id, "actor", actor(r).String())
	w.WriteHeader(http.StatusNoContent)
}
