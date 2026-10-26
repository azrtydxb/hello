package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/timecond"
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

// TestQueueNumberNotConferenceRoom: a queue number and a conference room
// number are both dialled as a plain number, so neither may take the other's
// value. It fails if a queue can be created on a room's number or a room on a
// queue's number.
func TestQueueNumberNotConferenceRoom(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	var ve *ValidationError
	if _, err := s.CreateConference(ctx, "t", ConferenceInput{Name: "board", Number: "860", MaxParticipants: 10,
		EntryExit: ConferenceSoundTone, Enabled: true}, PINChange{}, PINChange{}, nil); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateQueue(ctx, "t", queueInput("support", "860"), nil)
	if !errors.As(err, &ve) || ve.Fields[0].Path != "number" || !strings.Contains(ve.Fields[0].Message, "conference room") {
		t.Fatalf("queue on room 860: %v, want a number error naming the conference room", err)
	}
	if _, err := s.CreateQueue(ctx, "t", queueInput("support", "870"), nil); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateConference(ctx, "t", ConferenceInput{Name: "sales", Number: "870", MaxParticipants: 10,
		EntryExit: ConferenceSoundTone, Enabled: true}, PINChange{}, PINChange{}, nil)
	if !errors.As(err, &ve) || ve.Fields[0].Path != "number" || !strings.Contains(ve.Fields[0].Message, "queue") {
		t.Fatalf("room on queue 870: %v, want a number error naming the queue", err)
	}
}

// TestQueueTimeConditionCrossReferences: a time condition may send its
// callers to a queue and a queue may fail over to a time condition, and
// neither can then be deleted while the other names it. It fails if the
// CHECK lists of either table refuse the other kind, or a delete leaves a
// dangling reference.
func TestQueueTimeConditionCrossReferences(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()
	q, err := s.CreateQueue(ctx, "t", queueInput("support", "800"), nil)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := s.CreateSchedule(ctx, "t", ScheduleInput{Name: "office", TimeZone: "UTC"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := s.CreateTimeCondition(ctx, "t", TimeConditionInput{
		Name: "hours", ScheduleID: sc.ID,
		Open:   timecond.Dest{Kind: timecond.KindQueue, Target: "support"},
		Closed: timecond.Dest{Kind: timecond.KindHangup},
	}, nil)
	if err != nil {
		t.Fatalf("time condition to a queue: %v", err)
	}
	var iu *InUseError
	if err := s.DeleteQueue(ctx, "t", q.ID, nil); !errors.As(err, &iu) || !strings.Contains(iu.Msg, "time condition hours") {
		t.Fatalf("delete queue named by a time condition: %v, want an in-use error naming it", err)
	}
	in := queueInput("support", "800")
	in.Timeout = IVRDestination{Kind: IVRTimeCondition, Target: "hours"}
	if _, err := s.UpdateQueue(ctx, "t", q.ID, in, nil); err != nil {
		t.Fatalf("queue failing over to a time condition: %v", err)
	}
	if err := s.DeleteTimeCondition(ctx, "t", tc.ID, nil); !errors.As(err, &iu) || !strings.Contains(iu.Msg, "queue support") {
		t.Fatalf("delete time condition named by a queue: %v, want an in-use error naming it", err)
	}
}
