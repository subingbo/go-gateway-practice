package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"example.com/go-gateway-reference/internal/httpx"
)

func (s *Sidecar) outboundHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		requestID := httpx.RequestID(r)
		w.Header().Set(httpx.RequestIDHeader, requestID)
		r = r.WithContext(httpx.WithRequestID(r.Context(), requestID))

		if r.Method == http.MethodConnect {
			httpx.ErrorJSON(w, http.StatusMethodNotAllowed, "CONNECT is not supported")
			return
		}

		host := r.URL.Hostname()
		if host == "" {
			host = r.Host
			if parsed, _, err := net.SplitHostPort(host); err == nil {
				host = parsed
			}
		}

		if host == "" {
			httpx.ErrorJSON(w, http.StatusBadRequest, "mesh destination is required")
			return
		}

		response, err := s.router.Do(r.Context(), r, host)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}

			s.logger.Warn("mesh request failed",
				"request_id", requestID,
				"destination", host,
				"error", err,
			)
			httpx.ErrorJSON(w, http.StatusBadGateway, "mesh request failed")
			return
		}
		defer response.Body.Close()

		httpx.CopyHeaders(w.Header(), response.Header)
		w.Header().Set("X-Mesh-Destination", host)
		w.Header().Set(httpx.RequestIDHeader, requestID)
		w.WriteHeader(response.StatusCode)

		if _, err := io.Copy(w, response.Body); err != nil {
			s.logger.Debug("copy mesh response failed",
				"request_id", requestID,
				"destination", host,
				"error", err,
			)
		}

		s.logger.Info("outbound request",
			"request_id", requestID,
			"source", s.config.Service,
			"destination", host,
			"method", r.Method,
			"path", r.URL.Path,
			"status", response.StatusCode,
			"duration_ms", float64(time.Since(start).Microseconds())/1000,
		)
	})
}
