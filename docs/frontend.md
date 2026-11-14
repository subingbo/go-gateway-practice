# 前端看板

[返回 README](../README.md)

看板是一个 Vue 3 + Vite 单页应用，源码在 `gateway/web/`，构建产物由网关通过 `go:embed` 托管在 `GET /`。没有 UI 库、图表库和路由库。

## 开发与构建

```powershell
cd gateway/web
npm install

# 开发：Vite 监听 5173，代理 /api、/auth、/health 到 8080
npm run dev

# 生产：产物写入 gateway/web/dist，随后需要重新编译网关
npm run build
cd ../..
go run ./gateway
```

`vite.config.js` 中配置了开发代理：

```js
server: {
  port: 5173,
  proxy: {
    '/api': 'http://127.0.0.1:8080',
    '/auth': 'http://127.0.0.1:8080',
    '/health': 'http://127.0.0.1:8080',
  },
}
```

构建后 `dist/.gitkeep` 会被重新创建，保证没有前端产物时 `go build` 仍能通过，页面会显示构建提示。

## 页面结构

```text
Header      标题、网关健康状态、Token 倒计时、/health 链接
Row 1       认证卡（4 栅格） | 服务状态（8 栅格）
Row 2       请求构造器（4） | 响应面板（8）
Row 3       指标（12）：总请求、成功率、P95、进行中、延迟趋势、状态分布
Row 4       路由与上游（5） | 请求历史（7）
```

窄于 980px 时所有卡片变为单列。

## 设计令牌

定义在 `src/styles/tokens.css`。

### 颜色

以暖橙为主轴，副色取梅子棕，语义色保持暖调，不使用中性灰分区。

| Token | 色值 | 用途 |
|---|---|---|
| `--bg` | `#faf2e7` | 页面底色 |
| `--surface` | `#fff8ef` | 卡片 |
| `--surface-2` | `#f3e3d0` | 输入框、次级块 |
| `--primary` | `#d97545` | 主色：焦点、折线、装饰 |
| `--primary-strong` | `#b4522c` | 主按钮背景 |
| `--primary-deep` | `#96401f` | hover、强调文字 |
| `--secondary` | `#7e4e5d` | 副色：eyebrow、次要强调 |
| `--success` | `#436a34` | 2xx、在线 |
| `--warning` | `#855a12` | 504、Token 即将过期 |
| `--danger` | `#a33329` | 401、离线、5xx |
| `--ink` | `#35261e` | 正文 |
| `--ink-soft` | `#6f5647` | 辅助文字 |
| `--ink-faint` | `#7c5d49` | 表头、说明文字 |

卡片之间只用背景色阶、留白和柔影区分，全局不设置描边。

### 字号

严格 1.5 阶梯，锚点 18px：

| Token | 值 | 用途 |
|---|---|---|
| `--fs-caption` | 12px | 表头、徽标、辅助说明 |
| `--fs-body` | 18px | 正文、输入、表格 |
| `--fs-title` | 27px | 卡片标题 |
| `--fs-page` | 40.5px | 页面标题、指标数字 |
| `--fs-hero` | 60.75px | 总请求大数字 |

标题使用衬线字体，数据使用等宽字体并开启 `tabular-nums`。

## 组件职责

| 组件 | 职责 |
|---|---|
| `AppHeader` | 品牌标题、网关状态、Token 倒计时 |
| `AuthCard` | 登录表单；已登录时展示用户、签发/到期时间、JTI、倒计时环 |
| `ServiceStatus` | 网关健康检查；用户/订单服务最近一次请求结果 |
| `RequestBuilder` | 预设端点、路径输入、Token 开关、发送/取消、演示 401、复制 curl |
| `ResponsePanel` | 状态码、耗时、请求旅程、响应体/响应头、复制 |
| `MetricsPanel` | 全局/会话指标切换、四项数字、状态分布 |
| `LatencyChart` | 手写 SVG 延迟折线，按状态码着色 |
| `RoutesTable` | 展示 `/api/routes` |
| `HistoryTable` | 最近 50 条请求，支持重放与清空 |

## 状态与轮询

- Token 存在 `sessionStorage`，键名 `gateway.console.token`；关闭标签页即丢失
- 网关健康检查每 10 秒一次
- `/api/stats` 每 4 秒一次；登录状态变化时立即刷新
- 请求历史只保存在内存，最多 50 条
- 指标卡可切换「网关全局」与「本次会话」；后者由前端自行统计
- 504 在服务状态中显示为「响应超时」，502 显示为「上游异常」

## 交互细节

- 发送请求时可以取消（`AbortController`），取消不会写入历史
- 「演示 401」强制不携带 Token
- 「复制为 curl」使用 `$TOKEN` 占位，不复制真实 Token
- 令牌剩余时间小于 60 秒变黄，过期变红并自动清除
- 尊重 `prefers-reduced-motion`，键盘焦点可见
