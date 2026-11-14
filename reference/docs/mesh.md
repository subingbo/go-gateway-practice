# 集群内网关层（类 Istio）

[返回 README](../README.md)

这一层由三部分组成：

1. **控制面**（`mesh/controlplane`）：服务注册、健康检查、配置分发、CA、状态面板。
2. **Sidecar**（`mesh/sidecar`）：每个服务实例旁的入站/出站代理。
3. **共享数据面**（`mesh/dataplane`）：Edge 和 sidecar 共用的路由、LB、重试、故障注入、mTLS 发送。

## 1. 控制面

### 注册与心跳

sidecar 启动时注册：

```http
POST /v1/instances/register
{
  "id": "user-service-v1-19101",
  "service": "user-service",
  "namespace": "default",
  "address": "127.0.0.1:19101",
  "labels": {"version": "v1"},
  "health_url": "http://127.0.0.1:9101/healthz"
}
```

- `address` 是 sidecar 的 **inbound** 地址，不是应用地址；
- 控制面每 2 秒主动探测 `health_url`；
- 连续 3 次失败标记 `UNHEALTHY`，连续 2 次成功恢复；
- sidecar 每 10 秒发送心跳；45 秒没有心跳的实例会被移除；
- sidecar 正常退出时调用 `DELETE /v1/instances/{service}/{id}` 主动摘除。

实例状态：

| 状态 | 数据面行为 |
|---|---|
| `UNKNOWN` | 视为可用（避免冷启动 503） |
| `HEALTHY` | 参与负载均衡 |
| `UNHEALTHY` | 从快照的可用实例中过滤掉 |

### 配置模型

控制面加载 `configs/mesh/*.yaml`，支持多文档 YAML。

#### VirtualService

```yaml
apiVersion: mesh/v1
kind: VirtualService
metadata:
  name: user-service
spec:
  hosts: [user-service]
  http:
    - name: canary-header
      match:
        - headers: { x-canary: "true" }
      route:
        - destination: { subset: v2 }
          weight: 100

    - name: faulty
      match:
        - path_prefix: /users/faulty
      fault:
        abort: { percentage: 100, http_status: 500 }
      retries:
        attempts: 2
        per_try_timeout: 300ms
        retry_on: [5xx]
      route:
        - destination: { subset: v1 }
          weight: 100

    - name: default
      route:
        - destination: { subset: v1 }
          weight: 90
        - destination: { subset: v2 }
          weight: 10
      timeout: 2s
      retries:
        attempts: 2
        per_try_timeout: 500ms
        retry_on: [5xx, reset, connect]
```

规则按顺序匹配，第一条命中即返回。`match` 支持：

- `path_prefix`；
- `method`；
- `headers`（精确匹配）。

`fault` 支持：

- `abort`：按百分比直接返回状态码；
- `delay`：按百分比增加固定延迟。

`retries.attempts` 表示首次请求之外的重试次数，总尝试次数 = attempts + 1。

#### DestinationRule

```yaml
apiVersion: mesh/v1
kind: DestinationRule
metadata:
  name: user-service
spec:
  host: user-service
  subsets:
    - name: v1
      labels: { version: v1 }
    - name: v2
      labels: { version: v2 }
  traffic_policy:
    load_balancer: least_request
    connection_pool:
      max_connections: 100
      max_pending_requests: 50
    outlier_detection:
      consecutive_5xx: 5
      ejection_time: 10s
    peer_authentication: PERMISSIVE
```

- `subsets` 通过实例标签选择实例；
- `load_balancer`：`round_robin` / `random` / `least_request`；
- `connection_pool`：HTTP 连接上限与排队请求上限；
- `outlier_detection`：连续 5xx 达到阈值后摘除实例；
- `peer_authentication`：`PERMISSIVE` / `STRICT`。

### 快照与 SSE

`GET /v1/config` 返回：

```json
{
  "version": 7,
  "generated_at": "...",
  "services": [{"name": "user-service", "instances": [...]}],
  "virtual_services": [...],
  "destination_rules": [...],
  "trust_bundle_pem": "-----BEGIN CERTIFICATE-----..."
}
```

`GET /v1/watch?version=N` 是 SSE：

- 如果当前版本大于 N，立即推送；
- 否则等待注册表或配置变化；
- 15 秒心跳注释；
- 客户端断线后先 `GET /v1/config` 补全，再带新版本重连。

### CA 与证书

- 根 CA：ECDSA P-256，自签，10 年，持久化到 `data/ca/root.pem` 和 `root-key.pem`；
- 叶子证书：默认 10 分钟，最长 1 小时，最短 1 分钟；
- 叶子证书的 `URI SAN`：`spiffe://cluster.local/ns/<ns>/sa/<service>`；
- 叶子证书的 `DNS SAN`：服务名（用于标准 TLS 主机名校验）；
- 签发接口：

```http
POST /v1/certs/issue
{"spiffe_id": "spiffe://cluster.local/ns/default/sa/user-service", "ttl_seconds": 600}
```

响应包含 `certificate_pem`、`private_key_pem`、`root_ca_pem`、`serial`、`expires_at`。

> 演示环境通过 localhost 明文接口分发私钥；生产环境应该使用 SDS/密钥管理系统。

