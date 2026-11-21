package discovery

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

const (
	defaultGroup   = "DEFAULT_GROUP"
	defaultCluster = "DEFAULT"
)

// Client 封装 Nacos 服务发现客户端
type Client struct {
	naming naming_client.INamingClient
}

// NewClient 创建 Nacos 客户端
func NewClient(
	serverIP string,
	serverPort uint64,
) (*Client, error) {

	if serverIP == "" {
		serverIP = "127.0.0.1"
	}

	if serverPort == 0 {
		serverPort = 8848
	}

	logDir := filepath.Join("data", "nacos", "log")
	cacheDir := filepath.Join("data", "nacos", "cache")

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("create nacos log dir: %w", err)
	}

	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("create nacos cache dir: %w", err)
	}

	// 客户端配置
	clientConfig := *constant.NewClientConfig(
		constant.WithNamespaceId(""),
		constant.WithTimeoutMs(5000),
		constant.WithNotLoadCacheAtStart(true),
		constant.WithUpdateCacheWhenEmpty(true),
		constant.WithLogDir(logDir),
		constant.WithCacheDir(cacheDir),
		constant.WithLogLevel("info"),
	)

	// Nacos 服务端配置
	serverConfigs := []constant.ServerConfig{
		*constant.NewServerConfig(
			serverIP,
			serverPort,
			constant.WithScheme("http"),
			constant.WithContextPath("/nacos"),
		),
	}

	// 创建 Naming Client
	namingClient, err := clients.NewNamingClient(
		vo.NacosClientParam{
			ClientConfig:  &clientConfig,
			ServerConfigs: serverConfigs,
		},
	)

	if err != nil {
		return nil, fmt.Errorf("create nacos naming client: %w", err)
	}

	return &Client{
		naming: namingClient,
	}, nil
}

// Register 注册服务实例
func (c *Client) Register(
	serviceName string,
	ip string,
	port uint64,
) error {

	if c == nil || c.naming == nil {
		return fmt.Errorf("nacos client is nil")
	}

	if serviceName == "" || ip == "" || port == 0 {
		return fmt.Errorf("invalid service registration parameters")
	}

	success, err := c.naming.RegisterInstance(
		vo.RegisterInstanceParam{
			Ip:          ip,
			Port:        port,
			Weight:      1.0,
			Enable:      true,
			Healthy:     true,
			ServiceName: serviceName,
			GroupName:   defaultGroup,
			ClusterName: defaultCluster,
			Ephemeral:   true,
		},
	)

	if err != nil {
		return fmt.Errorf("register service %s: %w", serviceName, err)
	}

	if !success {
		return fmt.Errorf("nacos rejected registration: %s", serviceName)
	}

	return nil
}

// Deregister 注销服务实例
func (c *Client) Deregister(
	serviceName string,
	ip string,
	port uint64,
) error {

	if c == nil || c.naming == nil {
		return fmt.Errorf("nacos client is nil")
	}

	// DeregisterInstance 是 Nacos SDK 提供的真正注销操作。
	// SDK 会把下面的服务名、IP、端口等信息发送给 Nacos Naming 服务，
	// 由 Nacos 删除与这些字段匹配的服务实例。
	success, err := c.naming.DeregisterInstance(
		vo.DeregisterInstanceParam{
			Ip:          ip,
			Port:        port,
			ServiceName: serviceName,
			GroupName:   defaultGroup,
			Cluster:     defaultCluster,
			Ephemeral:   true,
		},
	)

	if err != nil {
		return fmt.Errorf("deregister service %s: %w", serviceName, err)
	}

	if !success {
		return fmt.Errorf("nacos rejected deregistration: %s", serviceName)
	}

	return nil
}

// GetHealthyInstances 获取健康的服务实例
func (c *Client) GetHealthyInstances(
	serviceName string,
) ([]model.Instance, error) {

	if c == nil || c.naming == nil {
		return nil, fmt.Errorf("nacos client is nil")
	}

	instances, err := c.naming.SelectInstances(
		vo.SelectInstancesParam{
			ServiceName: serviceName,
			GroupName:   defaultGroup,
			Clusters:    []string{defaultCluster},
			HealthyOnly: true,
		},
	)

	if err != nil {
		return nil, fmt.Errorf("discover service %s: %w", serviceName, err)
	}

	return instances, nil
}

// Close 关闭 Nacos 客户端
func (c *Client) Close() {
	if c != nil && c.naming != nil {
		c.naming.CloseClient()
	}
}
