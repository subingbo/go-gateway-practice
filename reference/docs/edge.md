# 边缘网关

[返回 README](../README.md)

入口代码：`edge/main.go`、`edge/gateway.go`、`edge/auth.go`、`edge/ratelimit.go`、`edge/proxy.go`、`edge/stats.go`。

## 1. 配置

默认配置：`configs/gateway.yaml`。加载时会做严格字段校验（未知字段直接报错），并应用默认值。

```yaml
server:
  addr: ":8080"
  read_header_timeout: 5s
  shutdown_timeout: 10s

auth:
  issuer: go-gateway-reference
  audience: go-gateway
  secret_file: data/jwt.secret
  access_ttl: 5m
  refresh_ttl: 30m
  users:
    - username: admin
      user_id: admin-001
      roles: [admin]
      password_env: GATEWAY_ADMIN_PASSWORD

mesh:
  control_plane_url: http://127.0.0.1:18000
  namespace: default
  identity: edge-gateway
  cert_ttl: 10m

rate_limit:
  default: { rps: 50, burst: 100 }
  routes:
    "/api/orders/": { rps: 10, burst: 20 }

routes:
  - name: users
    match:
      path_prefix: /api/users/
      methods: [GET]
      strip_prefix: /api
    upstream:
      service: user-service
      timeout: 5s
    auth:
      required: true
      roles: [admin, viewer]
```

### 字段说明

| 字段 | 说明 |
|---|---|
| `server.addr` | 监听地址 |
| `auth.access_ttl` / `refresh_ttl` | access / refresh 有效期 |
| `auth.users[].password_env` | 从环境变量读取密码；也可以直接写 `password`（仅本地演示） |
| `auth.users[].roles` | 路由 `auth.roles` 的匹配依据 |
| `mesh.control_plane_url` | 控制面地址；有 `upstream.service` 时必填 |
| `mesh.identity` | Edge 自己的网格身份（SPIFFE service 名） |
| `rate_limit.default` | 默认令牌桶 |
| `rate_limit.routes` | 按路由前缀覆盖；最长前缀优先 |
| `routes[].match` | host / path_prefix / methods / headers |
| `routes[].match.strip_prefix` | 转发前剥离的前缀 |
| `routes[].upstream.service` | 走网格服务发现 |
| `routes[].upstream.instances` | 静态上游地址列表（与 service 二选一） |
| `routes[].upstream.timeout` | 外层总超时 |
| `routes[].upstream.retries` | 静态上游重试；网格上游使用 VirtualService 的重试 |
| `routes[].auth.roles` | 任一角色匹配即可；空表示只要求登录 |

## 2. 请求处理顺序

`edge/gateway.go` 的 `ServeHTTP` 顺序：

```text
request ID
  → CORS / OPTIONS 预检
  → 公开路由（/health、/ready、/metrics、/auth/*、/、/dashboard）
  → 本地 API（/api/whoami、/api/routes、/api/stats、/api/logs/stream、/api/mesh/*）
  → 路由匹配（最长 path_prefix）
  → JWT 认证 + RBAC
  → 令牌桶限流
  → 反向代理
  → 访问日志 + 指标
```

`/health`、`/ready`、`/metrics`、看板、`/api/stats`、`/api/routes`、`/api/logs/stream` 不计入请求统计，避免看板轮询污染指标。

## 3. 认证

### 登录

```http
POST /auth/login
Content-Type: application/json

{"username":"admin","password":"..."}
```

响应包含：

- `access_token`：HS256 JWT，默认 5 分钟；
- `refresh_token`：32 字节随机串，默认 30 分钟；
- `user_id`、`roles`、`expires_in`、`access_expires_at` 等。

### 刷新与轮换

```http
POST /auth/refresh

{"refresh_token":"..."}
```

刷新成功后旧 refresh 立即失效（rotation），返回一对新令牌。重复使用旧 refresh 返回 401。

### 登出与吊销

```http
POST /auth/logout
Authorization: Bearer <access_token>

{"refresh_token":"..."}
```

- 删除 refresh；
- 把 access 的 `jti` 放入内存 denylist，直到它自然过期；
- 返回 204。

### RBAC

路由的 `auth.roles` 是“任一匹配”语义。viewer 访问 `roles: [admin]` 的路由会得到 403。

