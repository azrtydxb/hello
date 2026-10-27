package snapshot

import "testing"

// TestBossLinks: the snapshot answers by boss, and an assistant that serves
// several bosses with the outbound flag has no boss to call as. It fails if
// the lookup is by assistant or the ambiguous case picks one.
func TestBossLinks(t *testing.T) {
	s := New(1, "pbx.test", nil).WithBossLinks([]BossLink{
		{Boss: "200", Assistant: "300", RingMode: BossRingBoth, OutboundAsBoss: true},
		{Boss: "210", Assistant: "300", RingMode: BossRingAssistantFirst, Screening: true},
		{Boss: "220", Assistant: "310", RingMode: BossRingBoth, OutboundAsBoss: true},
		{Boss: "230", Assistant: "310", RingMode: BossRingBoth, OutboundAsBoss: true},
	})
	if l, ok := s.BossLinkOf("210"); !ok || l.Assistant != "300" || !l.Screening {
		t.Fatalf("BossLinkOf(210) = %+v, %v", l, ok)
	}
	if _, ok := s.BossLinkOf("300"); ok {
		t.Fatal("an assistant is not a boss")
	}
	if b, ok := s.OutboundBossOf("300"); !ok || b != "200" {
		t.Fatalf("OutboundBossOf(300) = %q, %v; want 200", b, ok)
	}
	if b, ok := s.OutboundBossOf("310"); ok {
		t.Fatalf("OutboundBossOf(310) = %q; two bosses with the flag are ambiguous", b)
	}
	if _, ok := New(1, "pbx.test", nil).OutboundBossOf("300"); ok {
		t.Fatal("a snapshot without links has no outbound boss")
	}
}
