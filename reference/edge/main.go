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

	"example.com/go-gateway-reference/internal/config"
	"example.com/go-gateway-reference/internal/logbuf"
)

func main() {
	configPath := flag.String("config", "configs/gateway.yaml", "gateway configuration file")
	logLevel := flag.String("log-level", "info", "debug, info, warn or error")
	flag.Parse()

	logs := logbuf.New(500)
	logger := slog.New(logbuf.NewHandler(os.Stdout, logs, logbuf.ParseLevel(*logLevel)))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load gateway config failed", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gateway, err := NewGateway(ctx, *configPath, cfg, logs, logger)
	if err != nil {
		logger.Error("initialize gateway failed", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           gateway,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
	}

	watcher := config.NewWatcher(*configPath, logger)
	go watcher.Run(ctx, gateway.Reload)

	go func() {
		logger.Info("edge gateway listening",
			"addr", cfg.Server.Addr,
			"config", *configPath,
			"mesh", gateway.mesh != nil,
		)

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("edge gateway failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down edge gateway")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	_ = server.Shutdown(shutdownCtx)
	gateway.Close()

	// 给日志缓冲一点时间输出最后几条记录。
	time.Sleep(100 * time.Millisecond)
}
