package metrics

import "sync"

type EventType string

const (
	EventRequest    EventType = "request"
	EventVRAMUpdate EventType = "vram_update"
)

type Event struct {
	Type EventType   `json:"type"`
	Data interface{} `json:"data"`
}

type RequestEventData struct {
	ID        string  `json:"id"`
	Route     string  `json:"route"`
	Model     string  `json:"model"`
	Tokens    int     `json:"tokens"`
	LatencyMs float64 `json:"latency_ms"`
}

type VRAMEventData struct {
	Total  int64          `json:"total"`
	Used   int64          `json:"used"`
	Free   int64          `json:"free"`
	Models []VRAMModelRow `json:"models"`
}

type VRAMModelRow struct {
	Name   string `json:"name"`
	VRAM   int64  `json:"vram"`
	State  string `json:"state"`
	Backend string `json:"backend"`
}

// Bus fans out events to dashboard WebSocket clients.
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus {
	return &Bus{subs: make(map[chan Event]struct{})}
}

func (b *Bus) Subscribe(buffer int) chan Event {
	if buffer <= 0 {
		buffer = 16
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Bus) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
	b.mu.Unlock()
}

func (b *Bus) Publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// drop if slow consumer
		}
	}
}
