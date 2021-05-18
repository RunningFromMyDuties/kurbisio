package backend

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/relabs-tech/backends/core/access"
)

func TestPutEvent(t *testing.T) {
	eventType := "some-event"
	received := make(chan Event, 10)
	testService.backend.HandleEvent(eventType, func(ctx context.Context, event Event) error {
		received <- event
		return nil
	})

	cl := testService.client
	status, err := cl.RawPut("/kurbisio/events/"+eventType, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status != 204 {
		t.Fatalf("Expecting status %d, got %d", 204, status)
	}

	testService.backend.ProcessJobsSync(-1)
	select {
	case <-received:
	default:
		t.Fatal("Timeout waiting for event to be received")
	}

	// We now try with a non admin client
	cl = testService.clientNoAuth
	status, _ = cl.RawPut("/kurbisio/events/"+eventType, nil, nil)
	if status != 401 {
		t.Fatalf("Expecting status %d, got %d", 401, status)
	}

	testService.backend.ProcessJobsSync(-1)
	select {
	case <-received:
		t.Fatal("Have received an event")
	default:
	}

	// We now try with an admin and 'admin viewer' client
	cl = testService.client.WithAuthorization(&access.Authorization{Roles: []string{"admin viewer", "admin"}})
	status, _ = cl.RawPut("/kurbisio/events/"+eventType, nil, nil)
	if status != 204 {
		t.Fatalf("Expecting status %d, got %d", 204, status)
	}

	testService.backend.ProcessJobsSync(-1)
	select {
	case <-received:
	default:
		t.Fatal("Have not received an event")
	}
}

func TestEventRetry(t *testing.T) {
	eventType := "retry-event"
	received := make(chan Event, 10)
	testService.backend.HandleEvent(eventType, func(ctx context.Context, event Event) error {
		received <- event
		return fmt.Errorf("this fails")
	})
	t0 := time.Now()
	err := testService.backend.RaiseEvent(context.TODO(), Event{Type: eventType, Resource: "something", ResourceID: uuid.New()})
	if err != nil {
		t.Fatalf("raise event error: %v", err)
	}

	var events []Event
	numExpectedEvents := 4
	timeouts := [3]time.Duration{time.Second, time.Second * 2, time.Second * 3}
	timeout := 7 * time.Second

	for {
		if time.Now().Sub(t0) > timeout {
			break
		}
		if len(events) == numExpectedEvents {
			break
		}
		testService.backend.ProcessJobsSyncWithTimeouts(-1, timeouts)
		select {
		case e := <-received:
			events = append(events, e)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if len(events) != numExpectedEvents {
		t.Fatalf("received %d events, but expected %d", len(events), numExpectedEvents)
	}

	if events[0].ScheduledAt != nil {
		t.Fatal("raised event has scheduled_at, but should not")
	}

	// we get max 3 retry attempts
	for i := 0; i < 3; i++ {
		en := i + 1
		t1 := *events[en].ScheduledAt
		if d := t1.Sub(t0.Add(timeouts[i])); d < 0 {
			t.Fatalf("event #%d too early: %v", en, d)
		}
		if d := t1.Sub(t0.Add(timeouts[i])); d > 50*time.Millisecond {
			t.Fatalf("event #%d too late: %v", en, d)
		}
		t0 = t1
	}

	// that's it, no more retries, we have failed
	health, _ := testService.backend.Health(true /*include details*/)
	found := false
	for _, j := range health.Jobs.Details {
		found = j.Job == "event" && j.Type == "retry-event" && j.AttemptsLeft == 0
	}

	if !found {
		t.Fatal("no failed event in jobs table")
	}

}

func TestRateLimitEvent(t *testing.T) {
	eventType := "rate-limited-event"
	received := make(chan Event, 10)
	testService.backend.HandleEvent(eventType, func(ctx context.Context, event Event) error {
		received <- event
		return nil
	})

	delta := 500 * time.Millisecond
	testService.backend.DefineRateLimitForEvent(eventType, delta, time.Minute)
	t0 := time.Now().UTC()
	err := testService.backend.RaiseEvent(context.TODO(), Event{Type: eventType, Resource: "something", ResourceID: uuid.New()})
	if err != nil {
		t.Fatalf("raise event error: %v", err)
	}
	err = testService.backend.RaiseEvent(context.TODO(), Event{Type: eventType, Resource: "something", ResourceID: uuid.New()})
	if err != nil {
		t.Fatalf("raise event error: %v", err)
	}
	err = testService.backend.RaiseEvent(context.TODO(), Event{Type: eventType, Resource: "something", ResourceID: uuid.New()})
	if err != nil {
		t.Fatalf("raise event error: %v", err)
	}
	err = testService.backend.RaiseEvent(context.TODO(), Event{Type: eventType, Resource: "something", ResourceID: uuid.New()})
	if err != nil {
		t.Fatalf("raise event error: %v", err)
	}

	var events []Event
	numExpectedEvents := 4
	timeout := 4 * time.Second

	for {
		if time.Now().Sub(t0) > timeout {
			break
		}
		if len(events) == numExpectedEvents {
			break
		}
		testService.backend.ProcessJobsSync(-1)
		select {
		case e := <-received:
			events = append(events, e)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if len(events) != numExpectedEvents {
		t.Fatalf("received %d events, but expected %d", len(events), numExpectedEvents)
	}

	// check that the first event has a schedule
	t0 = *events[0].ScheduledAt

	// we get 3 more events with delta delay between them
	for i := 1; i < 3; i++ {
		t1 := *events[i].ScheduledAt
		if d := t1.Sub(t0.Add(time.Duration(i) * delta)); d < 0 {
			t.Fatalf("event #%d too early: %v", i, d)
		}
		if d := t1.Sub(t0.Add(time.Duration(i) * delta)); d > 50*time.Millisecond {
			t.Fatalf("event #%d too late: %v", i, d)
		}
	}

}

func TestRateLimitEventRetry(t *testing.T) {
	eventType := "rate-limited-event-retry"

	received := make(chan Event, 10)
	testService.backend.HandleEvent(eventType, func(ctx context.Context, event Event) error {
		received <- event
		return fmt.Errorf("this fails")
	})

	delta := 1000 * time.Millisecond
	testService.backend.DefineRateLimitForEvent(eventType, delta, time.Minute)
	t0 := time.Now().UTC()
	err := testService.backend.RaiseEvent(context.TODO(), Event{Type: eventType, Resource: "something", ResourceID: uuid.New()})
	if err != nil {
		t.Fatalf("raise event error: %v", err)
	}

	var events []Event
	numExpectedEvents := 2
	timeouts := [3]time.Duration{200 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond}
	timeout := 7 * time.Second

	for {
		if time.Now().Sub(t0) > timeout {
			break
		}
		if len(events) == numExpectedEvents {
			break
		}
		testService.backend.ProcessJobsSyncWithTimeouts(-1, timeouts)
		select {
		case e := <-received:
			events = append(events, e)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if len(events) != numExpectedEvents {
		t.Fatalf("received %d events, but expected %d", len(events), numExpectedEvents)
	}

	// the first event came right away
	t1 := *events[0].ScheduledAt
	if d := t1.Sub(t0); d < 0 {
		t.Fatalf("event #%d too early: %v", 0, d)
	}
	if d := t1.Sub(t0); d > 50*time.Millisecond {
		t.Fatalf("event #%d too late: %v", 0, d)
	}
	t0 = t1

	// the 2nd event came after a 200ms retry timeout but was put back onto the rate limit schedule, so delta (=1000ms)
	t1 = *events[1].ScheduledAt
	if d := t1.Sub(t0.Add(delta)); d < 0 {
		t.Fatalf("event #%d too early: %v", 1, d)
	}
	if d := t1.Sub(t0.Add(delta)); d > 50*time.Millisecond {
		t.Fatalf("event #%d too late: %v", 1, d)
	}
}

func TestRateLimitEventMaxAge(t *testing.T) {
	eventType := "rate-limited-event-maxage"
	received := make(chan Event, 10)
	testService.backend.HandleEvent(eventType, func(ctx context.Context, event Event) error {
		received <- event
		return nil
	})

	delta := 200 * time.Millisecond
	maxAge := 400 * time.Millisecond
	testService.backend.DefineRateLimitForEvent(eventType, delta, maxAge)
	err := testService.backend.RaiseEvent(context.TODO(), Event{Type: eventType, Resource: "something", ResourceID: uuid.New()})
	if err != nil {
		t.Fatalf("raise event error: %v", err)
	}
	err = testService.backend.RaiseEvent(context.TODO(), Event{Type: eventType, Resource: "something", ResourceID: uuid.New()})
	if err != nil {
		t.Fatalf("raise event error: %v", err)
	}

	// now we simulate the server not being responsive
	time.Sleep(2 * time.Second)
	// and continue processing. The rate limited events are now older than max age and should be rescheduled by the system
	t0 := time.Now().UTC()

	var events []Event
	numExpectedEvents := 2
	timeout := 5 * time.Second

	for {
		if time.Now().Sub(t0) > timeout {
			break
		}
		if len(events) == numExpectedEvents {
			break
		}
		testService.backend.ProcessJobsSync(-1)
		select {
		case e := <-received:
			events = append(events, e)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if len(events) != numExpectedEvents {
		t.Fatalf("received %d events, but expected %d", len(events), numExpectedEvents)
	}

	// we get 2 events with delta delay between them
	for i := 0; i < 2; i++ {
		t1 := *events[i].ScheduledAt
		if d := t1.Sub(t0.Add(time.Duration(i) * delta)); d < 0 {
			t.Fatalf("event #%d too early: %v", i, d)
		}
		if d := t1.Sub(t0.Add(time.Duration(i) * delta)); d > 50*time.Millisecond {
			t.Fatalf("event #%d too late: %v", i, d)
		}
	}

}
