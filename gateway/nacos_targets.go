package main

import (
	"net"
	"strconv"
	"strings"

	"github.com/nacos-group/nacos-sdk-go/v2/model"
)

// nacosInstancesToTargets 把 Nacos 的实例描述转换成负载均衡器使用的 URL。
// Nacos 保存的是 IP 和端口；RoundRobinBalancer 需要的是 http://IP:Port。
func nacosInstancesToTargets(instances []model.Instance) []string {
	targets := make([]string, 0, len(instances))

	for _, instance := range instances {
		// Healthy 表示 Nacos 认为实例健康，Enable 表示实例没有被手动禁用。
		// 两个条件必须同时满足，Gateway 才能把请求转发给它。
		if !instance.Healthy || !instance.Enable {
			continue
		}

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
