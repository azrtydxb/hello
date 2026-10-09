package voice

// StoreSource is voice.Source over *store.Store: the store half speaks
// store types (store cannot import voice, the way round), the adapter maps
// them onto the runtime's wire shapes. Safe for concurrent use.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/store"
)

// StoreSource adapts *store.Store to voice.Source.
type StoreSource struct{ St *store.Store }

// NewStoreSource adapts st to voice.Source.
func NewStoreSource(st *store.Store) *StoreSource { return &StoreSource{St: st} }

// VoiceRevision returns the global voice revision (the view's ETag).
func (s *StoreSource) VoiceRevision(ctx context.Context) (int64, error) {
	return s.St.VoiceRevision(ctx)
}

// VoiceRuntimeAgents returns the revision and every enabled agent with its
// attachments, credentials still sealed (spec S-19).
func (s *StoreSource) VoiceRuntimeAgents(ctx context.Context) (Snapshot, error) {
	rev, agents, err := s.St.VoiceRuntimeSnapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Revision: rev, Agents: make([]SealedAgent, 0, len(agents))}
	for _, a := range agents {
		sa, err := sealedAgent(a)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Agents = append(snap.Agents, sa)
	}
	return snap, nil
}

// sealedAgent maps one store row.
func sealedAgent(a store.VoiceRuntimeAgent) (SealedAgent, error) {
	out := SealedAgent{
		Name: a.Name, SIPUser: a.SIPUser,
		Prompt: a.Prompt, Greeting: a.Greeting, Language: a.Language,
		Voice: a.Voice, VoiceReference: a.VoiceReference, Style: a.Style,
		Temperature:        a.Temperature,
		MaxCallSeconds:     a.MaxCallSeconds,
		MaxConcurrent:      a.MaxConcurrent,
		MaxToolCalls:       a.MaxToolCalls,
		IdleTimeoutSeconds: a.IdleTimeoutSeconds,
		Revision:           a.Revision,
		RecordTranscript:   a.RecordTranscript,
		CallerVerification: a.CallerVerification,
		CallerAllowlist:    a.CallerAllowlist,
		PINHash:            a.PINHash,
		Servers:            make([]SealedServer, 0, len(a.Servers)),
	}
	for _, sv := range a.Servers {
		sv := sv
		out.Servers = append(out.Servers, SealedServer{
			ID: sv.ID, Name: sv.Name, URL: sv.URL, Auth: sv.Auth,
			HeaderName: sv.HeaderName, Credential: sv.Credential,
			TokenURL: sv.TokenURL, ClientID: sv.ClientID, Scope: sv.Scope,
			TimeoutMS: sv.TimeoutMS,
			Tools:     make([]ToolAccess, 0, len(sv.Tools)),
		})
		for _, t := range sv.Tools {
			out.Servers[len(out.Servers)-1].Tools = append(
				out.Servers[len(out.Servers)-1].Tools,
				ToolAccess{Name: t.Name, Confirm: t.Confirm, Write: t.Write})
		}
	}
	return out, nil
}

// VoiceRuntimeStatus returns the runtime singleton plus the current voice
// revision and the enabled-agents flag (spec S-22, S-30).
func (s *StoreSource) VoiceRuntimeStatus(ctx context.Context) (RuntimeState, error) {
	st, err := s.St.VoiceRuntimeState(ctx)
	if err != nil {
		return RuntimeState{}, err
	}
	return RuntimeState{
		Revision:         st.Revision,
		AckRevision:      st.AckRevision,
		LastSeen:         st.LastSeen,
		Version:          st.Version,
		Loaded:           json.RawMessage(st.Loaded),
		HasEnabledAgents: st.HasEnabledAgents,
	}, nil
}

// SaveVoiceRuntimeAck records the runtime's last ack, replacing the
// previous one (spec S-20).
func (s *StoreSource) SaveVoiceRuntimeAck(ctx context.Context, accountID int64, at time.Time, ack Ack) error {
	loaded, err := json.Marshal(ack.Agents)
	if err != nil {
		return fmt.Errorf("voice: ack agents: %w", err)
	}
	return s.St.SaveVoiceRuntimeAck(ctx, accountID, at, ack.Revision, ack.Version, loaded)
}

// VoiceAgentPolicy reports how the agent reports calls; store.ErrNotFound
// for an unknown name.
func (s *StoreSource) VoiceAgentPolicy(ctx context.Context, name string) (AgentPolicy, error) {
	rt, err := s.St.VoiceAgentPolicy(ctx, name)
	if err != nil {
		return AgentPolicy{}, err
	}
	return AgentPolicy{RecordTranscript: rt}, nil
}

// SaveVoiceCallReport stores a report idempotently on its correlation id
// (spec S-21) and reports whether it was new.
func (s *StoreSource) SaveVoiceCallReport(ctx context.Context, rep CallReport, at time.Time) (bool, error) {
	tools, err := json.Marshal(rep.ToolCalls)
	if err != nil {
		return false, fmt.Errorf("voice: call report tool calls: %w", err)
	}
	var transcript *string
	if rep.Transcript != "" {
		t := rep.Transcript
		transcript = &t
	}
	return s.St.SaveVoiceAgentCall(ctx, rep.CorrelationID, rep.AgentName, rep.Outcome, rep.Summary,
		tools, rep.TokensIn, rep.TokensOut, transcript, at)
}

// TryVoiceLock takes the prune advisory lock on a connection of its own,
// held until unlock (spec S-23).
func (s *StoreSource) TryVoiceLock(ctx context.Context, key string) (func(), bool, error) {
	return s.St.TryVoiceLock(ctx, key)
}

// VoicePrune clears transcripts past their agent's retention and deletes
// reports past the CDR retention (spec S-23); it returns rows touched.
func (s *StoreSource) VoicePrune(ctx context.Context, now time.Time, cdrRetention time.Duration) (int64, error) {
	return s.St.VoicePrune(ctx, now, cdrRetention)
}
