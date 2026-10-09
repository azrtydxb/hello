package api

// The voice MCP server operations (spec voice-agents S-5 to S-9): the
// credential is write-only and admin-only; discovery and test are guarded
// egress.

import (
	"net/http"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/voice"
)

// voiceServerJSON is the response body of one MCP server, without its
// credential material (spec S-5). Mutation check: dropping the filter —
// returning the sealed bytes or the unsealed value on a read — fails the
// body-scanning assertions of TestVoiceMCPServers.
type voiceServerJSON struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	URL             string     `json:"url"`
	Auth            string     `json:"auth"`
	HeaderName      string     `json:"headerName"`
	TokenURL        string     `json:"tokenUrl"`
	ClientID        string     `json:"clientId"`
	Scope           string     `json:"scope"`
	CredentialSet   bool       `json:"credentialSet"`
	TimeoutMS       int        `json:"timeoutMs"`
	Enabled         bool       `json:"enabled"`
	LastCheckAt     *time.Time `json:"lastCheckAt"`
	LastCheckStatus string     `json:"lastCheckStatus"`
	UsedBy          []string   `json:"usedBy"`
}

func serverBody(v store.VoiceMCPServer, usedBy []string) voiceServerJSON {
	out := voiceServerJSON{
		ID: v.ID, Name: v.Name, URL: v.URL, Auth: v.Auth, HeaderName: v.HeaderName, TokenURL: v.TokenURL,
		ClientID: v.ClientID, Scope: v.Scope, CredentialSet: v.CredentialSet, TimeoutMS: v.TimeoutMS,
		Enabled: v.Enabled, LastCheckAt: v.LastCheckAt, LastCheckStatus: v.LastCheckStatus,
		UsedBy: []string{},
	}
	out.UsedBy = append(out.UsedBy, usedBy...)
	return out
}

// voiceServerIn is the create and update body; clientSecret is accepted as
// the credential's name for the OAuth auth.
type voiceServerIn struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	Auth         string `json:"auth"`
	HeaderName   string `json:"headerName"`
	TokenURL     string `json:"tokenUrl"`
	ClientID     string `json:"clientId"`
	Scope        string `json:"scope"`
	Credential   string `json:"credential"`
	ClientSecret string `json:"clientSecret"`
	TimeoutMS    int    `json:"timeoutMs"`
	Enabled      *bool  `json:"enabled"`
}

// input converts the body; clientSecret is the credential under its
// document name.
func (in voiceServerIn) input() voice.ServerInput {
	cred := in.Credential
	if in.Auth == "oauth_client_credentials" && in.ClientSecret != "" {
		cred = in.ClientSecret
	}
	return voice.ServerInput{
		Name: in.Name, URL: in.URL, Auth: in.Auth, HeaderName: in.HeaderName, TokenURL: in.TokenURL,
		ClientID: in.ClientID, Scope: in.Scope, Credential: cred, TimeoutMS: in.TimeoutMS,
		Enabled: in.Enabled == nil || *in.Enabled,
	}
}

func (s *server) listVoiceMCPServers(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	vs, used, err := s.Voice.MCPServers(r.Context())
	if err != nil {
		s.voiceError(w, "voice mcp servers", err)
		return
	}
	// used is "serverName:agentName" pairs from the store; regroup them per
	// server by name.
	byName := map[string][]string{}
	for _, u := range used {
		server, agent, found := strings.Cut(u, ":")
		if found {
			byName[server] = append(byName[server], agent)
		}
	}
	out := make([]voiceServerJSON, 0, len(vs))
	for _, v := range vs {
		out = append(out, serverBody(v, byName[v.Name]))
	}
	writeJSON(w, http.StatusOK, items(out))
}

func (s *server) createVoiceMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	var in voiceServerIn
	if !decode(w, r, &in) {
		return
	}
	v, err := s.Voice.CreateMCPServer(r.Context(), actor(r).Username, in.input(), nil)
	if err != nil {
		s.voiceError(w, "voice mcp server", err)
		return
	}
	writeJSON(w, http.StatusCreated, serverBody(v, nil))
}

func (s *server) getVoiceMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, used, err := s.Voice.MCPServer(r.Context(), id)
	if err != nil {
		s.voiceError(w, "voice mcp server", err)
		return
	}
	writeJSON(w, http.StatusOK, serverBody(v, used))
}

func (s *server) updateVoiceMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in voiceServerIn
	if !decode(w, r, &in) {
		return
	}
	v, err := s.Voice.UpdateMCPServer(r.Context(), actor(r).Username, id, in.input(), nil)
	if err != nil {
		s.voiceError(w, "voice mcp server", err)
		return
	}
	writeJSON(w, http.StatusOK, serverBody(v, nil))
}

func (s *server) deleteVoiceMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Voice.DeleteMCPServer(r.Context(), actor(r).Username, id, nil); err != nil {
		s.voiceError(w, "voice mcp server", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) discoverVoiceMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	// Egress refusals and unreachable peers answer 503 with the spec's
	// voice error codes; discovery itself is a read of the server.
	if !ok {
		return
	}
	tools, err := s.Voice.Discover(r.Context(), id)
	if err != nil {
		s.voiceError(w, "voice mcp discovery", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": tools})
}

func (s *server) testVoiceMCPServer(w http.ResponseWriter, r *http.Request) {
	if !s.voiceReady(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// The optional body names a tool to call; a test without one is the
	// tools/list of S-9. Either way the result is a 200 body.
	if r.ContentLength != 0 {
		var in struct {
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		}
		if !decode(w, r, &in) {
			return
		}
	}
	// A test of a server that is not there is a 404; a test of one that is
	// there is a result, whatever the peer did (spec S-9).
	if _, _, err := s.Voice.MCPServer(r.Context(), id); err != nil {
		s.voiceError(w, "voice mcp test", err)
		return
	}
	res, err := s.Voice.TestConnection(r.Context(), id)
	if err != nil {
		s.voiceError(w, "voice mcp test", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": res.Status, "latencyMs": res.LatencyMS, "toolCount": res.Tools,
	})
}
