package metrics

import (
	"sort"
	"sync"
	"time"
)

type Collector struct {
	mu sync.Mutex

	routeCounts map[string]int
	tokenTotal  int
	tokenMin    int
	tokenMax    int
	tokenN      int

	latencies []time.Duration

	localReqs          int
	cloudCostPerReqUSD float64
	actualCostUSD      float64

	bus *Bus
}

func NewCollector(bus *Bus) *Collector {
	return &Collector{
		routeCounts:        make(map[string]int),
		tokenMin:           -1,
		cloudCostPerReqUSD: 0.10,
		bus:                bus,
	}
}

func (c *Collector) SetCloudCostPerRequest(usd float64) {
	c.mu.Lock()
	c.cloudCostPerReqUSD = usd
	c.mu.Unlock()
}

type RequestRecord struct {
	ID        string
	Route     string // local | cloud
	Model     string
	Tokens    int
	Latency   time.Duration
	ActualUSD float64
}

func (c *Collector) Record(rec RequestRecord) {
	c.mu.Lock()
	c.routeCounts[rec.Route]++
	c.tokenTotal += rec.Tokens
	c.tokenN++
	if c.tokenMin < 0 || rec.Tokens < c.tokenMin {
		c.tokenMin = rec.Tokens
	}
	if rec.Tokens > c.tokenMax {
		c.tokenMax = rec.Tokens
	}
	c.latencies = append(c.latencies, rec.Latency)
	if rec.Route == "local" {
		c.localReqs++
	}
	c.actualCostUSD += rec.ActualUSD
	c.mu.Unlock()

	if c.bus != nil {
		c.bus.Publish(Event{
			Type: EventRequest,
			Data: RequestEventData{
				ID:        rec.ID,
				Route:     rec.Route,
				Model:     rec.Model,
				Tokens:    rec.Tokens,
				LatencyMs: float64(rec.Latency.Microseconds()) / 1000.0,
			},
		})
	}
}

func (c *Collector) GetRouteCounts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.routeCounts))
	for k, v := range c.routeCounts {
		out[k] = v
	}
	return out
}

type TokenStats struct {
	Total int `json:"total"`
	Avg   int `json:"avg"`
	Min   int `json:"min"`
	Max   int `json:"max"`
}

func (c *Collector) GetTokenStats() TokenStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokenN == 0 {
		return TokenStats{}
	}
	min := c.tokenMin
	if min < 0 {
		min = 0
	}
	return TokenStats{
		Total: c.tokenTotal,
		Avg:   c.tokenTotal / c.tokenN,
		Min:   min,
		Max:   c.tokenMax,
	}
}

type CostSavings struct {
	EstimatedCloudCost float64 `json:"estimated_cloud_cost"`
	ActualCost         float64 `json:"actual_cost"`
	Savings            float64 `json:"savings"`
}

func (c *Collector) GetCostSavings() CostSavings {
	c.mu.Lock()
	defer c.mu.Unlock()
	est := float64(c.localReqs) * c.cloudCostPerReqUSD
	return CostSavings{
		EstimatedCloudCost: est,
		ActualCost:         c.actualCostUSD,
		Savings:            est - c.actualCostUSD,
	}
}

type LatencyPercentiles struct {
	P50 time.Duration `json:"p50"`
	P90 time.Duration `json:"p90"`
	P99 time.Duration `json:"p99"`
}

func (c *Collector) GetLatencyPercentiles() LatencyPercentiles {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.latencies)
	if n == 0 {
		return LatencyPercentiles{}
	}
	cp := append([]time.Duration(nil), c.latencies...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return LatencyPercentiles{
		P50: percentile(cp, 50),
		P90: percentile(cp, 90),
		P99: percentile(cp, 99),
	}
}

func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

func (c *Collector) PublishVRAM(data VRAMEventData) {
	if c.bus != nil {
		c.bus.Publish(Event{Type: EventVRAMUpdate, Data: data})
	}
}
