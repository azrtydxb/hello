package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

// The paging-group test (POST /api/v1/paging-groups/{id}/test) is a request
// from hello-control to a hello-sip node, which alone can originate the
// INVITE: control queues a request here, any READY SIP node takes it, pages
// the one extension and posts the outcome, which control polls for.

const (
	pageTestQueue  = "hello:pagetest:queue"
	pageTestResult = "hello:pagetest:result:"
	// PageTestTTL bounds how long a request waits for a node and a result
	// is kept.
	PageTestTTL = 60 * time.Second
)

// PageTestRequest asks a SIP node to test-page one member of a group.
type PageTestRequest struct {
	ID        string `json:"id"`
	GroupCode string `json:"groupCode"` // the group's page code
	Extension string `json:"extension"` // the member's number
	Actor     string `json:"actor"`
}

// PageTestContact is one registered contact's outcome.
type PageTestContact struct {
	Device    string `json:"device"`
	UserAgent string `json:"userAgent"`
	// Result is answered, busy, no_answer or failed.
	Result string `json:"result"`
	// Headers are the auto-answer headers the INVITE carried, "Name: value".
	Headers []string `json:"headers"`
}

// PageTestResult is the outcome of a test page.
type PageTestResult struct {
	ID        string `json:"id"`
	Extension string `json:"extension"`
	// Result is answered (some contact answered and played the tone),
	// busy, dnd, no_answer, no_registration or failed.
	Result   string            `json:"result"`
	Detail   string            `json:"detail,omitempty"`
	Contacts []PageTestContact `json:"contacts"`
	Node     string            `json:"node"`
}

// EnqueuePageTest queues a test-page request for a SIP node.
func (s *Store) EnqueuePageTest(ctx context.Context, r PageTestRequest) error {
	v, err := json.Marshal(r)
	if err != nil {
		return err
	}
	for _, res := range s.c.DoMulti(ctx,
		s.c.B().Rpush().Key(pageTestQueue).Element(string(v)).Build(),
		s.c.B().Expire().Key(pageTestQueue).Seconds(int64(PageTestTTL/time.Second)).Build(),
	) {
		if err := res.Error(); err != nil {
			return fmt.Errorf("livestate: queue page test: %w", err)
		}
	}
	return nil
}

// TakePageTest removes and returns the oldest queued request; ok is false
// when there is none.
func (s *Store) TakePageTest(ctx context.Context) (r PageTestRequest, ok bool, err error) {
	v, err := s.c.Do(ctx, s.c.B().Lpop().Key(pageTestQueue).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("livestate: take page test: %w", err)
	}
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return r, false, fmt.Errorf("livestate: decode page test: %w", err)
	}
	return r, true, nil
}

// PutPageTestResult publishes a test page's outcome.
func (s *Store) PutPageTestResult(ctx context.Context, r PageTestResult) error {
	v, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.c.Do(ctx, s.c.B().Set().Key(pageTestResult+r.ID).Value(string(v)).Px(PageTestTTL).Build()).Error()
}

// GetPageTestResult returns the outcome of request id; ok is false while
// no node has finished it.
func (s *Store) GetPageTestResult(ctx context.Context, id string) (r PageTestResult, ok bool, err error) {
	v, err := s.c.Do(ctx, s.c.B().Get().Key(pageTestResult+id).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("livestate: get page test: %w", err)
	}
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return r, false, fmt.Errorf("livestate: decode page test result: %w", err)
	}
	return r, true, nil
}
