package main

import (
	"log"
	"sync"
	"time"
)

type upstreamEventKind uint8

const (
	upstreamEventEjected upstreamEventKind = iota + 1
	upstreamEventRecovered
)

// upstreamEvent 是负载均衡器发给异步日志 Goroutine 的状态转换快照。
type upstreamEvent struct {
	Kind             upstreamEventKind
	Target           string
	Failures         int
	Window           time.Duration
	SuccessfulProbes int
}

type upstreamEventPublisher interface {
	// Publish 只负责非阻塞入队；返回 false 表示缓冲区已满或日志器已关闭。
	Publish(event upstreamEvent) bool
}

// asyncUpstreamEventLogger 把日志 I/O 从业务请求和健康检查 Goroutine 中移走。
// Publish 只把低频状态转换追加到内存队列，后台 Goroutine 负责逐条写出。
type asyncUpstreamEventLogger struct {
	logger *log.Logger

	queueMu sync.Mutex
	queue   []upstreamEvent
	wake    *sync.Cond
	closed  bool

	closeOnce sync.Once
	wg        sync.WaitGroup
}

func newAsyncUpstreamEventLogger(logger *log.Logger) *asyncUpstreamEventLogger {
	if logger == nil {
		logger = log.Default()
	}

	result := &asyncUpstreamEventLogger{
		logger: logger,
	}
	result.wake = sync.NewCond(&result.queueMu)
	result.wg.Add(1)

	go result.run()

	return result
}

func (l *asyncUpstreamEventLogger) Publish(event upstreamEvent) bool {
	if l == nil {
		return false
	}

	l.queueMu.Lock()
	defer l.queueMu.Unlock()

	if l.closed {
		return false
	}

	l.queue = append(l.queue, event)
	l.wake.Signal()

	return true
}

func (l *asyncUpstreamEventLogger) run() {
	defer l.wg.Done()

	for {
		l.queueMu.Lock()
		for len(l.queue) == 0 && !l.closed {
			l.wake.Wait()
		}

		if len(l.queue) == 0 && l.closed {
			l.queueMu.Unlock()
			return
		}

		event := l.queue[0]
		l.queue[0] = upstreamEvent{}
		l.queue = l.queue[1:]
		if len(l.queue) == 0 {
			l.queue = nil
		}
		l.queueMu.Unlock()

		switch event.Kind {
		case upstreamEventEjected:
			l.logger.Printf(
				"[WARN] upstream passively ejected target=%s failures=%d window=%s",
				event.Target,
				event.Failures,
				event.Window,
			)

		case upstreamEventRecovered:
			l.logger.Printf(
				"[INFO] upstream recovered target=%s successful_probes=%d",
				event.Target,
				event.SuccessfulProbes,
			)
		}
	}
}

// Close 禁止新事件入队，唤醒后台 Goroutine，并等待它写完全部已入队事件。
// 调用方应先停止所有代理请求和健康检查生产者。
func (l *asyncUpstreamEventLogger) Close() {
	if l == nil {
		return
	}

	l.closeOnce.Do(func() {
		l.queueMu.Lock()
		l.closed = true
		l.wake.Broadcast()
		l.queueMu.Unlock()

		l.wg.Wait()
	})
}
