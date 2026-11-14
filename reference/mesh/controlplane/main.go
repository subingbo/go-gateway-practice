package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"example.com/go-gateway-reference/internal/logbuf"
	"example.com/go-gateway-reference/internal/upstream"
)

func main() {
	addr := flag.String("addr", ":18000", "control plane listen address")
	configDir := flag.String("config-dir", "configs/mesh", "directory containing VirtualService / DestinationRule YAML")
	dataDir := flag.String("data-dir", "data/ca", "directory for the root CA")
	instanceTTL := flag.Duration("ttl", 45*time.Second, "instance heartbeat TTL")
	checkInterval := flag.Duration("check-interval", 2*time.Second, "active health check interval")
	checkTimeout := flag.Duration("check-timeout", time.Second, "active health check timeout")
	unhealthyThreshold := flag.Int("unhealthy-threshold", 3, "consecutive failures before marking unhealthy")
	healthyThreshold := flag.Int("healthy-threshold", 2, "consecutive successes before marking healthy")
	logLevel := flag.String("log-level", "info", "debug, info, warn or error")
	resetCA := flag.Bool("reset-ca", false, "delete the existing root CA before starting")
	flag.Parse()

	logger := slog.New(logbuf.NewHandler(os.Stdout, logbuf.New(200), logbuf.ParseLevel(*logLevel)))

	if *resetCA {
		if err := os.RemoveAll(*dataDir); err != nil {
			logger.Error("reset CA failed", "error", err)
			os.Exit(1)
		}
	}

	ca, err := NewCA(*dataDir, logger)
	if err != nil {
		logger.Error("initialize CA failed", "error", err)
		os.Exit(1)
	}

	store := NewConfigStore(*configDir, ca.RootPEM(), logger)
	if err := store.Load(); err != nil {
		logger.Error("load mesh config failed", "error", err)
		os.Exit(1)
	}

	checkConfig := upstream.HealthCheckConfig{
		Interval:           *checkInterval,
		Timeout:            *checkTimeout,
		UnhealthyThreshold: *unhealthyThreshold,
		HealthyThreshold:   *healthyThreshold,
	}

	registry := NewRegistry(*instanceTTL, checkConfig, logger, store.Bump)
	store.SetRegistry(registry)

	server := NewServer(registry, store, ca, logger)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go registry.Run(ctx)
	go store.Run(ctx)

	go func() {
		logger.Info("mesh control plane listening",
			"addr", *addr,
			"config_dir", *configDir,
			"data_dir", *dataDir,
		)

		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("control plane server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down mesh control plane")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = httpServer.Shutdown(shutdownCtx)
	registry.Stop()
}
