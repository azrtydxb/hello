package livestate

import (
	"context"
	"testing"
)

// TestPageTestQueue fails if a queued request is not taken exactly once in
// order, an empty queue reports a request, or a posted result is lost or
// shown for another id.
func TestPageTestQueue(t *testing.T) {
	s, ctx := store(t), context.Background()
	if _, ok, err := s.TakePageTest(ctx); err != nil || ok {
		t.Fatalf("empty queue = %v, %v", ok, err)
	}
	for _, id := range []string{"a", "b"} {
		if err := s.EnqueuePageTest(ctx, PageTestRequest{ID: id, GroupCode: "*60", Extension: "200"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "b"} {
		r, ok, err := s.TakePageTest(ctx)
		if err != nil || !ok || r.ID != want || r.GroupCode != "*60" {
			t.Fatalf("take = %+v, %v, %v; want %s", r, ok, err, want)
		}
	}
	if _, ok, _ := s.TakePageTest(ctx); ok {
		t.Fatal("a request was taken twice")
	}

	if _, ok, err := s.GetPageTestResult(ctx, "a"); err != nil || ok {
		t.Fatalf("result before it was posted = %v, %v", ok, err)
	}
	res := PageTestResult{ID: "a", Extension: "200", Result: "answered", Node: "sip-1",
		Contacts: []PageTestContact{{Device: "b1", Result: "answered", Headers: []string{"Alert-Info: auto-answer"}}}}
	if err := s.PutPageTestResult(ctx, res); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetPageTestResult(ctx, "a")
	if err != nil || !ok || got.Result != "answered" || len(got.Contacts) != 1 || got.Contacts[0].Headers[0] != "Alert-Info: auto-answer" {
		t.Fatalf("result = %+v, %v, %v", got, ok, err)
	}
	if _, ok, _ := s.GetPageTestResult(ctx, "b"); ok {
		t.Fatal("a result showed for another id")
	}
}
