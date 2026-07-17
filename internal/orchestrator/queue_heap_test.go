package orchestrator

import (
	"container/heap"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
)

func TestPriorityHeap_FIFO(t *testing.T) {
	h := make(priorityHeap, 0)
	heap.Init(&h)
	heap.Push(&h, &queueItem{priority: backend.PriorityHigh, seq: 1})
	heap.Push(&h, &queueItem{priority: backend.PriorityHigh, seq: 2})

	first := heap.Pop(&h).(*queueItem)
	if first.seq != 1 {
		t.Fatalf("first seq = %d, want 1", first.seq)
	}
	second := heap.Pop(&h).(*queueItem)
	if second.seq != 2 {
		t.Fatalf("second seq = %d, want 2", second.seq)
	}
}

func TestPriorityHeap_HighBeforeLow(t *testing.T) {
	h := make(priorityHeap, 0)
	heap.Init(&h)
	heap.Push(&h, &queueItem{priority: backend.PriorityLow, seq: 1})
	heap.Push(&h, &queueItem{priority: backend.PriorityHigh, seq: 2})

	first := heap.Pop(&h).(*queueItem)
	if first.priority != backend.PriorityHigh {
		t.Fatalf("first priority = %v, want HIGH", first.priority)
	}
}
