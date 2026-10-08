package ai

import (
	"context"
	"strconv"
	"time"
)

// HeartbeatKey is a replica's Valkey heartbeat (spec S-15), refreshed every
// 5 s with a 15 s TTL: a replica whose key is gone stopped.
func HeartbeatKey(replica string) string { return "hello:ai:heartbeat:" + replica }

// heartbeatLoop refreshes this replica's heartbeat and fails the tasks of
// replicas whose heartbeat expired.
func (s *Service) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(s.timing.heartbeat)
	defer t.Stop()
	for {
		s.beat(ctx)
		s.failStale(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) beat(ctx context.Context) {
	vk := s.valkey()
	if vk == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := vk.B().Set().Key(HeartbeatKey(s.replica)).Value(strconv.FormatInt(s.now().UnixMilli(), 10)).
		Px(s.timing.stale).Build()
	if err := vk.Do(ctx, cmd).Error(); err != nil {
		s.log.Warn("ai: heartbeat", "error", err)
	}
}

// failStale fails, with instance_stopped, the unfinished tasks of every
// other replica whose heartbeat is gone. Tasks younger than the stale
// window are left alone (their replica may not have beaten yet), and with
// Valkey unreachable nothing is failed: only this replica's own tasks are
// known to be alive.
func (s *Service) failStale(ctx context.Context) {
	vk := s.valkey()
	if vk == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	replicas, err := s.st.AITaskReplicas(ctx, s.now().Add(-s.timing.stale))
	if err != nil {
		s.log.Warn("ai: list task replicas", "error", err)
		return
	}
	for _, r := range replicas {
		if r == s.replica {
			continue
		}
		n, err := vk.Do(ctx, vk.B().Exists().Key(HeartbeatKey(r)).Build()).AsInt64()
		if err != nil {
			return
		}
		if n > 0 {
			continue
		}
		failed, err := s.st.FailAITasks(ctx, r, CodeInstanceStopped, "the instance running it stopped", s.now())
		if err != nil {
			s.log.Warn("ai: fail stale tasks", "replica", r, "error", err)
			continue
		}
		if failed > 0 {
			s.log.Info("ai: failed tasks of a stopped replica", "replica", r, "tasks", failed)
		}
	}
}
