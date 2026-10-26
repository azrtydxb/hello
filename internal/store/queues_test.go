package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func queueInput(name, number string) QueueInput {
	return QueueInput{
		Name: name, Number: number, Enabled: true, Strategy: "ring_all", AgentTimeout: 20, RetryDelay: 5,
		MaxWait: 600, JoinEmpty: true, Timeout: IVRDestination{Kind: IVRHangup},
		Full: IVRDestination{Kind: IVRHangup}, NoAgents: IVRDestination{Kind: IVRHangup}, Members: []QueueMember{},
	}
}

// TestQueueNumberNotParkSlot: a queue number and a park slot are both dialled
// as a plain number, so neither may take the other's value. It fails if a
// queue can be created or renumbered onto a slot of the default lot
// (701-720), or a park lot created over a queue's number.
func TestQueueNumberNotParkSlot(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	var ve *ValidationError
	_, err := s.CreateQueue(ctx, "t", queueInput("support", "705"), nil)
	if !errors.As(err, &ve) || ve.Fields[0].Path != "number" || !strings.Contains(ve.Fields[0].Message, "park lot") {
		t.Fatalf("create queue on slot 705: %v, want a number error naming the park lot", err)
	}
	q, err := s.CreateQueue(ctx, "t", queueInput("support", "800"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateQueue(ctx, "t", q.ID, queueInput("support", "710"), nil)
	if !errors.As(err, &ve) || ve.Fields[0].Path != "number" || !strings.Contains(ve.Fields[0].Message, "park lot") {
		t.Fatalf("renumber queue onto slot 710: %v, want a number error naming the park lot", err)
	}
	_, err = s.CreateParkLot(ctx, "t", ParkLotInput{Name: "upstairs", SlotStart: 795, SlotEnd: 805, ReturnSecs: 120, VoicemailSecs: 60, Enabled: true}, nil)
	if !errors.As(err, &ve) || !strings.Contains(fmt.Sprint(ve.Fields), "queue support") {
		t.Fatalf("park lot over queue 800: %v, want an error naming the queue", err)
	}
}
