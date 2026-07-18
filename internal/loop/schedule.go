package loop

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// schedule wakes the runner on a fixed interval and/or cron-like expression.
type schedule struct {
	interval time.Duration
	cron     *cronExpr
}

func parseSchedule(cronSpec, interval string) (*schedule, error) {
	s := &schedule{}
	if interval != "" {
		d, err := time.ParseDuration(interval)
		if err != nil {
			return nil, err
		}
		if d <= 0 {
			return nil, fmt.Errorf("interval must be > 0")
		}
		s.interval = d
	}
	cronSpec = strings.TrimSpace(cronSpec)
	if cronSpec == "" {
		if s.interval == 0 {
			s.interval = 5 * time.Minute
		}
		return s, nil
	}
	if strings.HasPrefix(strings.ToLower(cronSpec), "@every ") {
		d, err := time.ParseDuration(strings.TrimSpace(cronSpec[7:]))
		if err != nil {
			return nil, fmt.Errorf("@every: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("@every must be > 0")
		}
		s.interval = d
		return s, nil
	}
	ce, err := parseCron(cronSpec)
	if err != nil {
		return nil, err
	}
	s.cron = ce
	if s.interval == 0 {
		// Poll cadence while waiting for the next cron match.
		s.interval = time.Minute
	}
	return s, nil
}

func (s *schedule) nextDelay(from time.Time) time.Duration {
	if s == nil {
		return 5 * time.Minute
	}
	if s.cron == nil {
		if s.interval > 0 {
			return s.interval
		}
		return 5 * time.Minute
	}
	next := s.cron.nextAfter(from)
	d := next.Sub(from)
	if d < time.Second {
		d = time.Second
	}
	return d
}

// cronExpr is a minimal 5-field cron (min hour dom mon dow). Supports *, N, */N, lists.
type cronExpr struct {
	min, hour, dom, mon, dow cronField
}

type cronField struct {
	any  bool
	vals map[int]struct{}
}

func parseCron(spec string) (*cronExpr, error) {
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return nil, fmt.Errorf("want 5 fields (min hour dom mon dow), got %d", len(parts))
	}
	min, err := parseCronField(parts[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	hour, err := parseCronField(parts[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	dom, err := parseCronField(parts[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("dom: %w", err)
	}
	mon, err := parseCronField(parts[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	dow, err := parseCronField(parts[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("dow: %w", err)
	}
	return &cronExpr{min: min, hour: hour, dom: dom, mon: mon, dow: dow}, nil
}

func parseCronField(s string, lo, hi int) (cronField, error) {
	s = strings.TrimSpace(s)
	if s == "*" {
		return cronField{any: true}, nil
	}
	out := cronField{vals: make(map[int]struct{})}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(part[2:])
			if err != nil || step <= 0 {
				return cronField{}, fmt.Errorf("bad step %q", part)
			}
			for v := lo; v <= hi; v += step {
				out.vals[v] = struct{}{}
			}
			continue
		}
		if strings.Contains(part, "-") {
			ab := strings.SplitN(part, "-", 2)
			a, err1 := strconv.Atoi(ab[0])
			b, err2 := strconv.Atoi(ab[1])
			if err1 != nil || err2 != nil || a < lo || b > hi || a > b {
				return cronField{}, fmt.Errorf("bad range %q", part)
			}
			for v := a; v <= b; v++ {
				out.vals[v] = struct{}{}
			}
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v < lo || v > hi {
			return cronField{}, fmt.Errorf("bad value %q", part)
		}
		out.vals[v] = struct{}{}
	}
	if len(out.vals) == 0 {
		return cronField{}, fmt.Errorf("empty field")
	}
	return out, nil
}

func (f cronField) match(v int) bool {
	if f.any {
		return true
	}
	_, ok := f.vals[v]
	return ok
}

func (c *cronExpr) matches(t time.Time) bool {
	if c == nil {
		return true
	}
	return c.min.match(t.Minute()) &&
		c.hour.match(t.Hour()) &&
		c.dom.match(t.Day()) &&
		c.mon.match(int(t.Month())) &&
		c.dow.match(int(t.Weekday()))
}

func (c *cronExpr) nextAfter(from time.Time) time.Time {
	t := from.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 60*24*366; i++ { // up to ~1 year
		if c.matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return from.Add(time.Hour)
}
