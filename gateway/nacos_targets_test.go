package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/nacos-group/nacos-sdk-go/v2/model"
)

type stubInstanceDiscovery struct {
	instancesByService map[string][]model.Instance
	errorsByService    map[string]error
	err                error
}

func (s stubInstanceDiscovery) GetHealthyInstances(serviceName string) ([]model.Instance, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := s.errorsByService[serviceName]; err != nil {
		return nil, err
	}
	return s.instancesByService[serviceName], nil
}

func TestNacosInstancesToTargetsConvertsValidAddresses(t *testing.T) {
	t.Parallel()

	instances := []model.Instance{
		{Ip: "127.0.0.1", Port: 9090},
		{Ip: "2001:db8::1", Port: 9091},
		{Ip: "not-an-ip", Port: 9094},
		{Ip: "127.0.0.5", Port: 0},
		{Ip: "127.0.0.6", Port: 70000},
	}

	want := []string{
		"http://127.0.0.1:9090",
		"http://[2001:db8::1]:9091",
	}

	if got := nacosInstancesToTargets(instances); !reflect.DeepEqual(got, want) {
		t.Fatalf("nacosInstancesToTargets() = %v, want %v", got, want)
	}
}

func TestResolveRouteTargetsUsesNacosInstances(t *testing.T) {
	t.Parallel()

	route := proxyRoute{
		ServiceName:     "user-service",
		FallbackTargets: []string{"http://127.0.0.1:9090"},
	}
	discovery := stubInstanceDiscovery{
		instancesByService: map[string][]model.Instance{
			"user-service": {
				{Ip: "10.0.0.8", Port: 19090},
			},
		},
	}

	got, err := resolveRouteTargets(discovery, route)
	if err != nil {
		t.Fatalf("resolveRouteTargets() error = %v", err)
	}

	want := []string{"http://10.0.0.8:19090"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveRouteTargets() = %v, want %v", got, want)
	}
}

func TestResolveRouteTargetsFallsBackWhenNacosFails(t *testing.T) {
	t.Parallel()

	route := proxyRoute{
		ServiceName:     "order-service",
		FallbackTargets: []string{"http://127.0.0.1:9091"},
	}
	discovery := stubInstanceDiscovery{err: errors.New("nacos unavailable")}

	got, err := resolveRouteTargets(discovery, route)
	if err == nil {
		t.Fatal("resolveRouteTargets() error = nil, want discovery error")
	}

	if !reflect.DeepEqual(got, route.FallbackTargets) {
		t.Fatalf("resolveRouteTargets() = %v, want fallback %v", got, route.FallbackTargets)
	}
}

func TestResolveRouteTargetsFallsBackWhenNacosHasNoUsableAddress(t *testing.T) {
	t.Parallel()

	route := proxyRoute{
		ServiceName:     "order-service",
		FallbackTargets: []string{"http://127.0.0.1:9091"},
	}
	discovery := stubInstanceDiscovery{
		instancesByService: map[string][]model.Instance{
			"order-service": {
				{Ip: "not-an-ip", Port: 9091},
			},
		},
	}

	got, err := resolveRouteTargets(discovery, route)
	if err == nil {
		t.Fatal("resolveRouteTargets() error = nil, want unusable-address error")
	}

	if !reflect.DeepEqual(got, route.FallbackTargets) {
		t.Fatalf("resolveRouteTargets() = %v, want fallback %v", got, route.FallbackTargets)
	}
}

func TestLoadNacosServerConfigUsesEnvironmentAddress(t *testing.T) {
	t.Parallel()

	getenv := func(name string) string {
		if name == "NACOS_ADDR" {
			return "nacos.internal:18848"
		}
		return ""
	}

	got, err := loadNacosServerConfig(getenv)
	if err != nil {
		t.Fatalf("loadNacosServerConfig() error = %v", err)
	}

	want := nacosServerConfig{
		address: "nacos.internal:18848",
		host:    "nacos.internal",
		port:    18848,
	}
	if got != want {
		t.Fatalf("loadNacosServerConfig() = %#v, want %#v", got, want)
	}
}

func TestLoadNacosServerConfigRejectsInvalidAddress(t *testing.T) {
	t.Parallel()

	_, err := loadNacosServerConfig(func(string) string {
		return "127.0.0.1:70000"
	})
	if err == nil {
		t.Fatal("loadNacosServerConfig() error = nil, want invalid-port error")
	}
}

// 一条服务发现失败不能阻塞其他服务刷新，也不能覆盖它最后一次可用的地址。
func TestRefreshRouteTargetsKeepsLastKnownTargetsOnPerServiceFailure(t *testing.T) {
	t.Parallel()

	userBalancer, err := NewRoundRobinBalancer(
		[]string{"http://127.0.0.1:9090"},
	)
	if err != nil {
		t.Fatal(err)
	}

	orderBalancer, err := NewRoundRobinBalancer(
		[]string{"http://127.0.0.1:9091"},
	)
	if err != nil {
		t.Fatal(err)
	}

	routes := []proxyRoute{
		{Path: "/api/users/", ServiceName: "user-service"},
		{Path: "/api/orders/", ServiceName: "order-service"},
	}
	balancers := map[string]*RoundRobinBalancer{
		"/api/users/":  userBalancer,
		"/api/orders/": orderBalancer,
	}
	registry := stubInstanceDiscovery{
		instancesByService: map[string][]model.Instance{
			"order-service": {
				{Ip: "10.0.0.9", Port: 19091},
			},
		},
		errorsByService: map[string]error{
			"user-service": errors.New("temporary nacos failure"),
		},
	}

	refreshErrors := refreshRouteTargets(registry, routes, balancers)
	if len(refreshErrors) != 1 {
		t.Fatalf("refreshRouteTargets() errors = %v, want one user-service error", refreshErrors)
	}

	wantUsers := []UpstreamStatus{
		{Target: "http://127.0.0.1:9090", Healthy: false},
	}
	if got := userBalancer.ListTargets(); !reflect.DeepEqual(got, wantUsers) {
		t.Fatalf("user targets = %#v, want last known %#v", got, wantUsers)
	}

	wantOrders := []UpstreamStatus{
		{Target: "http://10.0.0.9:19091", Healthy: false},
	}
	if got := orderBalancer.ListTargets(); !reflect.DeepEqual(got, wantOrders) {
		t.Fatalf("order targets = %#v, want refreshed %#v", got, wantOrders)
	}
}
