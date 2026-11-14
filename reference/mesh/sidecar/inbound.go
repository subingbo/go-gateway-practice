package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"example.com/go-gateway-reference/internal/httpx"
	"example.com/go-gateway-reference/mesh/transport"
)

func (s *Sidecar) inboundHandler() http.Handler {
	target, err := url.Parse("http://" + s.config.AppAddr)
	if err != nil {
		s.logger.Error("invalid app address", "app_addr", s.config.AppAddr, "error", err)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			httpx.ErrorJSON(w, http.StatusInternalServerError, "sidecar app address is invalid")
		})
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = pr.In.URL.Path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, proxyErr error) {
			if errors.Is(proxyErr, context.Canceled) {
				return
			}

			s.logger.Warn("inbound proxy failed",
				"request_id", httpx.RequestIDFromContext(r.Context()),
				"path", r.URL.Path,
				"error", proxyErr,
			)

			httpx.ErrorJSON(w, http.StatusBadGateway, "application unavailable")
		},
		FlushInterval: 100 * time.Millisecond,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		requestID := httpx.RequestID(r)
		w.Header().Set(httpx.RequestIDHeader, requestID)
		r = r.WithContext(httpx.WithRequestID(r.Context(), requestID))

		mode := "PERMISSIVE"
		if snapshot := s.Snapshot(); snapshot != nil {
			mode = snapshot.PeerAuthentication(s.config.Service)
		}

		identity, err := transport.VerifyInboundPeer(r.TLS, s.roots, mode)
		if err != nil {
			s.logger.Warn("inbound mTLS rejected",
				"request_id", requestID,
				"path", r.URL.Path,
				"error", err,
			)
			s.meshMetrics.Observe("inbound", "invalid", s.config.Service, http.StatusForbidden, time.Since(start))
			httpx.ErrorJSON(w, http.StatusForbidden, "mTLS verification failed")
			return
		}

		// 身份只能来自已校验的客户端证书，不能信任外部传入的同名头。
		r.Header.Del("X-Mesh-Source")
		r.Header.Del("X-Forwarded-Client-Cert")
		r.Header.Set("X-Mesh-Source", identity)
		r.Header.Set("X-Forwarded-Client-Cert", "URI="+identity)
		r.Header.Set(httpx.RequestIDHeader, requestID)

		recorder := &httpx.StatusRecorder{ResponseWriter: w}
		proxy.ServeHTTP(recorder, r)

		duration := time.Since(start)
		s.meshMetrics.Observe("inbound", identity, s.config.Service, recorder.Status(), duration)

		s.logger.Info("inbound request",
			"request_id", requestID,
			"source", identity,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.Status(),
			"duration_ms", float64(duration.Microseconds())/1000,
		)
	})
}
