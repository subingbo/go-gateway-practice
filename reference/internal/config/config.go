// Package config 负责边缘网关的 YAML 配置加载、默认值与校验。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是边缘网关的完整配置。
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Auth      AuthConfig      `yaml:"auth"`
	CORS      CORSConfig      `yaml:"cors"`
	Mesh      MeshConfig      `yaml:"mesh"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	Routes    []RouteConfig   `yaml:"routes"`
}

// ServerConfig 是 HTTP 服务参数。
type ServerConfig struct {
	Addr              string        `yaml:"addr"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
}

// AuthConfig 是登录与令牌参数。
type AuthConfig struct {
	Issuer     string        `yaml:"issuer"`
	Audience   string        `yaml:"audience"`
	SecretFile string        `yaml:"secret_file"`
	AccessTTL  time.Duration `yaml:"access_ttl"`
	RefreshTTL time.Duration `yaml:"refresh_ttl"`
	Users      []UserConfig  `yaml:"users"`
}

// UserConfig 是一个演示用户。密码可以来自环境变量或直接写在配置里。
type UserConfig struct {
	Username    string   `yaml:"username"`
	UserID      string   `yaml:"user_id"`
	Roles       []string `yaml:"roles"`
	PasswordEnv string   `yaml:"password_env"`
	Password    string   `yaml:"password"`
}

// ResolvePassword 返回用户密码；环境变量优先于明文配置。
func (u UserConfig) ResolvePassword() (string, error) {
	if u.PasswordEnv != "" {
		value := os.Getenv(u.PasswordEnv)
		if value == "" {
			return "", fmt.Errorf("environment variable %s is not set", u.PasswordEnv)
		}
		return value, nil
	}

	if u.Password != "" {
		return u.Password, nil
	}

	return "", errors.New("password_env or password is required")
}

// CORSConfig 是跨域配置。
type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// MeshConfig 是边缘网关作为网格客户端所需的参数。
type MeshConfig struct {
	ControlPlaneURL string        `yaml:"control_plane_url"`
	Namespace       string        `yaml:"namespace"`
	Identity        string        `yaml:"identity"`
	CertTTL         time.Duration `yaml:"cert_ttl"`
}

// RateLimitConfig 是限流配置。
type RateLimitConfig struct {
	Default Limit            `yaml:"default"`
	Routes  map[string]Limit `yaml:"routes"`
}

// Limit 是令牌桶参数；RPS <= 0 表示不启用。
type Limit struct {
	RPS   float64 `json:"rps" yaml:"rps"`
	Burst int     `json:"burst" yaml:"burst"`
}

// Enabled 返回该限流配置是否启用。
func (l Limit) Enabled() bool {
	return l.RPS > 0 && l.Burst > 0
}

// RouteConfig 是一条边缘路由。
type RouteConfig struct {
	Name      string         `yaml:"name"`
	Match     MatchConfig    `yaml:"match"`
	Upstream  UpstreamConfig `yaml:"upstream"`
	Auth      AuthPolicy     `yaml:"auth"`
	RateLimit *Limit         `yaml:"rate_limit,omitempty"`
}

// MatchConfig 是路由匹配条件。
type MatchConfig struct {
	Host        string            `yaml:"host"`
	PathPrefix  string            `yaml:"path_prefix"`
	Methods     []string          `yaml:"methods"`
	Headers     map[string]string `yaml:"headers"`
	StripPrefix string            `yaml:"strip_prefix"`
}

// AuthPolicy 描述一条路由的认证要求。
type AuthPolicy struct {
	Required bool     `json:"required" yaml:"required"`
	Roles    []string `json:"roles,omitempty" yaml:"roles"`
}

// UpstreamConfig 描述上游是网格服务还是静态实例。
type UpstreamConfig struct {
	Service          string            `yaml:"service"`
	Subset           string            `yaml:"subset"`
	Instances        []InstanceConfig  `yaml:"instances"`
	Timeout          time.Duration     `yaml:"timeout"`
	Retries          RetryConfig       `yaml:"retries"`
	LoadBalancer     string            `yaml:"load_balancer"`
	HealthCheck      HealthCheckConfig `yaml:"health_check"`
	OutlierDetection OutlierConfig     `yaml:"outlier_detection"`
}

