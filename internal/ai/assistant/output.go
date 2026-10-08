package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/azrtydxb/hello/internal/ai/proposal"
)

// Answer is the model's structured final answer (spec S-6, S-9).
type Answer struct {
	Answer    string `json:"answer" jsonschema:"description=The answer as plain text with line breaks: no Markdown links or images and no HTML."`
	Citations []int  `json:"citations" jsonschema:"description=Zero-based positions of the tool calls made for this message that the answer relies on; empty for a general question."`
	// Proposal is a suggested configuration change; a human reviews and
	// applies it.
	Proposal *ProposalOut `json:"proposal,omitempty" jsonschema:"description=Only when the user asks for a change: allowlisted operations a human reviews as a diff and applies."`
}

// ProposalOut is a proposal as the model returns it.
type ProposalOut struct {
	Title     string      `json:"title" jsonschema:"description=A short title of the change."`
	Rationale string      `json:"rationale" jsonschema:"description=Why the change helps and which data shows it."`
	Actions   []ActionOut `json:"actions" jsonschema:"description=The operations in the order they apply; at most 8."`
}

// ActionOut is one proposed operation.
type ActionOut struct {
	OperationID string            `json:"operationId" jsonschema:"description=The OpenAPI operation id such as updateOutboundRoute."`
	PathParams  map[string]string `json:"pathParams,omitempty" jsonschema:"description=The path parameters by name such as id."`
	Body        json.RawMessage   `json:"body,omitempty" jsonschema:"description=The request body; omitted for a delete."`
}

// markup matches what a console rendering Markdown or HTML would turn into
// a link, an image or an element: S-5 keeps answers plain text.
var markup = regexp.MustCompile(`(?i)!?\[[^\]]*\]\([^)]*\)|<\s*/?\s*[a-z!?][^>]*>|\]\s*:\s*\S+://|javascript:`)

// validateAnswer checks an answer against the calls this message made. Its
// errors go back to the model, which is asked again.
func validateAnswer(a Answer, calls int) error {
	if strings.TrimSpace(a.Answer) == "" {
		return errors.New("answer is empty")
	}
	if markup.MatchString(a.Answer) {
		return errors.New("answer must be plain text: no Markdown links or images and no HTML")
	}
	seen := map[int]bool{}
	for _, c := range a.Citations {
		if c < 0 || c >= calls {
			return fmt.Errorf("citation %d names no tool call: this message made %d (positions 0 to %d)", c, calls, calls-1)
		}
		if seen[c] {
			return fmt.Errorf("citation %d is listed twice", c)
		}
		seen[c] = true
	}
	if p := a.Proposal; p != nil {
		if strings.TrimSpace(p.Title) == "" || len(p.Actions) == 0 {
			return errors.New("a proposal needs a title and at least one action")
		}
		if len(p.Actions) > proposal.MaxActions {
			return fmt.Errorf("a proposal has at most %d actions", proposal.MaxActions)
		}
		if markup.MatchString(p.Title) || markup.MatchString(p.Rationale) {
			return errors.New("proposal title and rationale must be plain text: no Markdown links or images and no HTML")
		}
	}
	return nil
}

// draft is the proposal of an answer as a proposal draft for sessionID.
func draft(p *ProposalOut, sessionID string) *proposal.Draft {
	d := &proposal.Draft{
		Source:    proposal.SourceAssistant,
		Title:     strings.TrimSpace(p.Title),
		Rationale: strings.TrimSpace(p.Rationale),
		SessionID: &sessionID,
	}
	for _, a := range p.Actions {
		d.Actions = append(d.Actions, proposal.Action{
			OperationID: a.OperationID,
			PathParams:  a.PathParams,
			Body:        a.Body,
		})
	}
	return d
}

// validator returns the Validate of a message's call: the answer's checks,
// then its proposal's (spec S-12), keeping the draft that passed. A
// proposal with no validator wired is refused, so none is stored
// unvalidated.
func (a *Assistant) validator(ident proposal.Identity, sessionID string, ts *toolset, validated **proposal.Draft) func(context.Context, Answer) error {
	return func(ctx context.Context, ans Answer) error {
		*validated = nil
		if err := validateAnswer(ans, len(ts.Calls())); err != nil {
			return err
		}
		if ans.Proposal == nil {
			return nil
		}
		if a.cfg.Proposals == nil || a.cfg.Proposals.Validator == nil || a.cfg.Proposals.Store == nil {
			return errors.New("proposals are not available here: answer without a proposal")
		}
		d := draft(ans.Proposal, sessionID)
		if err := a.cfg.Proposals.Validator.Validate(ctx, ident, d); err != nil {
			return fmt.Errorf("the proposal is invalid: %w", err)
		}
		*validated = d
		return nil
	}
}
