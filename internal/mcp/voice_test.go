package mcp

import "testing"

// TestVoiceOperationsMCP (spec voice-agents S-18, S-31) fails if a runtime
// operation, a credential or an egress operation is an MCP tool, or if a
// read of the agents or the runtime status is not.
func TestVoiceOperationsMCP(t *testing.T) {
	ops := loadSpec(t).Operations()
	tools, _, err := buildTools(ops)
	if err != nil {
		t.Fatal(err)
	}
	exposed := map[string]bool{}
	for _, tl := range tools {
		exposed[tl.op.ID] = true
	}
	// Never a tool: the runtime operations (a service account reads the
	// view and the credential in it), the secret and account management,
	// and the egress operations.
	for _, id := range []string{
		"getVoiceRuntimeAgents", "ackVoiceRuntime", "reportVoiceCall",
		"rotateVoiceSIPSecret", "createVoiceRuntimeAccount",
		"discoverVoiceMCPServer", "testVoiceMCPServer",
		"createVoiceMCPServer", "updateVoiceMCPServer", "deleteVoiceMCPServer",
	} {
		found := false
		for _, op := range ops {
			found = found || op.ID == id
		}
		if !found {
			t.Errorf("operation %s is not in the document", id)
		}
		if exposed[id] {
			t.Errorf("%s is an MCP tool: the runtime view and egress are not for agents", id)
		}
	}
	// Console reads stay tools: agents and the runtime status are readable
	// through MCP without credentials or transcripts.
	for _, id := range []string{"listVoiceAgents", "getVoiceAgent", "getVoiceStatus", "listVoiceMCPServers"} {
		if !exposed[id] {
			t.Errorf("read operation %s is not an MCP tool", id)
		}
	}
}
