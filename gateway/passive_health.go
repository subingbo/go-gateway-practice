package main

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultPassiveFailureThreshold  = 3
	defaultPassiveFailureWindow     = 10 * time.Second
	defaultPassiveRecoveryThreshold = 1
)

// passiveFailureTracker 维护一个实例的被动失败状态。
// 它不修改主动 /health 检查得到的 healthy，而是单独决定实例是否应被临时摘除。
type passiveFailureTracker struct {
	mu sync.Mutex

	failureThreshold  int
	failureWindow     time.Duration
	recoveryThreshold int

	// failureTimes 是按时间从旧到新排列的固定上限队列。
	// 一达到阈值实例就会被摘除，后续失败不再入队，因此容量不会无限增长。
	failureTimes      []time.Time
	recoverySuccesses int

	ejected atomic.Bool
}

func newPassiveFailureTracker(
	failureThreshold int,
	failureWindow time.Duration,
	recoveryThreshold int,
) passiveFailureTracker {
	if failureThreshold <= 0 {
		failureThreshold = 1
	}
	if failureWindow <= 0 {
		failureWindow = time.Second
	}
	if recoveryThreshold <= 0 {
		recoveryThreshold = 1
	}

	return passiveFailureTracker{
		failureThreshold:  failureThreshold,
		failureWindow:     failureWindow,
		recoveryThreshold: recoveryThreshold,
		failureTimes:      make([]time.Time, 0, failureThreshold),
	}
}

// recordFailure 记录一次真实请求的传输失败。
// 返回 true 只表示本次调用刚刚让实例达到阈值，调用方可据此只记录一次告警。
func (t *passiveFailureTracker) recordFailure(now time.Time) bool {
	if t == nil {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.ejected.Load() {
		return false
	}

	cutoff := now.Add(-t.failureWindow)
	kept := t.failureTimes[:0]
	for _, failureTime := range t.failureTimes {
		if failureTime.Before(cutoff) {
			continue
		}
		kept = append(kept, failureTime)
	}
	t.failureTimes = kept

	t.failureTimes = append(t.failureTimes, now)
	if len(t.failureTimes) < t.failureThreshold {
		return false
	}

	t.recoverySuccesses = 0
	t.ejected.Store(true)

	return true
}

// recordRequestSuccess 打断“连续失败”序列。
// 已摘除实例可能仍有一个较早发出的在途请求返回成功，它不能绕过恢复探测。
func (t *passiveFailureTracker) recordRequestSuccess() {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.ejected.Load() {
		return
	}

	t.failureTimes = t.failureTimes[:0]
}

// recordHealthProbe 只在实例已被动摘除时统计连续恢复探测。
// 返回 true 表示本次探测刚刚完成恢复状态转换。
func (t *passiveFailureTracker) recordHealthProbe(healthy bool) bool {
	if t == nil {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.ejected.Load() {
		return false
	}

	if !healthy {
		t.recoverySuccesses = 0
		return false
	}

	t.recoverySuccesses++
	if t.recoverySuccesses < t.recoveryThreshold {
		return false
	}

	t.failureTimes = t.failureTimes[:0]
	t.recoverySuccesses = 0
	t.ejected.Store(false)

	return true
}

func (t *passiveFailureTracker) isEjected() bool {
	return t != nil && t.ejected.Load()
}
