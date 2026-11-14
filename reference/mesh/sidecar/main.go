package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"example.com/go-gateway-reference/internal/logbuf"
)

func main() {
	var config Config
	var labels string
	var logLevel string

	flag.StringVar(&config.Service, "service", "", "service name (e.g. user-service)")
	flag.StringVar(&config.Namespace, "namespace", "default", "mesh namespace")
	flag.StringVar(&config.Version, "version", "v1", "service version label")
	flag.StringVar(&config.AppAddr, "app-addr", "", "local application address (e.g. 127.0.0.1:9101)")
	flag.StringVar(&config.InboundAddr, "inbound", "127.0.0.1:19101", "inbound mTLS listen address")
	flag.StringVar(&config.OutboundAddr, "outbound", "127.0.0.1:15001", "outbound HTTP proxy listen address")
	flag.StringVar(&config.AdminAddr, "admin", "127.0.0.1:19201", "admin/metrics listen address")
	flag.StringVar(&config.ControlPlaneURL, "control-plane", "http://127.0.0.1:18000", "control plane URL")
	flag.StringVar(&config.HealthPath, "health-path", "/healthz", "application health check path")
	flag.DurationVar(&config.CertTTL, "cert-ttl", 10*time.Minute, "requested certificate TTL")
	flag.StringVar(&labels, "labels", "", "additional instance labels, e.g. zone=local")
	flag.StringVar(&logLevel, "log-level", "info", "debug, info, warn or error")
	flag.Parse()

	logger := slog.New(logbuf.NewHandler(os.Stdout, logbuf.New(200), logbuf.ParseLevel(logLevel)))
	config.Logger = logger
	config.Labels = parseLabels(labels, logger)

	sidecar, err := New(config)
	if err != nil {
		logger.Error("invalid sidecar configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := sidecar.Start(ctx); err != nil {
		logger.Error("start sidecar failed", "error", err)
		os.Exit(1)
	}

	logger.Info("sidecar ready",
		"service", config.Service,
		"version", config.Version,
		"identity", sidecar.identity,
		"instance", sidecar.instanceID,
		"inbound", config.InboundAddr,
		"outbound", config.OutboundAddr,
		"admin", config.AdminAddr,
	)

	select {
	case <-ctx.Done():
		logger.Info("shutting down sidecar")
	case err := <-sidecar.Errors():
		logger.Error("sidecar server failed", "error", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sidecar.Shutdown(shutdownCtx)
}

func parseLabels(raw string, logger *slog.Logger) map[string]string {
	labels := make(map[string]string)

	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			logger.Warn("ignoring malformed label", "label", pair)
			continue
		}

		labels[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	return labels
}
