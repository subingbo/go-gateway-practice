package main

import (
	"context"
	"errors"
	"example.com/go-gateway-practice/internal/discovery"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultOrderPort = 9091
	defaultNacosAddr = "127.0.0.1:8848"
	orderServiceName = "order-service"
	defaultServiceIP = "127.0.0.1"
)

type serviceConfig struct {
	port      uint64
	nacosHost string
	nacosPort uint64
	serviceIP string
}

type serviceRegistry interface {
	Register(serviceName string, ip string, port uint64) error
	Deregister(serviceName string, ip string, port uint64) error
	Close()
}

// 编译期检查：生产环境使用的 *discovery.Client 必须实现 serviceRegistry。
// 普通“转到定义”会跳到上面的接口；要查看真实代码请使用“转到实现”。
var _ serviceRegistry = (*discovery.Client)(nil)

func loadConfig(getenv func(string) string) (serviceConfig, error) {
	rawPort := strings.TrimSpace(getenv("ORDER_SERVICE_PORT"))
	if rawPort == "" {
		rawPort = getenv("PORT")
	}
	port, err := parsePort("ORDER_SERVICE_PORT", rawPort, defaultOrderPort)
	if err != nil {
		return serviceConfig{}, err
	}

	nacosAddr := strings.TrimSpace(getenv("NACOS_ADDR"))
	if nacosAddr == "" {
		nacosAddr = defaultNacosAddr
	}

	nacosHost, rawNacosPort, err := net.SplitHostPort(nacosAddr)
	if err != nil {
		return serviceConfig{}, fmt.Errorf("parse NACOS_ADDR %q: %w", nacosAddr, err)
	}
	if strings.TrimSpace(nacosHost) == "" {
		return serviceConfig{}, fmt.Errorf("NACOS_ADDR host is required")
	}

	nacosPort, err := parsePort("NACOS_ADDR port", rawNacosPort, 0)
	if err != nil {
		return serviceConfig{}, err
	}

	serviceIP := strings.TrimSpace(getenv("ORDER_SERVICE_IP"))
	if serviceIP == "" {
		serviceIP = strings.TrimSpace(getenv("SERVICE_IP"))
	}
	if serviceIP == "" {
		serviceIP = defaultServiceIP
	}
	parsedServiceIP := net.ParseIP(serviceIP)
	if parsedServiceIP == nil || parsedServiceIP.IsUnspecified() {
		return serviceConfig{}, fmt.Errorf("ORDER_SERVICE_IP must be a reachable IP address")
	}

	return serviceConfig{
		port:      port,
		nacosHost: nacosHost,
		nacosPort: nacosPort,
		serviceIP: serviceIP,
	}, nil
}

func parsePort(name string, raw string, defaultPort uint64) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if defaultPort == 0 {
			return 0, fmt.Errorf("%s is required", name)
		}
		return defaultPort, nil
	}

	port, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("%s must be an integer between 1 and 65535", name)
	}

	return port, nil
}

func newOrderMux() *http.ServeMux {
	// 创建订单服务自己的路由器
	mux := http.NewServeMux()

	// 健康检查接口
	mux.HandleFunc("GET /health", func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Order Service OK"))
	})

	// 注册查询订单接口

	mux.HandleFunc("GET /orders/slow", func(
		w http.ResponseWriter,
		r *http.Request,
	) {

		log.Println("Slow order request started")

		// 模拟一个需要 5 秒的业务任务
		select {

		case <-time.After(5 * time.Second):

			fmt.Fprintln(w, "Slow order completed")

		case <-r.Context().Done():

			// 客户端或 Gateway 取消了请求
			log.Printf(
				"Slow order canceled: %v",
				r.Context().Err(),
			)

			return
		}
	})

	return mux
}

func runOrderService(
	ctx context.Context,
	cfg serviceConfig,
	registry serviceRegistry,
	listener net.Listener,
) (returnErr error) {
	if ctx == nil {
		return errors.New("service context is required")
	}
	if registry == nil {
		return errors.New("service registry is required")
	}
	if listener == nil {
		return errors.New("HTTP listener is required")
	}

	defer listener.Close()
	defer registry.Close()

	if err := registry.Register(orderServiceName, cfg.serviceIP, cfg.port); err != nil {
		return fmt.Errorf("register Order Service: %w", err)
	}

	// registered 只用于防止重复注销，不代表 Nacos 中的实际状态
	registered := true

	deregister := func() error {
		if !registered {
			return nil
		}
		registered = false

		// registry 的实际类型是 *discovery.Client。
		// 这里会继续调用 discovery.Client.Deregister，向 Nacos 注销当前实例。
		if err := registry.Deregister(orderServiceName, cfg.serviceIP, cfg.port); err != nil {
			return fmt.Errorf("deregister Order Service: %w", err)
		}
		return nil
	}

	defer func() {
		returnErr = errors.Join(returnErr, deregister())
	}()

	server := &http.Server{
		Handler:           newOrderMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()

	log.Printf(
		"Order Service listening on :%d and registered with Nacos at %s:%d",
		cfg.port,
		cfg.nacosHost,
		cfg.nacosPort,
	)

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve Order Service: %w", err)
		}
		return nil

	case <-ctx.Done():
		log.Printf("Order Service shutdown signal received: %v", ctx.Err())
		returnErr = errors.Join(returnErr, deregister())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		if closeErr := server.Close(); closeErr != nil {
			return errors.Join(
				returnErr,
				fmt.Errorf("gracefully shut down Order Service: %w", err),
				fmt.Errorf("force close Order Service: %w", closeErr),
			)
		}
		return errors.Join(returnErr, fmt.Errorf("gracefully shut down Order Service: %w", err))
	}

	if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.Join(returnErr, fmt.Errorf("Order Service exit: %w", err))
	}

	return returnErr
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.port))
	if err != nil {
		log.Fatalf("Listen on port %d: %v", cfg.port, err)
	}

	registry, err := discovery.NewClient(cfg.nacosHost, cfg.nacosPort)
	if err != nil {
		_ = listener.Close()
		log.Fatalf("Initialize Nacos client: %v", err)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if err := runOrderService(ctx, cfg, registry, listener); err != nil {
		log.Fatal(err)
	}
}
