package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// prompt is one of the prompts of spec S-16: its arguments and the
// instruction it builds from them.
type prompt struct {
	p    *sdk.Prompt
	text func(args map[string]string) (string, error)
}

var prompts = []prompt{
	{
		p: &sdk.Prompt{Name: "troubleshoot-call", Title: "Troubleshoot a call",
			Description: "Find out why a call failed, from its CDR and trace, the device's registration and the trunk.",
			Arguments: []*sdk.PromptArgument{
				{Name: "number", Description: "The extension or external number involved."},
				{Name: "cdrId", Description: "The id of the call's CDR, when known."},
			}},
		text: func(a map[string]string) (string, error) {
			var b strings.Builder
			switch {
			case a["cdrId"] != "":
				fmt.Fprintf(&b, "Troubleshoot the call with CDR %s. Read the resource hello://cdrs/%s (the CDR with its SIP trace).", a["cdrId"], a["cdrId"])
			case a["number"] != "":
				fmt.Fprintf(&b, "Troubleshoot a recent call involving %s. Read hello://cdrs/recent, or call `listCDRs` with `failed` set, to find it, then read hello://cdrs/{id} for its trace.", a["number"])
			default:
				return "", fmt.Errorf("troubleshoot-call needs number or cdrId")
			}
			b.WriteString(" Then check the parties: read hello://registrations for the device, `getDeviceDiagnostics` (or hello://diagnostics/devices/{id}) for its registration attempts, and `listAuthFailures` for blocked addresses; for an external call read hello://trunks/status. " +
				"Explain the cause from the trace's SIP status codes and say what to change; follow the hello-troubleshoot skill.")
			return b.String(), nil
		},
	},
	{
		p: &sdk.Prompt{Name: "onboard-user", Title: "Onboard a user",
			Description: "Create an extension and a device for a new user, and optionally provision their phone.",
			Arguments: []*sdk.PromptArgument{
				{Name: "number", Description: "The extension number.", Required: true},
				{Name: "name", Description: "The user's name.", Required: true},
				{Name: "phoneMac", Description: "The phone's MAC address, to provision it."},
				{Name: "vendor", Description: "The phone's vendor, with phoneMac."},
			}},
		text: func(a map[string]string) (string, error) {
			if a["number"] == "" || a["name"] == "" {
				return "", fmt.Errorf("onboard-user needs number and name")
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Onboard %s on extension %s. Call `listExtensions` to check the number is free, `createExtension` to create it, and `createDevice` for its SIP account. "+
				"The device's SIP password is withheld from MCP: tell the administrator to read it once in the Hello console (Devices).", a["name"], a["number"])
			if a["phoneMac"] != "" {
				fmt.Fprintf(&b, " Then provision the phone %s", a["phoneMac"])
				if a["vendor"] != "" {
					fmt.Fprintf(&b, " (%s)", a["vendor"])
				}
				b.WriteString(": `listProvTemplates` for a template of its model, `createPhone` bound to the device; its provisioning URL is shown only in the console.")
			}
			b.WriteString(" Finally read hello://registrations to confirm the device registers. Follow the hello-setup skill.")
			return b.String(), nil
		},
	},
	{
		p: &sdk.Prompt{Name: "review-routing", Title: "Review routing",
			Description: "Review trunks, routes, ring groups and feature codes for mistakes and gaps."},
		text: func(map[string]string) (string, error) {
			return "Review Hello's call routing. Read hello://trunks/status and call `listTrunks`, `listOutboundRoutes`, `listInboundRoutes`, `listRingGroups` and `listFeatureCodes`. " +
				"Look for unreachable or shadowed routes, numbers no inbound route catches, and trunks that are down; check suspicious cases with `testRouting`. " +
				"Report findings before changing anything; follow the hello-routing skill.", nil
		},
	},
}

// addPrompts registers the prompts on srv.
func addPrompts(srv *sdk.Server) {
	for _, p := range prompts {
		srv.AddPrompt(p.p, func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			text, err := p.text(req.Params.Arguments)
			if err != nil {
				return nil, err
			}
			return &sdk.GetPromptResult{Description: p.p.Description,
				Messages: []*sdk.PromptMessage{{Role: "user", Content: &sdk.TextContent{Text: text}}}}, nil
		})
	}
}
