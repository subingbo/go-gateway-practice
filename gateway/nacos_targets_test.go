package main

import (
	"reflect"
	"testing"

	"github.com/nacos-group/nacos-sdk-go/v2/model"
)

func TestNacosInstancesToTargetsKeepsOnlyUsableInstances(t *testing.T) {
	t.Parallel()

	instances := []model.Instance{
		{Ip: "127.0.0.1", Port: 9090, Healthy: true, Enable: true},
		{Ip: "2001:db8::1", Port: 9091, Healthy: true, Enable: true},
		{Ip: "127.0.0.2", Port: 9092, Healthy: false, Enable: true},
		{Ip: "127.0.0.3", Port: 9093, Healthy: true, Enable: false},
		{Ip: "not-an-ip", Port: 9094, Healthy: true, Enable: true},
		{Ip: "127.0.0.5", Port: 0, Healthy: true, Enable: true},
		{Ip: "127.0.0.6", Port: 70000, Healthy: true, Enable: true},
	}

	want := []string{
		"http://127.0.0.1:9090",
		"http://[2001:db8::1]:9091",
	}

	if got := nacosInstancesToTargets(instances); !reflect.DeepEqual(got, want) {
		t.Fatalf("nacosInstancesToTargets() = %v, want %v", got, want)
	}
}
