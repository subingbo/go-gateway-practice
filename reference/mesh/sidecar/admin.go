package main

import (
	"net/http"
	"time"

	"example.com/go-gateway-reference/internal/httpx"
)

func (s *Sidecar) adminHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": s.config.Service,
			"version": s.config.Version,
		})
	})

	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		ready := s.Snapshot() != nil &&
			s.certManager != nil &&
			s.certManager.Certificate() != nil &&
			s.registered.Load()

		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}

		payload := map[string]any{
			"ready":          ready,
			"registered":     s.registered.Load(),
			"identity":       s.identity,
			"config_version": s.configClient.Version(),
		}

		if s.certManager != nil {
			payload["cert_expiry"] = s.certManager.NotAfter()
			payload["cert_serial"] = s.certManager.Serial()
		}

		httpx.WriteJSON(w, status, payload)
	})

	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		s.configVersionGauge.Set(float64(s.configClient.Version()))
		if s.certManager != nil {
			s.certExpiryGauge.Set(s.certManager.ExpiresInSeconds())
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = s.metrics.WritePrometheus(w)
	})

	mux.HandleFunc("GET /debug/config", func(w http.ResponseWriter, _ *http.Request) {
		snapshot := s.Snapshot()
		if snapshot == nil {
			httpx.ErrorJSON(w, http.StatusServiceUnavailable, "snapshot is not ready")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, snapshot)
	})

	mux.HandleFunc("GET /debug/endpoints", func(w http.ResponseWriter, r *http.Request) {
		if s.router == nil {
			httpx.ErrorJSON(w, http.StatusServiceUnavailable, "router is not ready")
			return
		}

		host := r.URL.Query().Get("host")
		subset := r.URL.Query().Get("subset")
		if host == "" {
			httpx.ErrorJSON(w, http.StatusBadRequest, "host is required")
			return
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"host":      host,
			"subset":    subset,
			"endpoints": s.router.Endpoints(host, subset),
			"time":      time.Now(),
		})
	})

	return mux
}
