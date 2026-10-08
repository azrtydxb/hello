package mcp

import "testing"

// TestAIOperationsMCP (spec ai-agent S-24) fails if an operation through
// which a human approves or drives the in-product agent (apply or dismiss a
// proposal, acknowledge or dismiss a finding, chat, run an agent now) is an
// MCP tool, or if a read of the agent's findings or proposals is not.
func TestAIOperationsMCP(t *testing.T) {
	ops := loadSpec(t).Operations()
	tools, _, err := buildTools(ops)
	if err != nil {
		t.Fatal(err)
	}
	exposed := map[string]bool{}
	for _, tl := range tools {
		exposed[tl.op.ID] = true
	}
	for _, id := range []string{"applyAIProposal", "dismissAIProposal", "acknowledgeAIFinding", "dismissAIFinding",
		"createAISession", "updateAISession", "deleteAISession", "postAIMessage", "runAIAgent"} {
		found := false
		for _, op := range ops {
			found = found || op.ID == id
		}
		if !found {
			t.Errorf("operation %s is not in the document", id)
		}
		if exposed[id] {
			t.Errorf("%s is an MCP tool: an external agent must not approve or drive the in-product agent", id)
		}
	}
	for _, id := range []string{"listAIFindings", "getAIFinding", "listAIProposals", "getAIProposal"} {
		if !exposed[id] {
			t.Errorf("read operation %s is not an MCP tool", id)
		}
	}
}
