package upstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MaxReplayBody 是允许缓冲并重试的请求体上限。
const MaxReplayBody = 8 << 20

// RetryPolicy 描述一次上游调用的重试策略。
type RetryPolicy struct {
	Attempts      int
	PerTryTimeout time.Duration
	On            []string
}

// retryableStatus 判断状态码是否在重试范围内。
func (p RetryPolicy) retryableStatus(status int) bool {
	if p.Attempts <= 0 {
		return false
	}

	for _, condition := range p.On {
		switch strings.ToLower(strings.TrimSpace(condition)) {
		case "5xx", "gateway-error":
			if status >= 500 && status < 600 {
				return true
			}
		case "503":
			if status == http.StatusServiceUnavailable {
				return true
			}
		}
	}

	// 没有显式配置时，默认重试 5xx。
	return len(p.On) == 0 && status >= 500 && status < 600
}

// retryableError 判断网络错误是否值得重试。
func (p RetryPolicy) retryableError(err error) bool {
	if p.Attempts <= 0 || err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	if len(p.On) == 0 {
		return true
	}

	for _, condition := range p.On {
		switch strings.ToLower(strings.TrimSpace(condition)) {
		case "reset", "connect", "network":
			return true
		}
	}

	return false
}

// BufferBody 把请求体读入内存，便于多次重试。
//
// 返回 replayable=false 时，请求体要么超过上限、要么读取失败；此时调用方
// 应该放弃重试。超过上限时会把已读部分和剩余部分重新拼回 r.Body。
func BufferBody(r *http.Request, limit int64) (body []byte, replayable bool, err error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, true, nil
	}

	if limit <= 0 {
		limit = MaxReplayBody
	}

	if r.ContentLength > limit {
		return nil, false, nil
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, false, err
	}

	if int64(len(data)) > limit {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(data), r.Body))
		return nil, false, nil
	}

	return data, true, nil
}

// SendResult 是一次上游请求的结果。
//
// SkipReport 用于故障注入：合成的 500 不应该被算作实例故障，否则会
// 误触发离群摘除。
type SendResult struct {
	Response   *http.Response
	Err        error
	SkipReport bool
}

// Sender 执行一次真正的上游请求。endpoint 已由负载均衡器选好。
type Sender func(ctx context.Context, endpoint *Endpoint, req *http.Request) SendResult

// DoWithRetry 按策略执行请求，每次重试都会重新选择实例。
//
// 返回的 attempts 是实际尝试次数（首次请求记为 1）。
func DoWithRetry(
	ctx context.Context,
	cluster *Cluster,
	req *http.Request,
	policy RetryPolicy,
	body []byte,
	sender Sender,
) (*http.Response, int, error) {
	maxAttempts := 1
	if policy.Attempts > 0 {
		maxAttempts = policy.Attempts + 1
	}

	// 请求体不可重放时只能尝试一次。
	replayable := body != nil || req.Body == nil || req.Body == http.NoBody
	if !replayable {
		maxAttempts = 1
	}

	attempts := 0

	for {
		attempts++

		endpoint, err := cluster.Pick()
		if err != nil {
			return nil, attempts, err
		}

		if err := cluster.Acquire(); err != nil {
			return nil, attempts, err
		}

		attemptCtx := ctx
		var cancel context.CancelFunc

		if policy.PerTryTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, policy.PerTryTimeout)
		}

		outReq := req.Clone(attemptCtx)
		if body != nil {
			outReq.Body = io.NopCloser(bytes.NewReader(body))
			outReq.ContentLength = int64(len(body))
		} else if req.Body != nil && req.Body != http.NoBody {
			if attempts == 1 {
				outReq.Body = req.Body
			} else {
				outReq.Body = http.NoBody
				outReq.ContentLength = 0
			}
		}

		endpoint.Begin()
		result := sender(attemptCtx, endpoint, outReq)
		endpoint.End()
		cluster.Release()

		resp := result.Response
		sendErr := result.Err

		status := 0
		if resp != nil {
			status = resp.StatusCode
		}

		if !result.SkipReport {
			cluster.Report(endpoint, status, sendErr)
		}

		if sendErr != nil && resp != nil {
			drainAndClose(resp.Body)
			resp = nil
			status = 0
		}

		if sendErr != nil {
			if cancel != nil {
				cancel()
			}

			if ctx.Err() != nil {
				return nil, attempts, ctx.Err()
			}

			if attempts < maxAttempts && policy.retryableError(sendErr) {
				if waitBackoff(ctx, attempts) != nil {
					return nil, attempts, ctx.Err()
				}
				continue
			}

			return nil, attempts, sendErr
		}

		if attempts < maxAttempts && policy.retryableStatus(resp.StatusCode) {
			drainAndClose(resp.Body)
			if cancel != nil {
				cancel()
			}

			if waitBackoff(ctx, attempts) != nil {
				return nil, attempts, ctx.Err()
			}
			continue
		}

		if cancel != nil {
			resp.Body = &cancelReadCloser{
				ReadCloser: resp.Body,
				cancel:     cancel,
			}
		}

		return resp, attempts, nil
	}
}

func waitBackoff(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt) * 50 * time.Millisecond

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(body, 32<<10))
	_ = body.Close()
}

// cancelReadCloser 在响应体关闭时释放每次尝试的 context。
type cancelReadCloser struct {
	io.ReadCloser
	once   sync.Once
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.once.Do(c.cancel)
	return err
}
