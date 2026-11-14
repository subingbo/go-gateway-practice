package main

import (
	"context"
	"flag"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"example.com/go-gateway-reference/internal/httpx"
	"example.com/go-gateway-reference/internal/logbuf"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9101", "listen address")
	service := flag.String("service", "user-service", "service name")
	version := flag.String("version", "v1", "service version")
	failRate := flag.Float64("fail-rate", 0, "probability of returning HTTP 500 for /users/* (0-1)")
	logLevel := flag.String("log-level", "info", "debug, info, warn or error")
	flag.Parse()

	logger := slog.New(logbuf.NewHandler(os.Stdout, logbuf.New(200), logbuf.ParseLevel(*logLevel)))

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": *service,
			"version": *version,
		})
	})

	mux.HandleFunc("GET /users/slow", func(w http.ResponseWriter, r *http.Request) {
		delay := 3 * time.Second
		if raw := r.URL.Query().Get("ms"); raw != "" {
			if value, err := strconv.Atoi(raw); err == nil && value > 0 {
				delay = time.Duration(value) * time.Millisecond
			}
		}

		select {
		case <-r.Context().Done():
			logger.Warn("slow user request canceled",
				"request_id", r.Header.Get(httpx.RequestIDHeader),
				"error", r.Context().Err(),
			)
			return
		case <-time.After(delay):
		}

		writeUser(w, r, *service, *version, "slow")
	})

	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		if *failRate > 0 && rand.Float64() < *failRate {
			httpx.WriteJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "injected failure",
				"service": *service,
				"version": *version,
			})
			return
		}

		writeUser(w, r, *service, *version, r.PathValue("id"))
	})

	server := &http.Server{
		Addr:              *addr,
		Handler:           loggingMiddleware(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("user service listening",
			"addr", *addr,
			"service", *service,
			"version", *version,
			"fail_rate", *failRate,
		)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("user service failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}

func writeUser(w http.ResponseWriter, r *http.Request, service, version, userID string) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"service":    service,
		"version":    version,
		"user_id":    userID,
		"path":       r.URL.Path,
		"request_id": r.Header.Get(httpx.RequestIDHeader),
		"source":     r.Header.Get("X-Mesh-Source"),
		"host":       r.Host,
		"timestamp":  time.Now(),
	})
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &httpx.StatusRecorder{ResponseWriter: w}

		next.ServeHTTP(recorder, r)

		logger.Info("user service request",
			"request_id", r.Header.Get(httpx.RequestIDHeader),
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.Status(),
			"duration_ms", float64(time.Since(start).Microseconds())/1000,
			"source", r.Header.Get("X-Mesh-Source"),
		)
	})
}