// IsMesh 返回上游是否使用网格服务发现。
func (u UpstreamConfig) IsMesh() bool {
	return u.Service != ""
}

// InstanceConfig 是一个静态上游实例。
type InstanceConfig struct {
	Address string            `yaml:"address"`
	Labels  map[string]string `yaml:"labels"`
}

// RetryConfig 是静态上游的重试配置。
type RetryConfig struct {
	Attempts      int           `yaml:"attempts"`
	PerTryTimeout time.Duration `yaml:"per_try_timeout"`
	On            []string      `yaml:"on"`
}

// HealthCheckConfig 是静态上游的主动健康检查。
type HealthCheckConfig struct {
	Path               string        `yaml:"path"`
	Interval           time.Duration `yaml:"interval"`
	Timeout            time.Duration `yaml:"timeout"`
	UnhealthyThreshold int           `yaml:"unhealthy_threshold"`
	HealthyThreshold   int           `yaml:"healthy_threshold"`
}

// OutlierConfig 是静态上游的离群摘除。
type OutlierConfig struct {
	Consecutive5xx int           `yaml:"consecutive_5xx"`
	EjectionTime   time.Duration `yaml:"ejection_time"`
}

// Load 读取并校验配置文件。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}

	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.ReadHeaderTimeout <= 0 {
		c.Server.ReadHeaderTimeout = 5 * time.Second
	}
	if c.Server.ShutdownTimeout <= 0 {
		c.Server.ShutdownTimeout = 10 * time.Second
	}

	if c.Auth.Issuer == "" {
		c.Auth.Issuer = "go-gateway-reference"
	}
	if c.Auth.Audience == "" {
		c.Auth.Audience = "go-gateway"
	}
	if c.Auth.SecretFile == "" {
		c.Auth.SecretFile = "data/jwt.secret"
	}
	if c.Auth.AccessTTL <= 0 {
		c.Auth.AccessTTL = 5 * time.Minute
	}
	if c.Auth.RefreshTTL <= 0 {
		c.Auth.RefreshTTL = 30 * time.Minute
	}

	if c.Mesh.Namespace == "" {
		c.Mesh.Namespace = "default"
	}
	if c.Mesh.Identity == "" {
		c.Mesh.Identity = "edge-gateway"
	}
	if c.Mesh.CertTTL <= 0 {
		c.Mesh.CertTTL = 10 * time.Minute
	}

	for i := range c.Routes {
		route := &c.Routes[i]

		if route.Upstream.Timeout <= 0 {
			route.Upstream.Timeout = 2 * time.Second
		}
		if route.Upstream.LoadBalancer == "" {
			route.Upstream.LoadBalancer = "round_robin"
		}
		if route.Upstream.HealthCheck.Path == "" {
			route.Upstream.HealthCheck.Path = "/healthz"
		}
		if route.Upstream.HealthCheck.Interval <= 0 {
			route.Upstream.HealthCheck.Interval = 3 * time.Second
		}
		if route.Upstream.HealthCheck.Timeout <= 0 {
			route.Upstream.HealthCheck.Timeout = time.Second
		}
		if route.Upstream.HealthCheck.UnhealthyThreshold <= 0 {
			route.Upstream.HealthCheck.UnhealthyThreshold = 3
		}
		if route.Upstream.HealthCheck.HealthyThreshold <= 0 {
			route.Upstream.HealthCheck.HealthyThreshold = 2
		}
		if route.Upstream.OutlierDetection.Consecutive5xx > 0 &&
			route.Upstream.OutlierDetection.EjectionTime <= 0 {
			route.Upstream.OutlierDetection.EjectionTime = 10 * time.Second
		}
		if route.Upstream.Retries.Attempts > 0 &&
			route.Upstream.Retries.PerTryTimeout <= 0 {
			route.Upstream.Retries.PerTryTimeout = 500 * time.Millisecond
		}
	}
}

