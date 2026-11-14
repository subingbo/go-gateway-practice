// Package logbuf 提供“结构化日志 + 环形缓冲 + SSE 订阅”。
//
// 网关把每条日志同时写到 stdout 和内存环形缓冲。看板通过 SSE 订阅缓冲，
// 这样不需要额外的日志系统也能观察请求流转。
package logbuf

import (
	"sync"
)

// Buffer 是一个固定容量的日志环形缓冲。
type Buffer struct {
	mu          sync.Mutex
	max         int
	lines       [][]byte
	subscribers map[chan []byte]struct{}
}

// New 创建缓冲；max <= 0 时默认 500 行。
func New(max int) *Buffer {
	if max <= 0 {
		max = 500
	}

	return &Buffer{
		max:         max,
		lines:       make([][]byte, 0, max),
		subscribers: make(map[chan []byte]struct{}),
	}
}

// Add 追加一行日志并通知订阅者。
func (b *Buffer) Add(line []byte) {
	if b == nil {
		return
	}

	copied := append([]byte(nil), line...)

	b.mu.Lock()
	b.lines = append(b.lines, copied)
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}

	subscribers := make([]chan []byte, 0, len(b.subscribers))
	for ch := range b.subscribers {
		subscribers = append(subscribers, ch)
	}
	b.mu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- copied:
		default:
			// 订阅者太慢时丢弃新日志，避免阻塞业务请求。
		}
	}
}

// Lines 返回当前缓冲的副本。
func (b *Buffer) Lines() [][]byte {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([][]byte, len(b.lines))
	for i, line := range b.lines {
		out[i] = append([]byte(nil), line...)
	}
	return out
}

// Subscribe 订阅新日志。调用 cancel 取消订阅。
func (b *Buffer) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)

	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			// 不关闭 channel：Add 可能在解锁后仍持有它的副本，
			// 关闭后发送会 panic。订阅方通过请求 context 退出。
			b.mu.Lock()
			delete(b.subscribers, ch)
			b.mu.Unlock()
		})
	}

	return ch, cancel
}
