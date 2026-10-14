package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/assistant/chat"
)

// answerField is the property of Answer whose text is streamed.
const answerField = "answer"

// eventBuffer is how many events may wait for a slow reader before the
// task slows to its pace.
const eventBuffer = 64

// taskDoneWait bounds how long the final event waits for the task's own row
// to finish, so that a client reacting to it can post the next message.
const taskDoneWait = 3 * time.Second

// errClientGone ends a task whose requester walked away (the stream's
// context ended): the work stops and nothing is stored.
var errClientGone = errors.New("the requester stopped waiting for the answer")

// toolLabels say in words what each read tool is doing, for the console's
// typing indicator. They name no argument.
var toolLabels = map[string]string{
	"listExtensions":        "Looking up extensions…",
	"getExtension":          "Looking up an extension…",
	"listDevices":           "Looking up devices…",
	"getDevice":             "Looking up a device…",
	"listRingGroups":        "Looking up ring groups…",
	"getRingGroup":          "Looking up a ring group…",
	"listFeatureCodes":      "Looking up feature codes…",
	"listTrunks":            "Looking up trunks…",
	"listTrunkStatus":       "Checking trunk status…",
	"getTrunk":              "Looking up a trunk…",
	"listOutboundRoutes":    "Looking up outbound routes…",
	"getOutboundRoute":      "Looking up an outbound route…",
	"listInboundRoutes":     "Looking up inbound routes…",
	"getInboundRoute":       "Looking up an inbound route…",
	"listRegistrations":     "Looking up registrations…",
	"listCalls":             "Looking up active calls…",
	"listCDRs":              "Searching call records…",
	"countCDRs":             "Counting call records…",
	"cdrConcurrency":        "Measuring call concurrency…",
	"getCDR":                "Reading a call record…",
	"getCluster":            "Checking the cluster…",
	"listClusterNodes":      "Checking cluster nodes…",
	"getDeviceDiagnostics":  "Diagnosing a device…",
	"listAuthFailures":      "Checking failed logins…",
	"listPhones":            "Looking up phones…",
	"getPhone":              "Looking up a phone…",
	"listPhoneFetches":      "Checking phone provisioning…",
	"listRecordings":        "Looking up recordings…",
	"listVoicemailBoxes":    "Looking up voicemail boxes…",
	"listVoicemailMessages": "Looking up voicemail messages…",
	"listAIFindings":        "Reading findings…",
	"getAIFinding":          "Reading a finding…",
	"listAIProposals":       "Reading proposals…",
	"getAIProposal":         "Reading a proposal…",
}

// ToolLabel is the human label of a tool call for the typing indicator.
func ToolLabel(operationID string) string {
	if l, ok := toolLabels[operationID]; ok {
		return l
	}
	return "Reading PBX data…"
}

// sink is the stream of one message's events. Sends never block the work
// once the reader is gone: ctx is the reader's context.
type sink struct {
	ctx    context.Context
	cancel context.CancelFunc
	ch     chan chat.Event
}

func newSink(ctx context.Context) *sink {
	ctx, cancel := context.WithCancel(ctx)
	return &sink{ctx: ctx, cancel: cancel, ch: make(chan chat.Event, eventBuffer)}
}

// send delivers an event, or drops it when the reader is gone.
func (k *sink) send(name string, data any) {
	if k == nil {
		return
	}
	select {
	case k.ch <- chat.Event{Name: name, Data: data}:
	case <-k.ctx.Done():
	}
}

// stream is the call's hooks for ai.Generate.
func (k *sink) stream() *ai.Stream {
	if k == nil {
		return nil
	}
	return &ai.Stream{
		Field:    answerField,
		Thinking: func() { k.send(chat.EventThinking, nil) },
		Text:     func(d string) { k.send(chat.EventDelta, chat.Delta{Text: d}) },
		Reset:    func() { k.send(chat.EventReset, nil) },
	}
}

// tool announces a tool call.
func (k *sink) tool(operationID string) {
	k.send(chat.EventTool, chat.ToolEvent{OperationID: operationID, Label: ToolLabel(operationID)})
}

// finish sends the terminal event once the task's own row has finished (it
// has not while the task function still runs, which is where this is
// called from), then closes the stream.
func (a *Assistant) finish(k *sink, sessionID, name string, data any) {
	go func() {
		defer k.cancel()
		defer close(k.ch)
		deadline := time.Now().Add(taskDoneWait)
		for time.Now().Before(deadline) && k.ctx.Err() == nil {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(k.ctx), 2*time.Second)
			t, err := a.cfg.Store.ActiveAITask(sctx, sessionID)
			cancel()
			if err != nil || t == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		k.send(name, data)
	}()
}

// errorEvent is a failed task's error event: its code and message as the
// task row will carry them.
func errorEvent(err error) chat.ErrorEvent {
	code, msg := ai.CodeOf(err), "the task failed"
	var e *ai.Error
	if errors.As(err, &e) && e.Message != "" {
		msg = e.Message
	}
	if code == "" {
		code = ai.CodeProviderError
	}
	return chat.ErrorEvent{Code: code, Message: msg}
}

// PostMessageStream is PostMessage with the answer reported as events: the
// channel delivers "started", then "thinking", "tool" and "delta" events as
// the assistant works, and ends with "done" (the stored message) or "error",
// then closes. An error returned here is PostMessage's, before anything
// started. Cancelling ctx stops the work; a message already generated is
// still stored, so a dropped stream neither loses nor repeats it.
func (a *Assistant) PostMessageStream(ctx context.Context, u User, sessionID, content string) (<-chan chat.Event, error) {
	k := newSink(ctx)
	if _, _, err := a.post(ctx, u, sessionID, content, k); err != nil {
		k.cancel()
		return nil, err
	}
	return k.ch, nil
}

// answerStreamed is the message task of a streamed message: answer, with
// the events around it. The terminal event goes out after the task
// function returns, see finish.
func (a *Assistant) answerStreamed(ctx context.Context, userID int64, sessionID, taskID string, msgID int64, history []Message, k *sink) (any, error) {
	defer func() {
		if r := recover(); r != nil {
			a.finish(k, sessionID, chat.EventError, chat.ErrorEvent{Code: ai.CodeProviderError, Message: "the task failed"})
			panic(r)
		}
	}()
	k.send(chat.EventStarted, chat.Started{TaskID: taskID, MessageID: msgID})
	out, err := a.answer(ctx, userID, sessionID, taskID, history, k)
	if err != nil {
		a.finish(k, sessionID, chat.EventError, errorEvent(err))
		return nil, err
	}
	a.finish(k, sessionID, chat.EventDone, out.done(taskID, msgID))
	return out.result(), nil
}
