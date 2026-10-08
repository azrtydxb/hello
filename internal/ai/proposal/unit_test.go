package proposal

import (
	"net/http"
	"testing"
)

// TestPickHeaders: apply copies only the applier's Cookie and
// Authorization; nothing else of the caller's request reaches the replay.
func TestPickHeaders(t *testing.T) {
	in := http.Header{
		"Cookie": {"s=1"}, "Authorization": {"Bearer x"},
		"X-Forwarded-For": {"1.2.3.4"}, "Origin": {"https://evil"}, "Sec-Fetch-Site": {"cross-site"},
		"X-Hello-Agent": {"1"}, "Content-Type": {"text/plain"},
	}
	got := pick(in)
	if len(got) != 2 || got.Get("Cookie") != "s=1" || got.Get("Authorization") != "Bearer x" {
		t.Fatalf("pick = %v", got)
	}
	if len(pick(nil)) != 0 {
		t.Fatal("pick(nil) is not empty")
	}
}

func TestDiffAndMerge(t *testing.T) {
	ch, err := Diff([]byte(`{"a":1,"b":{"c":[1,2]},"gone":true}`), []byte(`{"a":2,"b":{"c":[1,3,4]},"new":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"/a": true, "/b/c/1": true, "/b/c/2": true, "/gone": true, "/new": true}
	if len(ch) != len(want) {
		t.Fatalf("diff = %+v", ch)
	}
	for _, c := range ch {
		if !want[c.Path] {
			t.Errorf("unexpected change %+v", c)
		}
	}
	if ch, _ := Diff([]byte(`{"a":1,"b":2}`), []byte(`{"b":2,"a":1}`)); len(ch) != 0 || !Equal([]byte(`{"a":1,"b":2}`), []byte(`{"b":2, "a":1}`)) {
		t.Errorf("equal documents differ: %+v", ch)
	}
	if _, err := Diff([]byte(`{`), nil); err == nil {
		t.Error("invalid JSON accepted")
	}
	m, err := mergePatch([]byte(`{"a":1,"b":{"c":2,"d":3},"e":4}`), []byte(`{"b":{"c":null,"x":5},"e":[1]}`))
	if err != nil || !Equal(m, []byte(`{"a":1,"b":{"d":3,"x":5},"e":[1]}`)) {
		t.Errorf("merge = %s %v", m, err)
	}
	if got := Targets([]Action{{OperationID: "updateExtension", PathParams: map[string]string{"id": "1"}}, {OperationID: "createOutboundRoute"}, {OperationID: "putFeatureCodes"}}); len(got) != 2 || got[0] != "Extension/1" || got[1] != "FeatureCodes" {
		t.Errorf("targets = %v", got)
	}
}
