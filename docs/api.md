# 接口说明

[返回 README](../README.md)

所有示例假设网关运行在 `127.0.0.1:8080`。Windows PowerShell 中建议使用 `curl.exe`，避免 `curl` 别名带来的参数差异。

## 认证方式

除 `POST /auth/login` 和 `GET /health` 外，`/api/*` 都需要：

```http
Authorization: Bearer <access_token>
```

缺少 Token、格式不是 `Bearer `、签名无效或已过期，统一返回：

```http
HTTP/1.1 401 Unauthorized
Content-Type: text/plain; charset=utf-8

Unauthorized
```

## POST /auth/login

请求：

```json
{
  "username": "admin",
  "password": "admin123"
}
```

成功响应（`200 OK`，`Cache-Control: no-store`）：

```json
{
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "expires_in": 900
}
```

Token 有效期 15 分钟。常见失败：

| 状态 | 响应体 | 原因 |
|---|---|---|
| 400 | `Invalid request body` | JSON 格式错误或包含未知字段 |
| 400 | `Missing credentials` | 用户名或密码为空 |
| 401 | `Invalid username or password` | 凭据不正确 |

PowerShell 获取 Token：

```powershell
$body = '{"username":"admin","password":"admin123"}'
$token = (
  curl.exe -s -X POST http://127.0.0.1:8080/auth/login `
    -H "Content-Type: application/json" --data-raw $body |
  ConvertFrom-Json
).access_token
```

bash（需要 `jq`）：

```bash
TOKEN=$(curl -s -X POST http://127.0.0.1:8080/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | jq -r .access_token)
```

## GET /health

无需认证，用于存活检查。

```http
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8

Gateway OK
```

## GET /api/whoami

需要认证。返回认证中间件写入 Context 的用户 ID。

```text
Current User ID: dev-user-001
```

## GET /api/users/{id}

需要认证。转发到 `127.0.0.1:9090`，转发前剥离 `/api` 前缀。

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:8080/api/users/42
```

```text
Service: User
User ID: 42
Method: GET
Path: /users/42
```

## GET /api/orders/slow

需要认证。转发到 `127.0.0.1:9091`，上游会等待 5 秒；网关的 2 秒超时会先触发：

```http
HTTP/1.1 504 Gateway Timeout
Content-Type: text/plain; charset=utf-8

Gateway Timeout
```

同时网关会取消上游请求，订单服务日志出现：

```text
Slow order canceled: context canceled
```

这是观察超时中间件与 `ReverseProxy.ErrorHandler` 的主要方式。

## GET /api/routes

需要认证。返回网关当前注册的路由与上游配置。

```json
{
  "routes": [
    {
      "name": "用户服务",
      "method": "*",
      "path": "/api/users/",
      "upstream": "http://127.0.0.1:9090",
      "auth": true,
      "timeout_ms": 2000,
      "description": "反向代理，转发前剥离 /api 前缀"
    },
    {
      "name": "订单服务",
      "method": "*",
      "path": "/api/orders/",
      "upstream": "http://127.0.0.1:9091",
      "auth": true,
      "timeout_ms": 2000,
      "description": "反向代理，转发前剥离 /api 前缀"
    },
    {
      "name": "登录",
      "method": "POST",
      "path": "/auth/login",
      "auth": false,
      "description": "校验密码并签发 JWT"
    }
  ]
}
```

示例省略了 `whoami`、`/api/routes`、`/api/stats` 和 `/health`，实际响应包含全部路由。

## GET /api/stats

需要认证。返回网关进程内的请求指标。`/health`、`/api/routes`、`/api/stats` 和看板静态资源不计入统计。

```json
{
  "started_at": "2026-11-12T15:54:06.3103894+08:00",
  "uptime_seconds": 61.22,
  "in_flight": 0,
  "total_requests": 9,
  "status_classes": {
    "2xx": 4,
    "3xx": 0,
    "4xx": 4,
    "5xx": 1
  },
  "avg_latency_ms": 248.82,
  "p95_latency_ms": 2001.07,
  "routes": [
    {
      "path": "/api/orders/",
      "count": 2,
      "error_count": 2,
      "avg_ms": 1000.54
    }
  ],
  "recent": [
    {
      "time": "2026-11-12T15:55:05.2471192+08:00",
      "method": "GET",
      "path": "/api/orders/slow",
      "status": 504,
      "duration_ms": 2001.07
    }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `started_at` | 网关进程启动时间 |
| `uptime_seconds` | 已运行秒数 |
| `in_flight` | 正在处理的请求数 |
| `total_requests` | 计入统计的请求总数 |
| `status_classes` | 按 2xx / 3xx / 4xx / 5xx 分组 |
| `avg_latency_ms` | 最近 200 次请求的平均耗时 |
| `p95_latency_ms` | 最近 200 次请求的 P95 |
| `routes` | 按路由前缀聚合的请求数、错误数与平均耗时 |
| `recent` | 最近 50 条请求记录，按时间正序 |

## 状态码汇总

| 状态 | 场景 |
|---|---|
| 200 | 正常响应 |
| 400 | 登录请求体格式错误或缺少字段 |
| 401 | 未携带 Token、Token 无效或密码错误 |
| 502 | 上游不可达或连接中断 |
| 504 | 上游处理超过网关 2 秒超时 |
