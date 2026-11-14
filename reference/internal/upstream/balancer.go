package upstream

import (
	"math/rand/v2"
	"strings"
	"sync/atomic"
)

// Balancer 从可用实例中选择一个。
type Balancer interface {
	Pick(endpoints []*Endpoint, counter *atomic.Uint64) *Endpoint
}

// NewBalancer 根据配置名创建负载均衡器。
func NewBalancer(name string) Balancer {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "RANDOM":
		return Random{}
	case "LEAST_REQUEST", "LEAST_REQ", "LEAST_CONN":
		return LeastRequest{}
	default:
		return RoundRobin{}
	}
}

// RoundRobin 轮询选择。
type RoundRobin struct{}

func (RoundRobin) Pick(endpoints []*Endpoint, counter *atomic.Uint64) *Endpoint {
	if len(endpoints) == 0 {
		return nil
	}

	index := counter.Add(1) - 1
	return endpoints[index%uint64(len(endpoints))]
}

// Random 随机选择，适合演示流量分布。
type Random struct{}

func (Random) Pick(endpoints []*Endpoint, _ *atomic.Uint64) *Endpoint {
	if len(endpoints) == 0 {
		return nil
	}

	return endpoints[rand.IntN(len(endpoints))]
}

// LeastRequest 选择当前在途请求最少的实例；平手时轮询。
type LeastRequest struct{}

func (LeastRequest) Pick(endpoints []*Endpoint, counter *atomic.Uint64) *Endpoint {
	if len(endpoints) == 0 {
		return nil
	}

	start := int(counter.Add(1)-1) % len(endpoints)
	best := endpoints[start]
	bestInFlight := best.inFlight.Load()

	for i := 1; i < len(endpoints); i++ {
		candidate := endpoints[(start+i)%len(endpoints)]
		if inFlight := candidate.inFlight.Load(); inFlight < bestInFlight {
			best = candidate
			bestInFlight = inFlight
		}
	}

	return best
}
