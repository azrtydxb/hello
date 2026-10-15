package ai

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
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
//
// A call with tools runs in two phases. The tool phase offers the tools
// with no structured-output instruction and no response format: the model
// gathers what it needs and replies in plain text, at most MaxSteps steps,
// nudged once when it describes a tool call in text instead of making it.
// The answer phase then turns the conversation so far into T in one call
// without tools, with the structured-output mode and validation above.
// Streamed, only the answer phase's text reaches Stream.Text.
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
	turn := call.Prompt
	if call.Data != nil {
		turn = DataBlock(call.Data) + "\n\n" + call.Prompt
	}
	msgs := []provider.Message{provider.UserText(turn)}
	var fs *fieldStream
	if call.Stream != nil {
		fs = newFieldStream(call.Stream)
	}
	var u Usage
	if len(call.Tools) > 0 {
		var steps []aisdk.Step
		var err error
		msgs, steps, u, err = gather(ctx, s, call.Feature, call.System, call.Tools, call.MaxSteps, msgs, fs)
		if err != nil {
			return nil, u, err
		}
		var log any
		if call.ToolLog != nil {
			log = call.ToolLog()
		} else {
			log = stepCalls(steps)
		}
		var schema []byte
		if s.cfg.StructuredOutput == "prompt" {
			_, sj, err := schemaFor[T]()
			if err != nil {
				return nil, u, noSchema(s, call.Feature, err)
			}
			schema = sj
		}
		msgs = append(msgs, provider.UserText(answerRequest(log, schema)))
	}
	v, au, err := answer(ctx, s, call, msgs, fs)
	return v, u.add(au), err
}

// The tool phase's instructions and the answer phase's request (spec
// S-4): no JSON while tools are offered, the structured answer is asked for
// afterwards.
const (
	gatherInstruction = "First gather what you need with the tools: call them through the tool-calling interface, as many as the question needs. " +
		"When you have what you need, reply in plain text with what you found. Do not write JSON now: the final structured answer is asked for afterwards."
	nudgeInstruction = "You described a tool call in text instead of making it. Call the tool through the tool-calling interface now. " +
		"If you need no tool, reply in plain text with what you know."
	answerInstruction = "Now write the final answer to the question from the conversation above and the tool results, without calling any tool. " +
		"Your replies above are not shown to the user: the JSON object holds the whole answer, not only the citations."
)

// answerRequest is the user turn that opens the answer phase: what to do,
// and the tool calls the answer may cite, by zero-based position, as data.
// In the prompt mode it repeats the schema next to the request, where the
// model reads it last.
func answerRequest(log any, schema []byte) string {
	req := answerInstruction + " The tool calls made for this question, by zero-based position:\n" + DataBlock(log) +
		"\nList in \"citations\" the position of every call above whose result your answer uses: an answer from tool results cites them."
	if schema != nil {
		return req + "\nReply with a single JSON object and nothing else, using exactly the properties of this JSON schema:\n" + string(schema)
	}
	return req + "\nAnswer in the required JSON format."
}