### 控制面接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/health` | 存活 |
| GET | `/v1/registry` | 服务实例 |
| GET | `/v1/config` | 完整快照 |
| GET | `/v1/watch` | SSE 配置推送 |
| POST | `/v1/instances/register` | 注册 |
| POST | `/v1/instances/heartbeat` | 心跳 |
| DELETE | `/v1/instances/{service}/{id}` | 摘除 |
| POST | `/v1/certs/issue` | 签发证书 |
| GET | `/v1/ca` | 根 CA 与签发记录 |
| POST | `/v1/sidecars/heartbeat` | sidecar 舰队心跳 |
| GET | `/v1/sidecars` | sidecar 列表 |
| GET | `/v1/status` | 面板聚合数据 |
| GET | `/metrics` | Prometheus |
| GET | `/` | 控制面面板 |

## 2. Sidecar

### 入站

- TLS 监听（`tls.RequestClientCert`）；
- 根据目标服务的 DestinationRule 决定 `STRICT` / `PERMISSIVE`；
- 校验证书链和 URI SAN；
- 覆盖外部传入的 `X-Mesh-Source` 和 `X-Forwarded-Client-Cert`；
- 写入 `X-Mesh-Source: spiffe://...` 和 `X-Forwarded-Client-Cert: URI=...`；
- 用 `httputil.ReverseProxy` 转发到本机应用，保留原始 Host；
- 传播 `X-Request-ID`。

### 出站

- 监听一个 HTTP forward proxy 端口；
- SDK 把 `http.Transport.Proxy` 指向它；
- 从绝对 URL 或 Host 取目标服务；
- 匹配 VirtualService；
- 故障注入（abort / delay）；
- 按权重选择子集；
- 从快照过滤 `HEALTHY` 实例；
- 按 DestinationRule 做 LB、连接池、离群摘除；
- mTLS 发往目标 sidecar inbound；
- 重试时重新选实例；
- 记录出站指标和访问日志。

### 注册与证书

- 启动时注册实例（address = inbound 地址）；
- 每 10 秒心跳，同时上报配置版本和证书到期时间；
- 证书在剩余 1/3 TTL 时自动续期；
- 正常退出时摘除实例。

### 管理接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 存活 |
| GET | `/ready` | 快照、证书、注册是否就绪 |
| GET | `/metrics` | Prometheus |
| GET | `/debug/config` | 当前快照 |
| GET | `/debug/endpoints?host=&subset=` | 当前集群实例与熔断状态 |

## 3. 数据面策略

### 金丝雀

- 默认 90/10；
- 带 `x-canary: true` 的请求固定走 v2；
- 权重选择在 `mesh/dataplane/router.go` 的 `pickDestination`。

### 故障注入与重试

`/users/faulty` 规则 100% abort 500，并配置 2 次重试：

- 每次尝试都会重新触发故障；
- 最终返回 500；
- `mesh_retries_total` 增加。

### 离群摘除

- 连续 5xx 达到 `consecutive_5xx` 后，实例被摘除 `ejection_time`；
- 摘除期间不会被 LB 选中；
- 全部实例被摘除时返回 503；
- 摘除状态在快照更新时按地址保留；
- 指标：`mesh_outlier_ejections_total{destination,endpoint,source}`。

### 连接池

- `max_connections` 映射到 `http.Transport.MaxConnsPerHost`；
- `max_pending_requests` 用带缓冲 channel 做排队上限；
- 超过排队上限返回 503。

## 4. 指标

| 指标 | 类型 | 标签 |
|---|---|---|
| `mesh_requests_total` | counter | direction, source, destination, code |
| `mesh_request_duration_seconds` | histogram | direction, source, destination |
| `mesh_retries_total` | counter | source, destination |
| `mesh_outlier_ejections_total` | counter | source, destination, endpoint |
| `mesh_config_version` | gauge | — |
| `mesh_cert_expiry_seconds` | gauge | — |

控制面还有：

| 指标 | 说明 |
|---|---|
| `mesh_registry_instances{service,status}` | 各服务实例数 |
| `mesh_watch_clients` | 当前 SSE 客户端数 |
| `mesh_config_version` | 当前快照版本 |
| `mesh_ca_certs_issued_total` | 累计签发证书数 |

## 5. 和 Istio 的对应关系

| 本实现 | Istio |
|---|---|
| Control Plane | Istiod |
| Sidecar | Envoy |
| VirtualService | VirtualService |
| DestinationRule | DestinationRule |
| PeerAuthentication（放在 DR 的 traffic_policy 里） | PeerAuthentication |
| SSE 全量快照 | xDS / ADS |
| 显式 HTTP proxy | iptables 透明拦截 |
| 进程内注册表 | Kubernetes 服务发现 |

## 6. 边界

- 没有 Kubernetes、Docker、Envoy；
- 没有 gRPC xDS，只有 SSE 全量快照；
- 入站始终 TLS，PERMISSIVE 不实现明文/mTLS 同端口嗅探；
- 没有授权策略（AuthorizationPolicy）、请求头改写、重定向、镜像流量；
- 没有分布式追踪，只有请求 ID 和访问日志；
- 控制面 API 没有鉴权，仅用于 localhost 演示。
