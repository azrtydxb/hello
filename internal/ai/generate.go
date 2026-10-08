package ai

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/google/jsonschema-go/jsonschema"
)

const (
	// defaultMaxTokens is a call's max_tokens; a length finish retries
	// once with double, capped at maxTokensCap (spec S-4).
	defaultMaxTokens = 8192
	maxTokensCap     = 32768
)

// header is the start of every system prompt (spec S-4).
func header(feature string) string {
	return "hello-feature: " + feature + "\n" + Notice
}

// Generate is the one way Hello calls a model (spec S-4, S-16). It checks
// the budget, takes a slot, prepends the feature line and the untrusted-data
// notice to the system prompt, sends call.Data only through DataBlock, and
// decodes the answer into T: with HELLO_AI_STRUCTURED_OUTPUT=json_schema
// through go-ai-sdk's object Output (T's JSON schema), with prompt by the
// schema in the system prompt and the text after the last </think> and code
// fence. The decoded value is checked against T's schema (unknown
// properties rejected) and call.Validate; a failure is sent back to the
// model with the error text, at most HELLO_AI_VALIDATION_ATTEMPTS answers in
// all, then the call fails invalid_output. A length finish that does not
// decode is retried once with double max_tokens. Reasoning is dropped and
// only its tokens counted. The usage is recorded whatever the outcome.
func Generate[T any](ctx context.Context, s *Service, call Call[T]) (T, Usage, error) {
	var zero T
	if s == nil || !s.enabled {
		return zero, Usage{}, &Error{Code: CodeDisabled, Message: "the AI agent is off"}
	}
	v, u, err := s.generateBounded(ctx, call.Feature, call.Background, func(ctx context.Context) (any, Usage, error) {
		return generate(ctx, s, call)
	})
	if err != nil {
		return zero, u, err
	}
	return v.(T), u, nil
}