// toolCallEntry is one call in the answer phase's list of calls.
type toolCallEntry struct {
	Position  int             `json:"position"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// stepCalls lists the steps' tool calls in the order they were made.
func stepCalls(steps []aisdk.Step) []toolCallEntry {
	out := []toolCallEntry{}
	for _, st := range steps {
		for _, c := range st.ToolCalls {
			e := toolCallEntry{Position: len(out), Tool: c.Name}
			if len(c.Args) > 0 && json.Valid(c.Args) {
				e.Arguments = c.Args
			}
			out = append(out, e)
		}
	}
	return out
}

// gather is the tool phase: the model calls tools, at most maxSteps steps
// in all (HELLO_AI_MAX_STEPS when 0), and ends in plain text. A last step
// that describes a tool call instead of making one is nudged once while
// steps remain. It returns msgs followed by the phase's transcript, and
// every step it ran. Streamed, it reports reasoning and never text.
func gather(ctx context.Context, s *Service, feature, instructions string, tools []aisdk.Tool, maxSteps int,
	msgs []provider.Message, fs *fieldStream) ([]provider.Message, []aisdk.Step, Usage, error) {
	var u Usage
	system := header(feature)
	if instructions != "" {
		system += "\n\n" + instructions
	}
	system += "\n\n" + gatherInstruction
	stepsLeft := maxSteps
	if stepsLeft <= 0 {
		stepsLeft = max(s.cfg.MaxSteps, 1)
	}
	var all []aisdk.Step
	nudged := false
	for {
		mt := defaultMaxTokens
		opts := aisdk.GenerateTextOpts{
			System: system, Messages: msgs, Tools: tools, MaxSteps: stepsLeft, MaxTokens: &mt,
			ProviderOptions: s.providerOptions(),
		}
		var a attemptResult
		if fs != nil {
			fs.quiet = true
			a = streamOnce(ctx, s, opts, false, nil, fs)
		} else {
			a = runOnce(ctx, s, opts, false, nil)
		}
		u = u.add(stepUsage(a.steps))
		if a.err != nil {
			return nil, all, u, classify(ctx, a.err, true)
		}
		if len(a.steps) == 0 {
			return nil, all, u, &Error{Code: CodeProviderError, Message: "the model returned no answer"}
		}
		all = append(all, a.steps...)
		stepsLeft -= len(a.steps)
		msgs = transcript(msgs, a.steps, "", false)
		last := a.steps[len(a.steps)-1]
		if !nudged && stepsLeft > 0 && len(last.ToolCalls) == 0 && describesToolCall(last.Text, tools, len(stepCalls(all)) == 0) {
			nudged = true
			msgs = append(msgs, provider.UserText(nudgeInstruction))
			continue
		}
		return msgs, all, u, nil
	}
}

// toolCallMarkers are text forms of a tool call a model may write instead
// of making it: chat-template tags and OpenAI-style JSON.
var toolCallMarkers = []string{"<tool_call", "<function=", "<function_call", "<|tool_call", "[tool_calls]", `"tool_calls"`, `"function_call"`}

// intentToCall matches a model announcing a lookup it then does not make,
// such as "Now I will check the current SIP registrations."
var intentToCall = regexp.MustCompile(`(?i)\b(let me|let's|i will|i'll|i need to|i am going to|i'm going to|i should)\s+(now\s+|first\s+)?(check|look|call|query|fetch|read|retrieve|use|get|list|search|inspect|verify)\b`)

// describesToolCall reports whether text, a step that ended without a tool
// call, obviously describes one: a tool-call tag or JSON, a tool's name
// written as a call or as a JSON string, or, before any call was made, an
// announced lookup.
func describesToolCall(text string, tools []aisdk.Tool, noCalls bool) bool {
	t := stripAnswer(text)
	if t == "" {
		return false
	}
	lower := strings.ToLower(t)
	for _, m := range toolCallMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	for _, tl := range tools {
		n := tl.Name()
		if strings.Contains(t, n+"(") || strings.Contains(t, `"`+n+`"`) {
			return true
		}
	}
	return noCalls && intentToCall.MatchString(t)
}

// answer is the answer phase, and the whole of a call without tools: one
// structured answer from msgs, without tools, validated and retried.
func answer[T any](ctx context.Context, s *Service, call Call[T], msgs []provider.Message, fs *fieldStream) (any, Usage, error) {
	var u Usage
	resolved, schemaJSON, err := schemaFor[T]()
	if err != nil {
		return nil, u, noSchema(s, call.Feature, err)
	}
	useOutput := s.cfg.StructuredOutput != "prompt"
	system := header(call.Feature)
	if call.System != "" {
		system += "\n\n" + call.System
	}
	if !useOutput {
		system += "\n\nAnswer with a single JSON object and nothing else. It must validate against this JSON schema:\n" + string(schemaJSON)
	}
	maxTokens, lengthRetried := defaultMaxTokens, false
	attempts := max(s.cfg.ValidationAttempts, 1)

	for attempt := 1; ; {
		mt := maxTokens
		opts := aisdk.GenerateTextOpts{
			System: system, Messages: msgs, MaxSteps: 1, MaxTokens: &mt,
			ProviderOptions: s.providerOptions(),
		}
		var a attemptResult
		if fs != nil {
			fs.quiet = false
			fs.reset() // a new attempt voids any text the last one streamed
			a = streamOnce(ctx, s, opts, useOutput, outputOf[T], fs)
		} else {
			a = runOnce(ctx, s, opts, useOutput, outputOf[T])
		}
		u = u.add(stepUsage(a.steps))
		var noObject *aisdk.NoObjectGeneratedError
		if a.err != nil && !errors.As(a.err, &noObject) {
			return nil, u, classify(ctx, a.err, false)
		}
		if len(a.steps) == 0 {
			return nil, u, &Error{Code: CodeProviderError, Message: "the model returned no answer"}
		}
		last := a.steps[len(a.steps)-1]
		raw := answerText(a.res, last.Text)
		if raw == "" && noObject != nil {
			raw = stripAnswer(noObject.RawText)
		}
		v, decErr := decode[T](raw, resolved)
		verr := decErr
		if verr == nil && call.Validate != nil {
			if verr = call.Validate(ctx, v); verr != nil {
				verr = asRejected(verr)
			}
		}
		if verr == nil {
			return v, u, nil
		}
		class, reason := rejection(verr)
		s.log.Warn("ai: answer rejected", "feature", call.Feature, "task_id", taskIDFrom(ctx), "attempt", attempt,
			"of", attempts, "class", class, "reason", reason, "finish", string(last.FinishReason),
			"output_tokens", last.Usage.OutputTokens)
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

// providerOptions is what every model call adds to the request body. With
// HELLO_AI_THINKING=off and an OpenAI-compatible provider it is
// chat_template_kwargs.enable_thinking=false, which vLLM hands to the chat
// template of a reasoning model (Qwen3) and which templates without that
// switch ignore. It is never sent to another provider, and no
// reasoning_effort is sent: a server that validates that field strictly
// would reject it.
func (s *Service) providerOptions() map[string]any {
	if s.cfg.Thinking || (s.cfg.Provider != "" && s.cfg.Provider != "openai") {
		return nil
	}
	return map[string]any{"openai": map[string]any{
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}}
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

// streamOnce is runOnce on the model's streaming API: the same steps and
// result, with the answer field and the model's reasoning reported to fs as
// they arrive.
func streamOnce(ctx context.Context, s *Service, opts aisdk.GenerateTextOpts, useOutput bool, out func() aisdk.Output, fs *fieldStream) attemptResult {
	var a attemptResult
	opts.Model = s.model
	if useOutput {
		opts.Output = out()
	}
	opts.OnStepFinish = func(st aisdk.Step) { a.steps = append(a.steps, st) }
	ts, err := aisdk.StreamText(ctx, opts)
	if err != nil {
		a.err = err
		return a
	}
	defer func() { _ = ts.Close() }()
	for p := range ts.Parts() {
		switch part := p.(type) {
		case provider.TextDelta:
			fs.text(part.Text)
		case provider.ReasoningDelta:
			fs.reasoning()
		case provider.FinishPart:
			fs.endStep(part.Reason)
		}
	}
	if a.err = ts.Err(); a.err != nil {
		return a
	}
	if useOutput {
		v, err := ts.Output()
		if err != nil {
			a.err = err
			return a
		}
		a.res = &aisdk.GenerateTextResult{Output: v}
	}
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
// the prompt mode. It fails when T has no usable schema (a malformed
// jsonschema tag, for one): the model would otherwise be asked for an
// object without being told its properties.
func schemaFor[T any]() (*jsonschema.Resolved, []byte, error) {
	sc, err := jsonschema.For[T](&jsonschema.ForOptions{IgnoreInvalidTypes: true, TypeSchemas: rawMessageSchema})
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(sc)
	if err != nil {
		return nil, nil, err
	}
	r, err := sc.Resolve(nil)
	if err != nil {
		return nil, nil, err
	}
	return r, b, nil
}

// CheckSchema reports whether T, the answer type of a Generate call, has a
// JSON schema; callers check their types at start, so a malformed tag
// fails there instead of on every call.
func CheckSchema[T any]() error {
	if _, _, err := schemaFor[T](); err != nil {
		return fmt.Errorf("ai: the answer type %s has no JSON schema: %w", reflect.TypeFor[T](), err)
	}
	return nil
}

// noSchema is the failure of a call whose answer type has no JSON schema,
// logged at error: only a code change fixes it.
func noSchema(s *Service, feature string, err error) error {
	s.log.Error("ai: the answer type has no JSON schema", "feature", feature, "error", err)
	return &Error{Code: CodeInvalidOutput, Message: "the answer type has no JSON schema", Err: err}
}

// Rejected is a Validate error with a reason: a short fixed label, such as
// citation_out_of_range, logged with each rejected answer. Its message is
// what the model is told.
type Rejected struct {
	Reason string
	Err    error
}

func (e *Rejected) Error() string { return e.Err.Error() }
func (e *Rejected) Unwrap() error { return e.Err }

// Reject labels a Validate error with reason for the log.
func Reject(reason string, err error) error { return &Rejected{Reason: reason, Err: err} }

// decodeError is a decode failure with its class for the log: empty,
// not_json or schema.
type decodeError struct {
	class string
	err   error
}

func (e *decodeError) Error() string { return e.err.Error() }
func (e *decodeError) Unwrap() error { return e.err }

// asRejected labels a Validate error that carries no reason.
func asRejected(err error) error {
	if r := (*Rejected)(nil); errors.As(err, &r) {
		return err
	}
	return &Rejected{Reason: "unlabelled", Err: err}
}

// rejection is a rejected answer's class (empty, not_json, schema or
// validator) and reason for the log: fixed labels, never the answer's or
// the user's text.
func rejection(err error) (class, reason string) {
	var d *decodeError
	if errors.As(err, &d) {
		return d.class, d.class
	}
	var r *Rejected
	if errors.As(err, &r) {
		return "validator", r.Reason
	}
	return "validator", "unlabelled"
}

// decode parses raw as T, checking it against T's schema first, so an
// unknown property or a missing required one is an error the model sees.
func decode[T any](raw string, resolved *jsonschema.Resolved) (T, error) {
	var v T
	if raw == "" {
		return v, &decodeError{"empty", errors.New("the answer is empty; reply with a JSON object")}
	}
	var inst any
	if err := json.Unmarshal([]byte(raw), &inst); err != nil {
		return v, &decodeError{"not_json", fmt.Errorf("the answer is not valid JSON: %w", err)}
	}
	if resolved != nil {
		if err := resolved.Validate(inst); err != nil {
			return v, &decodeError{"schema", fmt.Errorf("the answer does not match the schema: %w", err)}
		}
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, &decodeError{"schema", fmt.Errorf("the answer does not match the schema: %w", err)}
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
