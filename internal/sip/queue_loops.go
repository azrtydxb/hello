package sip

import (
	"context"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// Background work of the call queues: the BLF lamps of the agent codes and
// the agent gauges.

const (
	// queueBLFEvery is how often the agents' lamps are compared with their
	// state (a change made through the API or another node shows within it).
	queueBLFEvery = 2 * time.Second
	// queueMetricsEvery is how often the agent gauges are published.
	queueMetricsEvery = 5 * time.Second
)

// queueBLFTarget reads a BLF subscription target such as *45101: an agent
// login or pause code followed by the extension number of an agent. It
// reports the code's action and the agent.
func queueBLFTarget(snap *snapshot.Snapshot, user string) (action, agent string, ok bool) {
	for _, fc := range snap.Codes() {
		if fc.Action != ActionQueueLogin && fc.Action != ActionQueuePause {
			continue
		}
		rest, found := strings.CutPrefix(user, fc.Code)
		if !found || rest == "" || !snap.HasExtension(rest) {
			continue
		}
		if len(queuesOf(snap, rest)) == 0 {
			continue
		}
		return fc.Action, rest, true
	}
	return "", "", false
}

// queueLampState is the dialog state a login or pause lamp shows: lit while
// the agent is logged in (or paused), dark otherwise. A static member counts
// as logged in until it says otherwise.
func queueLampState(action string, st queue.AgentState, known, static bool) string {
	switch action {
	case ActionQueuePause:
		if known && st.Paused {
			return StateOnCall
		}
	case ActionQueueLogin:
		if (known && st.LoggedIn) || (!known && static) {
			return StateOnCall
		}
	}
	return StateIdle
}

// queueLamp is the lamp of one BLF subscription target now.
func (s *Server) queueLamp(snap *snapshot.Snapshot, user string, states map[string]queue.AgentState) (string, bool) {
	action, agent, ok := queueBLFTarget(snap, user)
	if !ok {
		return "", false
	}
	st, known := states[agent]
	static := false
	for _, q := range snap.Queues() {
		for _, m := range q.Members {
			if m.Extension == agent && !m.Dynamic {
				static = true
			}
		}
	}
	return queueLampState(action, st, known, static), true
}

// queueLoop runs the queue background work until ctx ends.
func (s *Server) queueLoop(ctx context.Context) {
	st := s.queueStore()
	if st == nil {
		return
	}
	defer contain(s.log, "queue loop")
	last := map[string]string{}
	blf := time.NewTicker(queueBLFEvery)
	defer blf.Stop()
	met := time.NewTicker(queueMetricsEvery)
	defer met.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-blf.C:
			s.queueBLF(ctx, st, last)
		case <-met.C:
			s.queueGauges(ctx, st)
		}
	}
}

// queueBLF notifies the subscribers of every lamp whose state changed.
func (s *Server) queueBLF(ctx context.Context, st queue.Store, last map[string]string) {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	s.subsMu.Lock()
	var users []string
	for u, subs := range s.byExt {
		if len(subs) > 0 && !snap.HasExtension(u) {
			users = append(users, u)
		}
	}
	s.subsMu.Unlock()
	if len(users) == 0 {
		clear(last)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
	states, err := st.Agents(cctx)
	cancel()
	if err != nil {
		return
	}
	for _, u := range users {
		lamp, ok := s.queueLamp(snap, u, states)
		if !ok {
			continue
		}
		if prev, seen := last[u]; seen && prev == lamp {
			continue
		}
		last[u] = lamp
		s.notifyExtension(u, lamp)
	}
}

// queueGauges publishes the agent gauges. Every node could compute them, but
// they describe the cluster, so one node at a time (the holder of the
// metrics lease) reports them and the others report zero.
func (s *Server) queueGauges(ctx context.Context, st queue.Store) {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	states := []string{"available", "on_call", "dnd", "wrapping", "paused", "logged_out"}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
	defer cancel()
	holder, held, err := st.Holder(cctx, queue.MetricsLease)
	if err != nil {
		return
	}
	if held && holder == s.cfg.NodeID {
		_ = st.Release(cctx, queue.MetricsLease, s.cfg.NodeID)
	}
	mine, err := st.Acquire(cctx, queue.MetricsLease, s.cfg.NodeID, 3*queueMetricsEvery)
	for _, q := range snap.Queues() {
		for _, state := range states {
			if !mine || err != nil {
				s.m.QueueAgents.WithLabelValues(q.Name, state).Set(0)
			}
		}
	}
	if !mine || err != nil {
		return
	}
	agents, err := st.Agents(cctx)
	if err != nil {
		return
	}
	busy := s.busyExtensions()
	now := time.Now()
	for _, q := range snap.Queues() {
		stats, _ := st.Stats(cctx, q.Name)
		count := map[string]float64{}
		for _, m := range q.Members {
			as, known := agents[m.Extension]
			e, _ := snap.Extension(m.Extension)
			qm := queue.Member{Ext: m.Extension, Dynamic: m.Dynamic}
			switch {
			case known && as.Paused:
				count["paused"]++
			case !qm.Staffed(q.Name, as, known):
				count["logged_out"]++
			case e.DND:
				count["dnd"]++
			case busy[m.Extension]:
				count["on_call"]++
			case stats[m.Extension].Wrapping(now):
				count["wrapping"]++
			default:
				count["available"]++
			}
		}
		for _, state := range states {
			s.m.QueueAgents.WithLabelValues(q.Name, state).Set(count[state])
		}
	}
}
