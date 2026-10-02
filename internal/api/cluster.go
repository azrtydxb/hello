package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/azrtydxb/hello/internal/cluster"
)

// ClusterStore is node membership and drain requests; *cluster.Store and
// *LazyValkey implement it.
type ClusterStore interface {
	Members(ctx context.Context) ([]cluster.Member, error)
	RequestDrain(ctx context.Context, id string) error
	CancelDrain(ctx context.Context, id string) error
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
// dependency down, because reporting that is its job: members are empty
// while Valkey is unreachable, and revision and lags are absent while
// PostgreSQL is.
func (s *server) clusterOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	rev, pg := s.revision(ctx)
	vh := ValkeyHealth{Mode: "single", Error: "not configured"}
	if s.Valkey != nil {
		vh = s.Valkey.ValkeyHealth(ctx)
	}
	var ms []cluster.Member
	if s.Cluster != nil && vh.Up {
		var err error
		if ms, err = s.Cluster.Members(ctx); err != nil {
			s.Log.Warn("cluster view: members unavailable", "error", err)
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
// would get 503.
func leavesNoReadySIP(target cluster.Member, ms []cluster.Member) bool {
	if target.Kind != cluster.KindSIP {
		return false
	}
	for _, m := range ms {
		if m.Kind == cluster.KindSIP && m.State == cluster.Ready && m.ID != target.ID {
			return false
		}
	}
	return true
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
	target, ms, ok := s.drainTarget(w, r, ctx)
	if !ok {
		return
	}
	if target.State == cluster.Offline {
		writeError(w, http.StatusConflict, "conflict", "node "+target.ID+" is OFFLINE; a drain request would drain its next start")
		return
	}
	action := "drain"
	if leavesNoReadySIP(target, ms) {
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

// cancelDrain is DELETE /api/v1/cluster/nodes/{id}/drain.
func (s *server) cancelDrain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), liveTimeout)
	defer cancel()
	target, _, ok := s.drainTarget(w, r, ctx)
	if !ok {
		return
	}
	if err := s.Cluster.CancelDrain(ctx, target.ID); err != nil {
		s.liveDown(w, "cancel drain", err)
		return
	}
	// As with drain: an unaudited cancel is undone.
	if err := s.Store.Audit(r.Context(), actor(r).String(), "undrain", "node", target.ID); err != nil {
		if rerr := s.Cluster.RequestDrain(context.WithoutCancel(r.Context()), target.ID); rerr != nil {
			s.Log.Error("undrain: audit failed and the drain could not be restored", "node", target.ID, "error", rerr)
		}
		s.internal(w, "undrain: audit", err)
		return
	}
	s.Log.Info("drain cancelled", "node", target.ID, "actor", actor(r).String())
	w.WriteHeader(http.StatusNoContent)
}
