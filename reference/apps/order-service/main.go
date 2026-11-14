package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/go-gateway-reference/internal/httpx"
	"example.com/go-gateway-reference/internal/logbuf"
	"example.com/go-gateway-reference/mesh/sdk"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9103", "listen address")
	sidecarAddr := flag.String("sidecar", "127.0.0.1:15003", "local sidecar outbound address")
	userService := flag.String("user-service", "user-service", "user service name in the mesh")
	logLevel := flag.String("log-level", "info", "debug, info, warn or error")
	flag.Parse()

	logger := slog.New(logbuf.NewHandler(os.Stdout, logbuf.New(200), logbuf.ParseLevel(*logLevel)))
	meshClient := sdk.New(*sidecarAddr)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "order-service",
		})
	})

	mux.HandleFunc("GET /orders/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			logger.Warn("slow order request canceled",
				"request_id", r.Header.Get(httpx.RequestIDHeader),
				"error", r.Context().Err(),
			)
			return
		case <-time.After(5 * time.Second):
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"service":    "order-service",
			"order_id":   "slow",
			"request_id": r.Header.Get(httpx.RequestIDHeader),
			"timestamp":  time.Now(),
		})
	})

	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("id")
		requestID := httpx.RequestID(r)

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		target := fmt.Sprintf("http://%s/users/%s", *userService, orderID)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			httpx.ErrorJSON(w, http.StatusInternalServerError, "create mesh request failed")
			return
		}
		request.Header.Set(httpx.RequestIDHeader, requestID)

		response, err := meshClient.Do(request)
		if err != nil {
			logger.Warn("call user service failed", "request_id", requestID, "error", err)
			httpx.ErrorJSON(w, http.StatusBadGateway, "user service unavailable")
			return
		}
		defer response.Body.Close()

		userBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			httpx.ErrorJSON(w, http.StatusBadGateway, "read user service response failed")
			return
		}

		if response.StatusCode != http.StatusOK {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(userBody)
			return
		}

		var user map[string]any
		if err := json.Unmarshal(userBody, &user); err != nil {
			httpx.ErrorJSON(w, http.StatusBadGateway, "invalid user service response")
			return
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"service":    "order-service",
			"order_id":   orderID,
			"user":       user,
			"request_id": requestID,
			"timestamp":  time.Now(),
		})
	})

	mux.HandleFunc("GET /admin/ping", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status":     "admin ok",
			"service":    "order-service",
			"request_id": r.Header.Get(httpx.RequestIDHeader),
		})
	})

	server := &http.Server{
		Addr:              *addr,
		Handler:           loggingMiddleware(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("order service listening",
			"addr", *addr,
			"sidecar", *sidecarAddr,
			"user_service", *userService,
		)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("order service failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	meshClient.CloseIdleConnections()
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &httpx.StatusRecorder{ResponseWriter: w}

		next.ServeHTTP(recorder, r)

		logger.Info("order service request",
			"request_id", r.Header.Get(httpx.RequestIDHeader),
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.Status(),
			"duration_ms", float64(time.Since(start).Microseconds())/1000,
		)
	})
}