// Validate 检查配置的完整性与合法性。
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Server.Addr) == "" {
		return errors.New("server.addr is required")
	}

	if len(c.Auth.Users) == 0 {
		return errors.New("auth.users must contain at least one user")
	}

	usernames := make(map[string]struct{}, len(c.Auth.Users))
	for _, user := range c.Auth.Users {
		if strings.TrimSpace(user.Username) == "" {
			return errors.New("auth.users[].username is required")
		}
		if user.UserID == "" {
			return fmt.Errorf("auth user %q: user_id is required", user.Username)
		}
		if _, exists := usernames[user.Username]; exists {
			return fmt.Errorf("duplicate auth user %q", user.Username)
		}
		usernames[user.Username] = struct{}{}

		if _, err := user.ResolvePassword(); err != nil {
			return fmt.Errorf("auth user %q: %w", user.Username, err)
		}
	}

	if c.Mesh.ControlPlaneURL != "" {
		parsed, err := url.Parse(c.Mesh.ControlPlaneURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("mesh.control_plane_url is invalid: %q", c.Mesh.ControlPlaneURL)
		}
	}

	if err := validateLimit("rate_limit.default", c.RateLimit.Default); err != nil {
		return err
	}
	for prefix, limit := range c.RateLimit.Routes {
		if !strings.HasPrefix(prefix, "/") {
			return fmt.Errorf("rate_limit.routes key %q must start with /", prefix)
		}
		if err := validateLimit("rate_limit.routes["+prefix+"]", limit); err != nil {
			return err
		}
	}

	if len(c.Routes) == 0 {
		return errors.New("routes must contain at least one route")
	}

	names := make(map[string]struct{}, len(c.Routes))
	usesMesh := false

	for _, route := range c.Routes {
		if strings.TrimSpace(route.Name) == "" {
			return errors.New("routes[].name is required")
		}
		if _, exists := names[route.Name]; exists {
			return fmt.Errorf("duplicate route name %q", route.Name)
		}
		names[route.Name] = struct{}{}

		if !strings.HasPrefix(route.Match.PathPrefix, "/") {
			return fmt.Errorf("route %q: match.path_prefix must start with /", route.Name)
		}

		if route.Match.StripPrefix != "" && !strings.HasPrefix(route.Match.StripPrefix, "/") {
			return fmt.Errorf("route %q: match.strip_prefix must start with /", route.Name)
		}

		upstream := route.Upstream
		if upstream.Service != "" && len(upstream.Instances) > 0 {
			return fmt.Errorf("route %q: service and instances cannot both be set", route.Name)
		}
		if upstream.Service == "" && len(upstream.Instances) == 0 {
			return fmt.Errorf("route %q: one of upstream.service or upstream.instances is required", route.Name)
		}
		if upstream.Service != "" {
			usesMesh = true
		}

		if upstream.Timeout <= 0 {
			return fmt.Errorf("route %q: upstream.timeout must be positive", route.Name)
		}

		switch strings.ToLower(upstream.LoadBalancer) {
		case "round_robin", "random", "least_request":
		default:
			return fmt.Errorf("route %q: unknown load_balancer %q", route.Name, upstream.LoadBalancer)
		}

		if upstream.Retries.Attempts < 0 {
			return fmt.Errorf("route %q: retries.attempts cannot be negative", route.Name)
		}
		if upstream.Retries.Attempts > 0 && upstream.Retries.PerTryTimeout <= 0 {
			return fmt.Errorf("route %q: retries.per_try_timeout must be positive", route.Name)
		}

		for _, instance := range upstream.Instances {
			if strings.TrimSpace(instance.Address) == "" {
				return fmt.Errorf("route %q: instance address is required", route.Name)
			}
		}

		if route.RateLimit != nil {
			if err := validateLimit("route "+route.Name+" rate_limit", *route.RateLimit); err != nil {
				return err
			}
		}
	}

	if usesMesh && c.Mesh.ControlPlaneURL == "" {
		return errors.New("mesh.control_plane_url is required when a route uses upstream.service")
	}

	return nil
}

func validateLimit(name string, limit Limit) error {
	if limit.RPS == 0 && limit.Burst == 0 {
		return nil
	}

	if limit.RPS <= 0 {
		return fmt.Errorf("%s: rps must be positive", name)
	}
	if limit.Burst <= 0 {
		return fmt.Errorf("%s: burst must be positive", name)
	}

	return nil
}
