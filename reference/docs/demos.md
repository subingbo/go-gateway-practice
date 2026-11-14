# 演示剧本

[返回 README](../README.md)

先启动 devstack：

```bash
cd reference
go run ./cmd/devstack
```

记下终端打印的 `admin` / `viewer` 密码。下面用 `$GW` 表示边缘网关：

```bash
export GW=http://127.0.0.1:8080
LOGIN=$(curl -s -X POST "$GW/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<粘贴 devstack 打印的 admin 密码>"}')
TOKEN=$(printf '%s' "$LOGIN" | python -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
```

> Windows Git Bash 下如果不想用 Python，也可以手动从登录响应里复制 `access_token`。

## 1. 登录、401 与 RBAC

```bash
# 没有 token
curl -i "$GW/api/whoami"

# 有 token
curl -s "$GW/api/whoami" -H "Authorization: Bearer $TOKEN"

# viewer 访问管理员接口
VLOGIN=$(curl -s -X POST "$GW/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"viewer","password":"<粘贴 devstack 打印的 viewer 密码>"}')
VTOKEN=$(printf '%s' "$VLOGIN" | python -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
curl -i "$GW/api/admin/ping" -H "Authorization: Bearer $VTOKEN"
```

预期：401 / 200 / 403。

## 2. 南北向与 90/10 金丝雀

```bash
for i in $(seq 1 20); do
  curl -s "$GW/api/users/$i" -H "Authorization: Bearer $TOKEN"
  echo
done | grep -o '"version":"v[12]"' | sort | uniq -c
```

预期大约 `v1: 18`、`v2: 2`。

强制走 v2：

```bash
for i in $(seq 1 5); do
  curl -s "$GW/api/users/$i" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'x-canary: true'
  echo
done | grep -o '"version":"v[12]"' | sort | uniq -c
```

预期全部是 v2。

## 3. 东西向调用

```bash
curl -s "$GW/api/orders/1" -H "Authorization: Bearer $TOKEN" | python -m json.tool
```

预期返回订单信息，并且 `user.source` 是：

```text
spiffe://cluster.local/ns/default/sa/order-service
```

说明 order-service 是通过自己的 sidecar 调用 user-service 的。

## 4. 超时返回 504

```bash
curl -s -o /dev/null -w 'status=%{http_code} time=%{time_total}s\n' \
  "$GW/api/orders/slow" -H "Authorization: Bearer $TOKEN"
```

预期约 2 秒后返回 504（Edge 的 orders 路由 timeout 是 2 秒）。

## 5. 故障注入与重试

```bash
curl -s -w '\nstatus=%{http_code} time=%{time_total}s\n' \
  "$GW/api/users/faulty" -H "Authorization: Bearer $TOKEN"
```

预期 500，耗时约 150ms；VirtualService 配置了 2 次重试，每次都会重新触发 100% abort。

观察指标：

```bash
curl -s "$GW/metrics" | grep mesh_retries_total
```

## 6. 限流

orders 路由默认 10 rps / burst 20：

```bash
go run ./cmd/loadgen \
  --url "$GW/api/orders/1" \
  --requests 80 \
  --concurrency 20 \
  --rps 50 \
  --bearer "$TOKEN"
```

预期输出里出现 429，例如：

```text
status: 200=35  429=45
```

## 7. refresh 轮换与登出吊销

```bash
REFRESH=$(printf '%s' "$LOGIN" | python -c 'import sys,json; print(json.load(sys.stdin)["refresh_token"])')

# 刷新，旧 refresh 立即失效
REFRESH_RESPONSE=$(curl -s -X POST "$GW/auth/refresh" \
  -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH\"}")
NEW_TOKEN=$(printf '%s' "$REFRESH_RESPONSE" | python -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
NEW_REFRESH=$(printf '%s' "$REFRESH_RESPONSE" | python -c 'import sys,json; print(json.load(sys.stdin)["refresh_token"])')

# 再用旧 refresh
curl -i -X POST "$GW/auth/refresh" \
  -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$REFRESH\"}"

# 登出后 access token 立即失效
curl -i -X POST "$GW/auth/logout" \
  -H "Authorization: Bearer $NEW_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"refresh_token\":\"$NEW_REFRESH\"}"

curl -i "$GW/api/whoami" -H "Authorization: Bearer $NEW_TOKEN"
```

