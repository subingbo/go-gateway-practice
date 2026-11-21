package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// retryTargetSelector 为重试选择另一台可用实例。
type retryTargetSelector interface {
	NextExcluding(excluded *url.URL) *url.URL
}

// retryingRoundTripper 只对可安全重放的幂等请求做一次跨实例重试。
// 两次尝试沿用同一个 Context，因此共享 Gateway 的总超时时间。
type retryingRoundTripper struct {
	base     http.RoundTripper
	selector retryTargetSelector
	reporter upstreamOutcomeReporter
}

func newRetryingRoundTripper(
	base http.RoundTripper,
	selector retryTargetSelector,
	reporter upstreamOutcomeReporter,
) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}

	return &retryingRoundTripper{
		base:     base,
		selector: selector,
		reporter: reporter,
	}
}

func (t *retryingRoundTripper) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err == nil {
		return response, nil
	}

	if !requestCanRetry(req) || req.Context().Err() != nil || t.selector == nil {
		return nil, err
	}

	failedTarget, ok := selectedUpstreamFromContext(req.Context())
	if !ok {
		return nil, err
	}

	retryTarget := t.selector.NextExcluding(failedTarget)
	if retryTarget == nil {
		return nil, err
	}

	retryRequest, cloneErr := cloneRequestForRetry(req, retryTarget)
	if cloneErr != nil {
		return nil, err
	}

	// 第一台实例已经产生传输错误；第二次尝试的最终失败由 ErrorHandler 上报。
	// 先成功构造重试请求再上报，避免构造失败后 ErrorHandler 把同一次失败重复计算。
	if t.reporter != nil {
		t.reporter.ReportFailure(failedTarget)
	}

	response, retryErr := t.base.RoundTrip(retryRequest)
	if retryErr != nil {
		return nil, &upstreamRoundTripError{
			target: retryTarget,
			err:    retryErr,
		}
	}

	return response, nil
}

func requestCanRetry(req *http.Request) bool {
	if req == nil || (req.Method != http.MethodGet && req.Method != http.MethodHead) {
		return false
	}

	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

func cloneRequestForRetry(
	req *http.Request,
	target *url.URL,
) (*http.Request, error) {
	ctx := withSelectedUpstream(req.Context(), target)
	retryRequest := req.Clone(ctx)

	retryURL := *req.URL
	retryURL.Scheme = target.Scheme
	retryURL.Host = target.Host
	retryURL.User = target.User
	retryRequest.URL = &retryURL

	if req.Body != nil && req.Body != http.NoBody {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		retryRequest.Body = body
	}

	return retryRequest, nil
}

// upstreamRoundTripError 记录最后一次尝试的实例地址，
// 让 ReverseProxy 的 ErrorHandler 能把失败归属到正确实例。
type upstreamRoundTripError struct {
	target *url.URL
	err    error
}

func (e *upstreamRoundTripError) Error() string {
	return e.err.Error()
}

func (e *upstreamRoundTripError) Unwrap() error {
	return e.err
}

func failedUpstreamFromError(
	ctx context.Context,
	err error,
) (*url.URL, bool) {
	var roundTripErr *upstreamRoundTripError
	if errors.As(err, &roundTripErr) && roundTripErr.target != nil {
		return roundTripErr.target, true
	}

	return selectedUpstreamFromContext(ctx)
}
