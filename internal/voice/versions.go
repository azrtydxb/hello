package voice

// Persona versions (spec S-4): the last 10 personas per agent, and a
// restore that adds a revision instead of rewriting history.

import (
	"context"

	"github.com/azrtydxb/hello/internal/store"
)

// ListVersions returns an agent's saved personas, newest first, an empty
// list for an agent without versions.
func (r *Registry) ListVersions(ctx context.Context, agentID int64) ([]store.VoiceAgentVersion, error) {
	// The agent must exist (404 otherwise).
	if _, err := r.st.GetVoiceAgent(ctx, agentID); err != nil {
		return nil, err
	}
	return r.st.ListVoiceAgentVersions(ctx, agentID)
}

// RestoreVersion applies a saved persona as a new revision (spec S-4): the
// source version is kept, a new version row snapshots the restored persona,
// and the global voice revision moves so talking-agent reloads.
func (r *Registry) RestoreVersion(ctx context.Context, actor string, id, revision int64, check store.Check) (store.VoiceAgent, error) {
	return r.st.RestoreVoiceAgentVersion(ctx, actor, id, revision, check)
}
