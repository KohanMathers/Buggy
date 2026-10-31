package app

import "sync"

type counterVec struct {
	mu sync.Mutex
	m  map[string]int64
}

func (c *counterVec) inc(k string) {
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]int64{}
	}
	c.m[k]++
	c.mu.Unlock()
}

func (c *counterVec) snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.m))
	for k, v := range c.m {
		out[k] = v
	}
	return out
}

var (
	eventsTotal      counterVec
	webhooksTotal    counterVec
	automationsTotal counterVec
)

func Counters() map[string]map[string]int64 {
	return map[string]map[string]int64{
		"buggy_events_total":      eventsTotal.snapshot(),
		"buggy_webhooks_total":    webhooksTotal.snapshot(),
		"buggy_automations_total": automationsTotal.snapshot(),
	}
}