预期：旧 refresh 401，logout 204，登出后的 access 401。

## 8. mTLS：PERMISSIVE → STRICT

默认 `configs/mesh/destinationrules.yaml` 里 user-service 是 `PERMISSIVE`：

```bash
# 直接访问 sidecar inbound，没有客户端证书，身份是 anonymous
curl -sk https://127.0.0.1:19101/users/1 | python -m json.tool
```

把 user-service 的 `peer_authentication` 改成 `STRICT`，等 3 秒让控制面热更新：

```bash
# 直连被拒绝
curl -sk -i https://127.0.0.1:19101/users/1

# 网格内部调用仍然成功（Edge 和 order sidecar 都有证书）
curl -s -o /dev/null -w '%{http_code}\n' "$GW/api/users/1" -H "Authorization: Bearer $TOKEN"
curl -s -o /dev/null -w '%{http_code}\n' "$GW/api/orders/1" -H "Authorization: Bearer $TOKEN"
```

预期：直连 403，网格调用 200。改回 `PERMISSIVE` 后直连恢复 200。

## 9. 直接使用 sidecar 代理

不写 Go 代码，直接用 curl 走 order-service 的 sidecar outbound：

```bash
curl -s -x http://127.0.0.1:15003 http://user-service/users/7 | python -m json.tool
```

预期 `source` 是 order-service 的 SPIFFE 身份。

## 10. 指标、日志和看板

```bash
# Edge
curl -s "$GW/metrics" | grep -E '^(gateway_|mesh_)' | head

# 某个 sidecar
curl -s http://127.0.0.1:19203/metrics | grep '^mesh_' | head

# 控制面
curl -s http://127.0.0.1:18000/metrics | grep '^mesh_'

# SSE 日志（Ctrl+C 结束）
curl -N "$GW/api/logs/stream?access_token=$TOKEN"
```

浏览器：

- 边缘网关看板：<http://127.0.0.1:8080/>
- 控制面面板：<http://127.0.0.1:18000/>

看板包含登录、请求构造器、响应、RPS/P95/错误率、路由与上游健康、Mesh 面板、实时日志和最近请求。

## 11. 熔断 / 离群摘除

停止当前 devstack，用 chaos 模式重启：

```bash
go run ./cmd/devstack --chaos
```

v2 会对所有 `/users/*` 返回 500。用新 devstack 打印的密码重新登录，然后压测：

```bash
LOGIN=$(curl -s -X POST "$GW/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<新 devstack 打印的密码>"}')
TOKEN=$(printf '%s' "$LOGIN" | python -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')

go run ./cmd/loadgen \
  --url "$GW/api/users/1" \
  --requests 60 \
  --concurrency 10 \
  --bearer "$TOKEN"
```

预期出现 503（v2 被摘除后该子集没有可用实例），同时指标出现：

```bash
curl -s "$GW/metrics" | grep mesh_outlier_ejections_total
```

约 10 秒后摘除窗口结束，v2 会被重新尝试。

## 12. 注册中心与配置版本

```bash
curl -s http://127.0.0.1:18000/v1/registry | python -m json.tool
curl -s http://127.0.0.1:18000/v1/config | python -c 'import sys,json; d=json.load(sys.stdin); print("version", d["version"], "services", len(d["services"]))'
curl -s "$GW/api/routes" -H "Authorization: Bearer $TOKEN" | python -m json.tool
```

可以看到每个实例的 `HEALTHY` 状态、子集标签和配置版本。

## 停止

```bash
curl -X POST http://127.0.0.1:18099/shutdown
```

或在 devstack 终端按 `Ctrl+C`。
