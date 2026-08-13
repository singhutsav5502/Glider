package metrics

import (
	"math"
	"strings"
	"time"
)

// Distribution is the LOCAL / CLOUD % split for Overview and GET /api/metrics.
// Cloud includes both gateway BYOK cloud and MITM origin_passthrough so cloud
// turns are never hidden from the percentage tile.
type Distribution struct {
	LocalCount  int            `json:"local_count"`
	CloudCount  int            `json:"cloud_count"`
	CannedCount int            `json:"canned_count"`
	ErrorCount  int            `json:"error_count"`
	Total       int            `json:"total"` // local + cloud + canned (errors excluded from %)
	LocalPct    float64        `json:"local_pct"`
	CloudPct    float64        `json:"cloud_pct"`
	CannedPct   float64        `json:"canned_pct"`
	Actions     map[string]int `json:"actions,omitempty"`
}

// ComputeDistribution builds Local/Cloud/Canned percentages from action tallies.
// origin_passthrough counts as cloud. Errors are reported but excluded from %.
func ComputeDistribution(actionCounts map[string]int) Distribution {
	d := Distribution{Actions: map[string]int{}}
	for k, v := range actionCounts {
		if v == 0 {
			continue
		}
		d.Actions[k] = v
	}
	d.LocalCount = actionCounts["local"]
	d.CloudCount = actionCounts["cloud"] + actionCounts["origin_passthrough"]
	d.CannedCount = actionCounts["canned"]
	d.ErrorCount = actionCounts["error"]
	d.Total = d.LocalCount + d.CloudCount + d.CannedCount
	if d.Total > 0 {
		d.LocalPct = roundPct(100 * float64(d.LocalCount) / float64(d.Total))
		d.CloudPct = roundPct(100 * float64(d.CloudCount) / float64(d.Total))
		d.CannedPct = roundPct(100 * float64(d.CannedCount) / float64(d.Total))
	}
	return d
}

func roundPct(v float64) float64 {
	return math.Round(v*10) / 10
}

// GetDistribution returns live Overview % from recorded request-log actions.
func (c *Collector) GetDistribution() Distribution {
	counts := c.GetRouteCounts()
	actions := map[string]int{}
	for k, v := range counts {
		if strings.HasPrefix(k, "action:") {
			actions[strings.TrimPrefix(k, "action:")] = v
		}
	}
	return ComputeDistribution(actions)
}

// LatencyMS is JSON-friendly latency percentiles in milliseconds.
type LatencyMS struct {
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P99 float64 `json:"p99"`
}

// Snapshot is the GET /api/metrics payload.
type Snapshot struct {
	SessionID      string         `json:"session_id,omitempty"`
	Distribution   Distribution   `json:"distribution"`
	ClassRates     map[string]int `json:"class_rates,omitempty"` // reason → count (small_offload, must_cloud, …)
	RoleRates      map[string]int `json:"role_rates,omitempty"`  // plan|exec|research → count
	RouteCounts    map[string]int `json:"route_counts"`
	TokenStats     TokenStats     `json:"token_stats"`
	TokensSavedEst int            `json:"tokens_saved_est"`
	CostSavings    CostSavings    `json:"cost_savings"`
	LatencyMs      LatencyMS      `json:"latency_ms"`
	// ContextRoutes is optional turn-family route tallies from contextgraph
	// (local/cloud). Present when the dashboard wires a ContextGraph source.
	ContextRoutes map[string]int `json:"context_routes,omitempty"`
}

// GetClassRates returns classifier/routing reason tallies (keys without "class:" prefix).
func (c *Collector) GetClassRates() map[string]int {
	return c.prefixedCounts("class:")
}

// GetRoleRates returns role hint tallies (keys without "role:" prefix).
func (c *Collector) GetRoleRates() map[string]int {
	return c.prefixedCounts("role:")
}

func (c *Collector) prefixedCounts(prefix string) map[string]int {
	if c == nil {
		return map[string]int{}
	}
	counts := c.GetRouteCounts()
	out := map[string]int{}
	for k, v := range counts {
		if strings.HasPrefix(k, prefix) && v > 0 {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out
}

// GetSnapshot assembles live metrics for the dashboard / API.
func (c *Collector) GetSnapshot() Snapshot {
	if c == nil {
		return Snapshot{Distribution: ComputeDistribution(nil), RouteCounts: map[string]int{}}
	}
	lat := c.GetLatencyPercentiles()
	return Snapshot{
		SessionID:      c.SessionID(),
		Distribution:   c.GetDistribution(),
		ClassRates:     c.GetClassRates(),
		RoleRates:      c.GetRoleRates(),
		RouteCounts:    c.GetRouteCounts(),
		TokenStats:     c.GetTokenStats(),
		TokensSavedEst: c.tokensSavedEst(),
		CostSavings:    c.GetCostSavings(),
		LatencyMs: LatencyMS{
			P50: durationMS(lat.P50),
			P90: durationMS(lat.P90),
			P99: durationMS(lat.P99),
		},
	}
}

func durationMS(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

func (c *Collector) tokensSavedEst() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localTokens
}