// generateBounded applies the budget, limits, timeout, usage, metrics and
// logging around one Generate.
func (s *Service) generateBounded(ctx context.Context, feature string, background bool, run func(context.Context) (any, Usage, error)) (any, Usage, error) {
	start := time.Now()
	finish := func(u Usage, err error) {
		outcome := "ok"
		attrs := []any{"feature", feature, "task_id", taskIDFrom(ctx), "input_tokens", u.InputTokens,
			"output_tokens", u.OutputTokens, "reasoning_tokens", u.ReasoningTokens, "duration", time.Since(start).String()}
		if err != nil {
			outcome = CodeOf(err)
			s.errs.add(outcome, s.now())
			s.log.Warn("ai: call failed", append(attrs, "error_code", outcome)...)
		} else {
			s.log.Info("ai: call", attrs...)
		}
		s.metrics.calls.WithLabelValues(feature, outcome).Inc()
		s.metrics.callSeconds.WithLabelValues(feature).Observe(time.Since(start).Seconds())
	}
	if err := s.checkBudget(ctx, background); err != nil {
		finish(Usage{}, err)
		return nil, Usage{}, err
	}
	release, reason, err := s.limits.acquire(ctx, background)
	if err != nil {
		if reason != "" {
			s.metrics.busy.WithLabelValues(reason).Inc()
		} else {
			err = classify(ctx, err, false)
		}
		finish(Usage{}, err)
		return nil, Usage{}, err
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	v, u, err := run(cctx)
	s.recordUsage(ctx, feature, u)
	finish(u, err)
	return v, u, err
}

// attemptResult is one GenerateText run: its steps (captured as they
// finish, so a failed decode still has them) and the result when it
// succeeded.
type attemptResult struct {
	steps []aisdk.Step
	res   *aisdk.GenerateTextResult
	err   error
}

func generate[T any](ctx context.Context, s *Service, call Call[T]) (any, Usage, error) {
	var u Usage
	resolved, schemaJSON := schemaFor[T]()
	useOutput := s.cfg.StructuredOutput != "prompt" && (len(call.Tools) == 0 || s.model.Capabilities().NativeJSON)
	system := header(call.Feature)
	if call.System != "" {
		system += "\n\n" + call.System
	}
	if !useOutput {
		system += "\n\nAnswer with a single JSON object and nothing else. It must validate against this JSON schema:\n" + string(schemaJSON)
	}
	turn := call.Prompt
	if call.Data != nil {
		turn = DataBlock(call.Data) + "\n\n" + call.Prompt
	}
	msgs := []provider.Message{provider.UserText(turn)}
	stepsLeft := call.MaxSteps
	if stepsLeft <= 0 {
		stepsLeft = max(s.cfg.MaxSteps, 1)
	}
	maxTokens, lengthRetried := defaultMaxTokens, false
	attempts := max(s.cfg.ValidationAttempts, 1)

	for attempt := 1; ; {
		tools := call.Tools
		if stepsLeft <= 0 {
			tools = nil
		}
		mt := maxTokens
		a := runOnce(ctx, s, aisdk.GenerateTextOpts{
			System: system, Messages: msgs, Tools: tools, MaxSteps: max(stepsLeft, 1), MaxTokens: &mt,
		}, useOutput, outputOf[T])
		u = u.add(stepUsage(a.steps))
		var noObject *aisdk.NoObjectGeneratedError
		if a.err != nil && !errors.As(a.err, &noObject) {
			return nil, u, classify(ctx, a.err, len(tools) > 0)
		}
		if len(a.steps) == 0 {
			return nil, u, &Error{Code: CodeProviderError, Message: "the model returned no answer"}
		}
		stepsLeft -= len(a.steps)
		last := a.steps[len(a.steps)-1]
		if len(last.ToolCalls) > 0 {
			// The step budget ran out while the model still called tools:
			// ask for the final answer without tools.
			msgs = append(transcript(msgs, a.steps, "", false), provider.UserText(
				"The tool budget for this question is spent. Answer now from what the tools returned, in the required JSON format."))
			stepsLeft = 0
			continue
		}
		raw := answerText(a.res, last.Text)
		if raw == "" && noObject != nil {
			raw = stripAnswer(noObject.RawText)
		}
		v, decErr := decode[T](raw, resolved)
		verr := decErr
		if verr == nil && call.Validate != nil {
			verr = call.Validate(ctx, v)
		}
		if verr == nil {
			return v, u, nil
		}
		if decErr != nil && last.FinishReason == provider.FinishLength && !lengthRetried {
			lengthRetried, maxTokens = true, min(2*maxTokens, maxTokensCap)
			continue
		}
		if attempt >= attempts {
			return nil, u, &Error{Code: CodeInvalidOutput, Message: fmt.Sprintf("no valid answer after %d attempts", attempts), Err: verr}
		}
		attempt++
		msgs = append(transcript(msgs, a.steps, raw, true), provider.UserText(
			"Your answer was rejected: "+verr.Error()+"\nReply again with a corrected answer in the required JSON format."))
	}
}

func outputOf[T any]() aisdk.Output { return aisdk.OutputObject[T]() }

// runOnce runs GenerateText once, capturing every finished step.
func runOnce(ctx context.Context, s *Service, opts aisdk.GenerateTextOpts, useOutput bool, out func() aisdk.Output) attemptResult {
	var a attemptResult
	opts.Model = s.model
	if useOutput {
		opts.Output = out()
	}
	opts.OnStepFinish = func(st aisdk.Step) { a.steps = append(a.steps, st) }
	a.res, a.err = aisdk.GenerateText(ctx, opts)
	return a
}

// transcript is msgs followed by the steps as the model saw them, without
// reasoning. A tool call left without a result is answered with an error so
// the conversation stays well formed. When answer is set, the last step is
// replaced by answer (final).
func transcript(msgs []provider.Message, steps []aisdk.Step, answer string, final bool) []provider.Message {
	out := append([]provider.Message(nil), msgs...)
	for i, st := range steps {
		if final && i == len(steps)-1 {
			out = append(out, provider.AssistantText(cmp.Or(answer, "(empty answer)")))
			break
		}
		var parts []provider.ContentPart
		if st.Response != nil {
			for _, p := range st.Response.Content {
				switch p.(type) {
				case provider.TextPart, provider.ToolCallPart:
					parts = append(parts, p)
				}
			}
		} else if st.Text != "" {
			parts = append(parts, provider.TextPart{Text: st.Text})
		}
		if len(parts) == 0 {
			continue
		}
		out = append(out, provider.Message{Role: provider.RoleAssistant, Content: parts})
		if len(st.ToolCalls) == 0 {
			continue
		}
		results := map[string]aisdk.ToolResultRecord{}
		for _, r := range st.ToolResults {
			results[r.ToolCallID] = r
		}
		var tr []provider.ContentPart
		for _, c := range st.ToolCalls {
			r, ok := results[c.ID]
			switch {
			case !ok:
				tr = append(tr, provider.ToolResultPart{ToolCallID: c.ID, Name: c.Name, Result: "not run: the step budget is spent", IsError: true})
			case r.Err != nil:
				tr = append(tr, provider.ToolResultPart{ToolCallID: c.ID, Name: c.Name, Result: r.Err.Error(), IsError: true})
			default:
				tr = append(tr, provider.ToolResultPart{ToolCallID: c.ID, Name: c.Name, Result: r.Result})
			}
		}
		out = append(out, provider.Message{Role: provider.RoleTool, Content: tr})
	}
	return out
}

// answerText is the candidate answer: the last step's text after the last
// </think> and without a code fence, or, when the step has no text (go-ai-sdk's
// tool-mode fallback), its decoded Output. The text is preferred so the
// schema check sees properties a lenient decode would drop.
func answerText(res *aisdk.GenerateTextResult, text string) string {
	if t := stripAnswer(text); t != "" {
		return t
	}
	if res != nil && res.Output != nil {
		if b, err := json.Marshal(res.Output); err == nil {
			return string(b)
		}
	}
	return ""
}

// stripAnswer drops inline reasoning (everything up to the last </think>)
// and a surrounding Markdown code fence.
func stripAnswer(text string) string {
	if i := strings.LastIndex(text, "</think>"); i >= 0 {
		text = text[i+len("</think>"):]
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		if nl := strings.IndexByte(text, '\n'); nl >= 0 {
			text = text[nl+1:]
		} else {
			text = strings.TrimPrefix(text, "```")
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	return strings.TrimSpace(text)
}

// rawMessageSchema lets json.RawMessage fields (proposal bodies) be any
// JSON value instead of jsonschema's string for []byte.
var rawMessageSchema = map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}}

// schemaFor is T's JSON schema, resolved for validation, and its JSON for
// the prompt mode; nil when T has no schema (then only decoding checks it).
func schemaFor[T any]() (*jsonschema.Resolved, []byte) {
	sc, err := jsonschema.For[T](&jsonschema.ForOptions{IgnoreInvalidTypes: true, TypeSchemas: rawMessageSchema})
	if err != nil {
		return nil, []byte(`{"type":"object"}`)
	}
	b, _ := json.Marshal(sc)
	r, err := sc.Resolve(nil)
	if err != nil {
		return nil, b
	}
	return r, b
}

// decode parses raw as T, checking it against T's schema first, so an
// unknown property or a missing required one is an error the model sees.
func decode[T any](raw string, resolved *jsonschema.Resolved) (T, error) {
	var v T
	if raw == "" {
		return v, errors.New("the answer is empty; reply with a JSON object")
	}
	var inst any
	if err := json.Unmarshal([]byte(raw), &inst); err != nil {
		return v, fmt.Errorf("the answer is not valid JSON: %w", err)
	}
	if resolved != nil {
		if err := resolved.Validate(inst); err != nil {
			return v, fmt.Errorf("the answer does not match the schema: %w", err)
		}
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("the answer does not match the schema: %w", err)
	}
	return v, nil
}

// stepUsage sums the steps' tokens; reasoning is part of the provider's
// output tokens and is split out so it is counted once.
func stepUsage(steps []aisdk.Step) Usage {
	var u Usage
	for _, st := range steps {
		r := int64(st.Usage.ReasoningTokens)
		u.InputTokens += int64(st.Usage.InputTokens)
		u.OutputTokens += max(int64(st.Usage.OutputTokens)-r, 0)
		u.ReasoningTokens += r
		u.Calls++
	}
	return u
}

func (u Usage) add(o Usage) Usage {
	return Usage{InputTokens: u.InputTokens + o.InputTokens, OutputTokens: u.OutputTokens + o.OutputTokens,
		ReasoningTokens: u.ReasoningTokens + o.ReasoningTokens, Calls: u.Calls + o.Calls}
}

// classify turns a go-ai-sdk or transport failure into a coded error whose
// message holds no prompt or response text.
func classify(ctx context.Context, err error, tools bool) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, errNotPrivate) {
		return &Error{Code: CodeEndpointNotPublic, Message: "the endpoint host resolves to a public address", Err: err}
	}
	var te *aisdk.TimeoutError
	if errors.As(err, &te) || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Message: "the model did not answer in time", Err: err}
	}
	var api *aisdk.APICallError
	if errors.As(err, &api) {
		msg := api.Message
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if api.StatusCode == 400 && tools && strings.Contains(strings.ToLower(msg+" "+api.ResponseBody), "tool") {
			return &Error{Code: CodeToolsUnsupported, Message: "the endpoint rejected tool calling: " + msg, Err: err}
		}
		return &Error{Code: CodeProviderError, Message: fmt.Sprintf("the endpoint answered %d: %s", api.StatusCode, msg), Err: err}
	}
	return &Error{Code: CodeProviderError, Message: "the model endpoint failed", Err: err}
}
