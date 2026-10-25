package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/azrtydxb/hello/internal/flow"
)

// maxFlowNodes caps a whole-configuration graph (spec flow-canvas, failure
// modes); a larger one is asked for as a closure.
const maxFlowNodes = 5000

// flowReadAttempts bounds the retries when the configuration changes while
// the graph is being read.
const flowReadAttempts = 3

// getFlowCatalogue is GET /api/v1/flow/catalogue: the node and port types
// and which output may connect to which input.
func (s *server) getFlowCatalogue(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, flow.Get())
}

// getFlowRevision is GET /api/v1/flow/revision: the token the canvas polls.
func (s *server) getFlowRevision(w http.ResponseWriter, r *http.Request) {
	rev, err := s.Store.ConfigRevision(r.Context())
	if err != nil {
		s.internal(w, "read config revision", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configRevision": rev})
}

// getFlowGraph is GET /api/v1/flow/graph: the configuration drawn as nodes
// and edges. It reads every table, so the revision is read before and after
// and the read is repeated when it moved.
func (s *server) getFlowGraph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opt := flow.Options{}
	switch sc := q.Get("scope"); {
	case sc == "" || sc == "all":
	case strings.HasPrefix(sc, "closure:") && len(sc) > len("closure:"):
		opt.Closure = strings.TrimPrefix(sc, "closure:")
	default:
		badRequest(w, `scope must be "all" or "closure:<nodeId>"`)
		return
	}
	switch inc := q.Get("include"); inc {
	case "":
	case "devices":
		opt.Devices = true
	default:
		badRequest(w, `include must be "devices"`)
		return
	}
	for range flowReadAttempts {
		before, err := s.Store.ConfigRevision(r.Context())
		if err != nil {
			s.internal(w, "read config revision", err)
			return
		}
		in, err := s.flowInput(r, opt.Devices)
		if err != nil {
			s.internal(w, "read configuration for the flow graph", err)
			return
		}
		after, err := s.Store.ConfigRevision(r.Context())
		if err != nil {
			s.internal(w, "read config revision", err)
			return
		}
		if before != after {
			continue
		}
		in.Revision = after
		g, err := flow.Build(in, opt)
		if errors.Is(err, flow.ErrUnknownNode) {
			writeError(w, http.StatusNotFound, "not_found", "flow node: not found")
			return
		}
		if err != nil {
			s.internal(w, "build the flow graph", err)
			return
		}
		if opt.Closure == "" && len(g.Nodes) > maxFlowNodes {
			writeError(w, http.StatusRequestEntityTooLarge, "graph_too_large",
				"the configuration has too many nodes for one graph; use scope=closure:<nodeId>")
			return
		}
		writeJSON(w, http.StatusOK, g)
		return
	}
	writeError(w, http.StatusServiceUnavailable, "config_changing", "the configuration kept changing; try again")
}

func (s *server) flowInput(r *http.Request, devices bool) (flow.Input, error) {
	ctx, st := r.Context(), s.Store
	var in flow.Input
	var err error
	if in.Trunks, err = st.ListTrunks(ctx); err != nil {
		return in, err
	}
	if in.Inbound, err = st.ListInboundRoutes(ctx); err != nil {
		return in, err
	}
	if in.Outbound, err = st.ListOutboundRoutes(ctx); err != nil {
		return in, err
	}
	if in.Extensions, err = st.ListExtensions(ctx); err != nil {
		return in, err
	}
	if in.RingGroups, err = st.ListRingGroups(ctx); err != nil {
		return in, err
	}
	if in.PagingGroups, err = st.ListPagingGroups(ctx); err != nil {
		return in, err
	}
	if in.Announcements, err = st.ListAnnouncements(ctx); err != nil {
		return in, err
	}
	if in.FeatureCodes, err = st.ListFeatureCodes(ctx); err != nil {
		return in, err
	}
	if devices {
		if in.Devices, err = st.ListDevices(ctx); err != nil {
			return in, err
		}
		if s.ProvStore != nil {
			if in.Phones, err = s.ProvStore.ListPhones(ctx); err != nil {
				return in, err
			}
		}
	}
	return in, nil
}
