package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPassiveFailureTrackerEjectsAtThresholdWithinWindow(t *testing.T) {
	t.Parallel()

	tracker := newPassiveFailureTracker(3, 10*time.Second, 2)
	base := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

	if tracker.recordFailure(base) {
		t.Fatal("first failure triggered ejection")
	}
	if tracker.recordFailure(base.Add(time.Second)) {
		t.Fatal("second failure triggered ejection")
	}
	if !tracker.recordFailure(base.Add(2 * time.Second)) {
		t.Fatal("third failure in window did not trigger ejection")
	}
	if !tracker.isEjected() {
		t.Fatal("tracker is not ejected after reaching threshold")
	}

	// 已摘除状态下的更多失败不能重复产生状态转换和警告事件。
	if tracker.recordFailure(base.Add(3 * time.Second)) {
		t.Fatal("failure after ejection triggered a duplicate transition")
	}
}

func TestPassiveFailureTrackerPrunesExpiredFailures(t *testing.T) {
	t.Parallel()

	tracker := newPassiveFailureTracker(3, 10*time.Second, 2)
	base := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

	tracker.recordFailure(base)
	tracker.recordFailure(base.Add(6 * time.Second))

	// 第一次失败已经落在十秒窗口之外，此时窗口中只有两条有效记录。
	if tracker.recordFailure(base.Add(11 * time.Second)) {
		t.Fatal("expired failure was incorrectly counted toward threshold")
	}

	if !tracker.recordFailure(base.Add(12 * time.Second)) {
		t.Fatal("three recent failures did not trigger ejection")
	}
}

func TestPassiveFailureTrackerRequestSuccessResetsWindow(t *testing.T) {
	t.Parallel()

	tracker := newPassiveFailureTracker(3, 10*time.Second, 2)
	base := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

	tracker.recordFailure(base)
	tracker.recordFailure(base.Add(time.Second))
	tracker.recordRequestSuccess()

	if tracker.recordFailure(base.Add(2 * time.Second)) {
		t.Fatal("first failure after a success triggered ejection")
	}
	if tracker.recordFailure(base.Add(3 * time.Second)) {
		t.Fatal("second failure after a success triggered ejection")
	}
	if tracker.isEjected() {
		t.Fatal("tracker was ejected after success reset the consecutive sequence")
	}
}

func TestPassiveFailureTrackerRecoversAfterOneHealthyProbe(t *testing.T) {
	t.Parallel()

	tracker := newPassiveFailureTracker(3, 10*time.Second, 1)
	base := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

	tracker.recordFailure(base)
	tracker.recordFailure(base.Add(time.Second))
	tracker.recordFailure(base.Add(2 * time.Second))

	if tracker.recordHealthProbe(false) {
		t.Fatal("failed health probe reported recovery")
	}
	if !tracker.recordHealthProbe(true) {
		t.Fatal("healthy probe did not report recovery")
	}
	if tracker.isEjected() {
		t.Fatal("target remained ejected after recovery threshold")
	}
}

func TestPassiveFailureTrackerRequestSuccessCannotRecoverEjectedTarget(t *testing.T) {
	t.Parallel()

	tracker := newPassiveFailureTracker(3, 10*time.Second, 2)
	base := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

	tracker.recordFailure(base)
	tracker.recordFailure(base.Add(time.Second))
	tracker.recordFailure(base.Add(2 * time.Second))

	// 这是摘除前已经发出的在途请求；它后来成功也不能绕过恢复探测。
	tracker.recordRequestSuccess()

	if !tracker.isEjected() {
		t.Fatal("in-flight request success recovered an ejected target")
	}
}

func TestPassiveFailureTrackerConcurrentFailuresEmitOneTransition(t *testing.T) {
	t.Parallel()

	tracker := newPassiveFailureTracker(3, 10*time.Second, 2)
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

	const goroutines = 100
	var wg sync.WaitGroup
	var transitions atomic.Int32

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tracker.recordFailure(now) {
				transitions.Add(1)
			}
		}()
	}

	wg.Wait()

	if got := transitions.Load(); got != 1 {
		t.Fatalf("ejection transitions = %d, want 1", got)
	}
}