## 4. 限流

- 令牌桶实现：`edge/ratelimit.go`；
- key = `route + "|" + user:<id>` 或 `route + "|" + ip:<client>`；
- 路由显式 `rate_limit` 优先，其次按 `rate_limit.routes` 最长前缀匹配，最后用 default；
- 超限返回 429 + `Retry-After: 1`。

默认配置对 `/api/orders/` 限制 10 rps / burst 20，可以用 loadgen 观察 429。

## 5. 上游

### 网格上游

```yaml
upstream:
  service: user-service
  timeout: 5s
```

- 实例来自控制面快照；
- 路由规则、子集权重、故障注入、重试、超时、LB、离群摘除来自 VirtualService / DestinationRule；
- Edge 以 `mesh.identity` 的身份 mTLS 调用目标 sidecar。

### 静态上游

```yaml
upstream:
  service: ""
  instances:
    - address: 127.0.0.1:9201
      labels: { zone: local }
    - address: 127.0.0.1:9202
  timeout: 2s
  load_balancer: round_robin
  retries:
    attempts: 2
    per_try_timeout: 300ms
    on: [5xx, reset, connect]
  health_check:
    path: /healthz
    interval: 3s
    timeout: 1s
    unhealthy_threshold: 3
    healthy_threshold: 2
  outlier_detection:
    consecutive_5xx: 5
    ejection_time: 10s
```

静态上游由 Edge 自己做主动健康检查和离群摘除，不依赖网格。

## 6. 管理接口

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| GET | `/health` | 否 | 存活检查 |
| GET | `/ready` | 否 | 配置、路由、网格证书是否就绪 |
| GET | `/metrics` | 否 | Prometheus 文本 |
| POST | `/auth/login` | 否 | 登录 |
| POST | `/auth/refresh` | 否 | 刷新 |
| POST | `/auth/logout` | 可选 Bearer | 登出 |
| GET | `/api/whoami` | 是 | 当前用户、角色、jti、到期时间 |
| GET | `/api/routes` | 是 | 路由、上游实例、子集、健康状态 |
| GET | `/api/stats` | 是 | 请求统计、RPS、P95、最近请求、上游健康 |
| GET | `/api/logs/stream` | `?access_token=` | SSE 日志 |
| GET | `/api/mesh/status` | 是 | 代理控制面 `/v1/status` |
| GET | `/api/mesh/config` | 是 | 代理控制面 `/v1/config` |
| GET | `/`、`/dashboard` | 否 | 内置看板 |

`/api/logs/stream` 用 query token，是因为浏览器 `EventSource` 不能自定义请求头；仅用于本地演示。

## 7. 指标

| 指标 | 类型 | 标签 |
|---|---|---|
| `gateway_http_requests_total` | counter | route, method, code |
| `gateway_http_request_duration_seconds` | histogram | route |
| `gateway_http_in_flight` | gauge | — |
| `gateway_upstream_healthy` | gauge | cluster, instance |
| `gateway_upstream_retries_total` | counter | cluster |
| `gateway_ratelimit_rejected_total` | counter | route |
| `gateway_auth_failures_total` | counter | reason |
| `gateway_config_reloads_total` | counter | — |
| `gateway_mesh_config_version` | gauge | — |
| `mesh_*` | counter/gauge | 见 [mesh.md](mesh.md) |

网格路由的 `gateway_upstream_healthy` 会按子集展示，例如 `cluster="users/v2"`。

## 8. 热重载

`config.NewWatcher` 每 2 秒检查配置文件 mtime + size：

- 解析和校验成功：构建新路由表，替换旧表，停止旧静态上游的健康检查；
- 解析失败：保留旧配置并记录错误，不会让网关中断。

以下配置变更需要重启：

- `mesh.control_plane_url` / `mesh.identity`；
- JWT 的 issuer/audience/TTL（用户列表可以热更新）。

## 9. 边界

- 只支持 HTTP 上游，没有 WebSocket / HTTP/2；
- 重试只对可重放请求生效（无 body 或 body 已缓冲且不超过 8 MiB）；
- 限流是单进程内存实现，不是分布式限流；
- 指标和令牌都在内存里，重启清零；
- CORS 只允许配置中明确列出的 origin，不支持 `*`。
