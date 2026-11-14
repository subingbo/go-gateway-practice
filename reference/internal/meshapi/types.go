// Package meshapi 定义控制面与数据面之间共享的数据结构。
//
// 控制面把服务实例、VirtualService、DestinationRule 和根证书打包成一个
// Snapshot，通过 SSE 推送给 edge 与 sidecar。这里的字段名同时用于 YAML
// 配置和 JSON 传输，所以每个字段都带 yaml 与 json 标签。
package meshapi

import "time"

// InstanceStatus 表示一个服务实例的健康状态。
//
// UNKNOWN 表示刚注册、还没有完成第一次健康检查；数据面会把 UNKNOWN
// 当作可用，避免冷启动期间出现 503。UNHEALTHY 的实例不会进入负载均衡。
type InstanceStatus string

const (
	StatusUnknown   InstanceStatus = "UNKNOWN"
	StatusHealthy   InstanceStatus = "HEALTHY"
	StatusUnhealthy InstanceStatus = "UNHEALTHY"
)

// Healthy 返回实例是否应该参与负载均衡。
func (s InstanceStatus) Healthy() bool {
	return s != StatusUnhealthy
}

// Instance 是一个注册到控制面的服务实例。
//
// Address 指向 sidecar 的 inbound 监听地址，而不是应用自己的地址。
// 这样所有东西向流量都会经过 sidecar，mTLS、重试和指标才有落点。
type Instance struct {
	ID           string            `json:"id" yaml:"id"`
	Service      string            `json:"service" yaml:"service"`
	Namespace    string            `json:"namespace" yaml:"namespace"`
	Address      string            `json:"address" yaml:"address"`
	Labels       map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	HealthURL    string            `json:"health_url,omitempty" yaml:"health_url,omitempty"`
	Status       InstanceStatus    `json:"status" yaml:"status"`
	LastSeen     time.Time         `json:"last_seen" yaml:"last_seen"`
	RegisteredAt time.Time         `json:"registered_at" yaml:"registered_at"`

	// ConfigVersion 与 CertExpiry 由 sidecar 心跳上报，便于控制面面板展示。
	ConfigVersion uint64    `json:"config_version,omitempty" yaml:"config_version,omitempty"`
	CertExpiry    time.Time `json:"cert_expiry,omitempty" yaml:"cert_expiry,omitempty"`
}

// ServiceSnapshot 是一个服务及其全部实例的只读视图。
type ServiceSnapshot struct {
	Name      string     `json:"name"`
	Namespace string     `json:"namespace"`
	Instances []Instance `json:"instances"`
}

// Snapshot 是控制面下发给数据面的完整配置快照。
//
// Version 单调递增。客户端断线重连时带上自己持有的版本号，控制面只在
// 版本落后时立即推送，避免无意义的重复传输。
type Snapshot struct {
	Version          uint64            `json:"version"`
	GeneratedAt      time.Time         `json:"generated_at"`
	Services         []ServiceSnapshot `json:"services"`
	VirtualServices  []VirtualService  `json:"virtual_services"`
	DestinationRules []DestinationRule `json:"destination_rules"`
	TrustBundlePEM   string            `json:"trust_bundle_pem"`
}

// Metadata 是所有网格配置共用的元数据。
type Metadata struct {
	Name      string `json:"name" yaml:"name"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
}

// VirtualService 描述“请求应该怎么走”，对应 Istio 的同名资源。
type VirtualService struct {
	APIVersion string             `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string             `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata           `json:"metadata" yaml:"metadata"`
	Spec       VirtualServiceSpec `json:"spec" yaml:"spec"`
}

// VirtualServiceSpec 是 VirtualService 的规则主体。
type VirtualServiceSpec struct {
	Hosts []string    `json:"hosts" yaml:"hosts"`
	HTTP  []HTTPRoute `json:"http" yaml:"http"`
}

// HTTPRoute 是一条按顺序匹配的 HTTP 规则。
type HTTPRoute struct {
	Name    string              `json:"name,omitempty" yaml:"name,omitempty"`
	Match   []HTTPMatch         `json:"match,omitempty" yaml:"match,omitempty"`
	Route   []DestinationWeight `json:"route" yaml:"route"`
	Timeout time.Duration       `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Retries *RetryPolicy        `json:"retries,omitempty" yaml:"retries,omitempty"`
	Fault   *Fault              `json:"fault,omitempty" yaml:"fault,omitempty"`
}

