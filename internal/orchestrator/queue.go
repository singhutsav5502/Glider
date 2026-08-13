package orchestrator

import (
	"container/heap"
	"context"
	"sync"
	"sync/atomic"

	"github.com/glider-ai/glider/internal/backend"
)

type queueItem struct {
	priority backend.Priority
	seq      uint64
	ctx      context.Context
	dequeued chan struct{}
	index    int
}

type priorityHeap []*queueItem

func (h priorityHeap) Len() int { return len(h) }

func (h priorityHeap) Less(i, j int) bool {
	if h[i].priority != h[j].priority {
		return h[i].priority > h[j].priority
	}
	return h[i].seq < h[j].seq
}

func (h priorityHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *priorityHeap) Push(x any) {
	n := len(*h)
	item := x.(*queueItem)
	item.index = n
	*h = append(*h, item)
}

func (h *priorityHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*h = old[:n-1]
	return item
}

// PriorityQueue orders requests by priority (HIGH before LOW), FIFO within priority.
type PriorityQueue struct {
	mu   sync.Mutex
	cond sync.Cond
	items priorityHeap
	seq  atomic.Uint64
}

// NewPriorityQueue creates an empty priority queue.
func NewPriorityQueue() *PriorityQueue {
	q := &PriorityQueue{}
	q.cond.L = &q.mu
	heap.Init(&q.items)
	return q
}

// Enqueue waits until the item is dequeued or ctx is cancelled.
func (q *PriorityQueue) Enqueue(ctx context.Context, priority backend.Priority) error {
	item := &queueItem{
		priority: priority,
		seq:      q.seq.Add(1),
		ctx:      ctx,
		dequeued: make(chan struct{}),
	}

	q.mu.Lock()
	heap.Push(&q.items, item)
	q.cond.Signal()
	q.mu.Unlock()

	cancelled := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			q.mu.Lock()
			if item.index >= 0 {
				heap.Remove(&q.items, item.index)
			}
			q.mu.Unlock()
			close(cancelled)
		case <-item.dequeued:
		}
	}()

	select {
	case <-item.dequeued:
		return nil
	case <-cancelled:
		return ctx.Err()
	}
}

// Dequeue removes and returns the highest-priority waiting item.
func (q *PriorityQueue) Dequeue(ctx context.Context) (backend.Priority, error) {
	q.mu.Lock()
	for len(q.items) == 0 {
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return backend.PriorityLow, ctx.Err()
		default:
		}
		q.mu.Lock()
		if len(q.items) == 0 {
			q.cond.Wait()
		}
	}

	item := heap.Pop(&q.items).(*queueItem)
	q.mu.Unlock()

	close(item.dequeued)
	return item.priority, nil
}
