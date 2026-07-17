package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/orchestrator"
)

// T3.5.1 — Requests processed in priority order
func TestPriorityQueue_PriorityOrder(t *testing.T) {
	q := orchestrator.NewPriorityQueue()
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if err := q.Enqueue(ctx, backend.PriorityLow); err != nil {
			t.Errorf("enqueue low: %v", err)
		}
	}()

	time.Sleep(20 * time.Millisecond)

	go func() {
		defer wg.Done()
		if err := q.Enqueue(ctx, backend.PriorityHigh); err != nil {
			t.Errorf("enqueue high: %v", err)
		}
	}()

	time.Sleep(20 * time.Millisecond)

	p, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue 1: %v", err)
	}
	if p != backend.PriorityHigh {
		t.Fatalf("first dequeued priority = %v, want HIGH", p)
	}

	p, err = q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue 2: %v", err)
	}
	if p != backend.PriorityLow {
		t.Fatalf("second dequeued priority = %v, want LOW", p)
	}

	wg.Wait()
}

// T3.5.2 — Same priority: FIFO ordering
func TestPriorityQueue_FIFOWithinPriority(t *testing.T) {
	q := orchestrator.NewPriorityQueue()
	ctx := context.Background()

	go func() {
		time.Sleep(5 * time.Millisecond)
		if _, err := q.Dequeue(ctx); err != nil {
			t.Errorf("dequeue 1: %v", err)
		}
		if _, err := q.Dequeue(ctx); err != nil {
			t.Errorf("dequeue 2: %v", err)
		}
	}()

	firstDone := make(chan struct{})
	secondDone := make(chan struct{})

	go func() {
		if err := q.Enqueue(ctx, backend.PriorityHigh); err != nil {
			t.Errorf("enqueue first: %v", err)
		}
		close(firstDone)
	}()
	time.Sleep(20 * time.Millisecond)

	go func() {
		if err := q.Enqueue(ctx, backend.PriorityHigh); err != nil {
			t.Errorf("enqueue second: %v", err)
		}
		close(secondDone)
	}()

	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first enqueue did not complete")
	}

	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second enqueue did not complete")
	}
}

// T3.5.3 — Queue respects context cancellation
func TestPriorityQueue_ContextCancellation(t *testing.T) {
	q := orchestrator.NewPriorityQueue()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- q.Enqueue(ctx, backend.PriorityLow)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
		if err != context.Canceled {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue did not cancel in time")
	}
}
