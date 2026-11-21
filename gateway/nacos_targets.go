package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"example.com/go-gateway-practice/internal/discovery"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
)

const defaultGatewayNacosAddress = "127.0.0.1:8848"

// nacosServerConfig 是 Gateway 创建 Nacos 客户端所需的连接配置。
type nacosServerConfig struct {
	address string
	host    string
	port    uint64
}

// healthyInstanceDiscovery 是 Gateway 对服务发现能力的最小依赖。
// 生产环境由 *discovery.Client 实现，测试可以传入不连接真实 Nacos 的实现。
type healthyInstanceDiscovery interface {
	GetHealthyInstances(serviceName string) ([]model.Instance, error)
}

// 编译期检查：真实的 Nacos Client 必须实现 Gateway 需要的查询能力。
var _ healthyInstanceDiscovery = (*discovery.Client)(nil)

// loadNacosServerConfig 读取并校验 NACOS_ADDR。
// 使用函数参数接收 getenv，是为了让测试不修改当前进程的真实环境变量。
func loadNacosServerConfig(getenv func(string) string) (nacosServerConfig, error) {
	address := strings.TrimSpace(getenv("NACOS_ADDR"))
	if address == "" {
		address = defaultGatewayNacosAddress
	}

	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return nacosServerConfig{}, fmt.Errorf(
			"parse NACOS_ADDR %q: %w",
			address,
			err,
		)
	}

	host = strings.TrimSpace(host)
	if host == "" {
		return nacosServerConfig{}, fmt.Errorf("NACOS_ADDR host is required")
	}

	port, err := strconv.ParseUint(strings.TrimSpace(rawPort), 10, 16)
	if err != nil || port == 0 {
		return nacosServerConfig{}, fmt.Errorf(
			"NACOS_ADDR port must be an integer between 1 and 65535",
		)
	}

	return nacosServerConfig{
		address: address,
		host:    host,
		port:    port,
	}, nil
}

// nacosInstancesToTargets 把已经由 Nacos 筛选过的实例转换成负载均衡器使用的 URL。
// GetHealthyInstances 底层使用 SelectInstances，已经排除了不健康、被禁用和权重为 0 的实例；
// 因此这个函数只负责地址格式转换和基本的 IP、端口校验，不重复判断实例状态。
func nacosInstancesToTargets(instances []model.Instance) []string {
	targets := make([]string, 0, len(instances))

	for _, instance := range instances {
		// Nacos 的 Instance.Ip 理论上应该是 IP，但边界数据仍需要校验，
		// 避免一个损坏的实例地址进入负载均衡器。
		ip := net.ParseIP(strings.TrimSpace(instance.Ip))
		if ip == nil || instance.Port == 0 || instance.Port > 65535 {
			continue
		}

		// JoinHostPort 会自动给 IPv6 地址添加方括号：
		// 2001:db8::1 + 9091 -> [2001:db8::1]:9091。
		address := net.JoinHostPort(
			ip.String(),
			strconv.FormatUint(instance.Port, 10),
		)

		targets = append(targets, "http://"+address)
	}

	return targets
}

// resolveRouteTargets 决定一条路由启动时应该使用哪组后端地址。
// Nacos 可用且返回合法实例时使用动态地址；查询失败或结果不可用时返回静态兜底地址。
// 即使已经返回兜底地址，也保留 error 给调用方记录告警，避免故障被静默隐藏。
func resolveRouteTargets(
	registry healthyInstanceDiscovery,
	route proxyRoute,
) ([]string, error) {
	// 复制切片，避免调用方后续修改结果时意外改动全局路由配置。
	fallbackTargets := append([]string(nil), route.FallbackTargets...)

	instances, err := registry.GetHealthyInstances(route.ServiceName)
	if err != nil {
		return fallbackTargets, fmt.Errorf(
			"discover service %s: %w",
			route.ServiceName,
			err,
		)
	}

	targets := nacosInstancesToTargets(instances)
	if len(targets) == 0 {
		return fallbackTargets, fmt.Errorf(
			"discover service %s: no usable instance address",
			route.ServiceName,
		)
	}

	return targets, nil
}

// refreshRouteTargets 执行一轮运行期服务发现。
// 某个服务查询失败时保留它最后一次可用的地址，同时继续刷新其他服务。
func refreshRouteTargets(
	registry healthyInstanceDiscovery,
	routes []proxyRoute,
	balancers map[string]*RoundRobinBalancer,
) []error {
	var refreshErrors []error

	for _, route := range routes {
		targets, err := resolveRouteTargets(registry, route)
		if err != nil {
			refreshErrors = append(refreshErrors, err)
			continue
		}

		balancer := balancers[route.Path]
		if balancer == nil {
			refreshErrors = append(refreshErrors, fmt.Errorf(
				"refresh service %s: balancer for route %s not found",
				route.ServiceName,
				route.Path,
			))
			continue
		}

		if err := balancer.ReplaceTargets(targets); err != nil {
			refreshErrors = append(refreshErrors, fmt.Errorf(
				"refresh service %s targets: %w",
				route.ServiceName,
				err,
			))
		}
	}

	return refreshErrors
}