// HTTPMatch 是一组 AND 条件；同一规则里的多个 HTTPMatch 之间是 OR。
type HTTPMatch struct {
	PathPrefix string            `json:"path_prefix,omitempty" yaml:"path_prefix,omitempty"`
	Method     string            `json:"method,omitempty" yaml:"method,omitempty"`
	Headers    map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// DestinationWeight 表示一条带权重的目标路由。
type DestinationWeight struct {
	Destination Destination `json:"destination" yaml:"destination"`
	Weight      int         `json:"weight,omitempty" yaml:"weight,omitempty"`
}

// Destination 指向某个服务的子集。
type Destination struct {
	Host   string `json:"host,omitempty" yaml:"host,omitempty"`
	Subset string `json:"subset,omitempty" yaml:"subset,omitempty"`
	Port   int    `json:"port,omitempty" yaml:"port,omitempty"`
}

// RetryPolicy 对应 Istio 的 retries 字段。
//
// Attempts 表示首次请求之外的重试次数，因此总尝试次数是 Attempts+1。
type RetryPolicy struct {
	Attempts      int           `json:"attempts" yaml:"attempts"`
	PerTryTimeout time.Duration `json:"per_try_timeout,omitempty" yaml:"per_try_timeout,omitempty"`
	RetryOn       []string      `json:"retry_on,omitempty" yaml:"retry_on,omitempty"`
}

// Fault 用于故障注入，便于演示重试与熔断。
type Fault struct {
	Delay *DelayFault `json:"delay,omitempty" yaml:"delay,omitempty"`
	Abort *AbortFault `json:"abort,omitempty" yaml:"abort,omitempty"`
}

// DelayFault 按百分比给请求增加固定延迟。
type DelayFault struct {
	Percentage int           `json:"percentage" yaml:"percentage"`
	FixedDelay time.Duration `json:"fixed_delay" yaml:"fixed_delay"`
}

// AbortFault 按百分比直接返回指定状态码。
type AbortFault struct {
	Percentage int `json:"percentage" yaml:"percentage"`
	HTTPStatus int `json:"http_status" yaml:"http_status"`
}

// DestinationRule 描述“目标服务有哪些子集、怎么连”，对应 Istio 的同名资源。
type DestinationRule struct {
	APIVersion string              `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string              `json:"kind,omitempty" yaml:"kind,omitempty"`
	Metadata   Metadata            `json:"metadata" yaml:"metadata"`
	Spec       DestinationRuleSpec `json:"spec" yaml:"spec"`
}

// DestinationRuleSpec 是 DestinationRule 的规则主体。
type DestinationRuleSpec struct {
	Host          string        `json:"host" yaml:"host"`
	Subsets       []Subset      `json:"subsets,omitempty" yaml:"subsets,omitempty"`
	TrafficPolicy TrafficPolicy `json:"traffic_policy,omitempty" yaml:"traffic_policy,omitempty"`
}

// Subset 通过标签选择一组实例，例如 version=v1。
type Subset struct {
	Name   string            `json:"name" yaml:"name"`
	Labels map[string]string `json:"labels" yaml:"labels"`
}

// TrafficPolicy 是连接与负载均衡策略。
type TrafficPolicy struct {
	LoadBalancer       string           `json:"load_balancer,omitempty" yaml:"load_balancer,omitempty"`
	ConnectionPool     ConnectionPool   `json:"connection_pool,omitempty" yaml:"connection_pool,omitempty"`
	OutlierDetection   OutlierDetection `json:"outlier_detection,omitempty" yaml:"outlier_detection,omitempty"`
	PeerAuthentication string           `json:"peer_authentication,omitempty" yaml:"peer_authentication,omitempty"`
}

// ConnectionPool 限制到某个目标服务的连接数与排队请求数。
type ConnectionPool struct {
	MaxConnections     int `json:"max_connections,omitempty" yaml:"max_connections,omitempty"`
	MaxPendingRequests int `json:"max_pending_requests,omitempty" yaml:"max_pending_requests,omitempty"`
}

// OutlierDetection 是离群摘除（被动熔断）配置。
type OutlierDetection struct {
	Consecutive5xx int           `json:"consecutive_5xx,omitempty" yaml:"consecutive_5xx,omitempty"`
	EjectionTime   time.Duration `json:"ejection_time,omitempty" yaml:"ejection_time,omitempty"`
}

// SidecarStatus 是 sidecar 心跳上报的运行状态，仅用于控制面展示。
type SidecarStatus struct {
	Identity      string    `json:"identity"`
	Service       string    `json:"service"`
	Namespace     string    `json:"namespace"`
	Inbound       string    `json:"inbound"`
	ConfigVersion uint64    `json:"config_version"`
	CertExpiry    time.Time `json:"cert_expiry"`
	LastSeen      time.Time `json:"last_seen"`
}

// CAInfo 是控制面 CA 的公开信息。
type CAInfo struct {
	Subject   string       `json:"subject"`
	NotBefore time.Time    `json:"not_before"`
	NotAfter  time.Time    `json:"not_after"`
	Issued    []CertRecord `json:"issued"`
}

// CertRecord 是一条证书签发记录。
type CertRecord struct {
	Serial   string    `json:"serial"`
	SpiffeID string    `json:"spiffe_id"`
	IssuedAt time.Time `json:"issued_at"`
	NotAfter time.Time `json:"not_after"`
}

// FindVirtualService 返回 hosts 中包含 host 的第一条 VirtualService。
func (s *Snapshot) FindVirtualService(host string) *VirtualService {
	if s == nil {
		return nil
	}

	for i := range s.VirtualServices {
		for _, h := range s.VirtualServices[i].Spec.Hosts {
			if h == host {
				return &s.VirtualServices[i]
			}
		}
	}

	return nil
}

// FindDestinationRule 返回 host 对应的 DestinationRule。
func (s *Snapshot) FindDestinationRule(host string) *DestinationRule {
	if s == nil {
		return nil
	}

	for i := range s.DestinationRules {
		if s.DestinationRules[i].Spec.Host == host {
			return &s.DestinationRules[i]
		}
	}

	return nil
}

// SubsetLabels 返回某个子集的标签选择器；子集不存在时返回 nil。
func (d *DestinationRule) SubsetLabels(name string) map[string]string {
	if d == nil || name == "" {
		return nil
	}

	for _, subset := range d.Spec.Subsets {
		if subset.Name == name {
			return subset.Labels
		}
	}

	return nil
}

// PeerAuthentication 返回目标服务的 mTLS 模式，默认 PERMISSIVE。
func (s *Snapshot) PeerAuthentication(host string) string {
	dr := s.FindDestinationRule(host)
	if dr == nil {
		return "PERMISSIVE"
	}

	switch dr.Spec.TrafficPolicy.PeerAuthentication {
	case "STRICT", "PERMISSIVE":
		return dr.Spec.TrafficPolicy.PeerAuthentication
	default:
		return "PERMISSIVE"
	}
}

// ServiceInstances 返回某个服务的全部实例。
func (s *Snapshot) ServiceInstances(service string) []Instance {
	if s == nil {
		return nil
	}

	for _, svc := range s.Services {
		if svc.Name == service {
			return svc.Instances
		}
	}

	return nil
}
