import { computed, reactive, watch } from 'vue'
import { fetchJSON, getToken } from '../lib/api'
import { authState } from './useAuth'
import { formatDuration, formatTime, formatUptime } from '../lib/format'

export const gatewayState = reactive({
  health: 'unknown',
  healthChecked: false,
  stats: null,
  statsError: '',
  routes: [],
  routesError: '',
  routesLoading: false,
  lastUpdated: null,
})

let started = false
let healthTimer = null
let statsTimer = null

async function checkHealth() {
  try {
    const response = await fetch('/health', { cache: 'no-store' })
    gatewayState.health = response.ok ? 'online' : 'offline'
  } catch {
    gatewayState.health = 'offline'
  }

  gatewayState.healthChecked = true
}

async function loadStats() {
  if (!getToken()) {
    gatewayState.stats = null
    gatewayState.statsError = ''
    return
  }

  try {
    gatewayState.stats = await fetchJSON('/api/stats')
    gatewayState.statsError = ''
    gatewayState.lastUpdated = new Date()
  } catch (error) {
    gatewayState.statsError =
      error.status === 401 ? '登录已失效，请重新登录' : error.message
  }
}

async function loadRoutes() {
  if (!getToken()) {
    gatewayState.routes = []
    gatewayState.routesError = ''
    return
  }

  gatewayState.routesLoading = true

  try {
    const data = await fetchJSON('/api/routes')
    gatewayState.routes = data.routes || []
    gatewayState.routesError = ''
  } catch (error) {
    gatewayState.routesError =
      error.status === 401 ? '登录已失效，请重新登录' : error.message
  } finally {
    gatewayState.routesLoading = false
  }
}

export function startGatewayPolling() {
  if (started) return

  started = true

  checkHealth()
  loadStats()
  loadRoutes()

  healthTimer = window.setInterval(checkHealth, 10000)
  statsTimer = window.setInterval(loadStats, 4000)

  watch(
    () => authState.token,
    () => {
      loadStats()
      loadRoutes()
    },
  )
}

function latestFor(prefix) {
  const recent = gatewayState.stats?.recent || []

  for (let index = recent.length - 1; index >= 0; index -= 1) {
    if (recent[index].path.startsWith(prefix)) {
      return recent[index]
    }
  }

  return null
}

function toneForRecord(record) {
  if (!record) return 'idle'
  if (record.status === 504) return 'warning'
  if (record.status >= 500) return 'danger'
  if (record.status >= 400) return 'warning'
  return 'success'
}

function upstreamService(name, endpoint, prefix) {
  const latest = latestFor(prefix)
  const tone = toneForRecord(latest)

  const stateLabel = !latest
    ? '未测试'
    : latest.status === 504
      ? '响应超时'
      : tone === 'success'
        ? '已连通'
        : tone === 'warning'
          ? '返回错误'
          : '上游异常'

  return {
    key: prefix,
    name,
    endpoint,
    tone,
    stateLabel,
    detail: latest
      ? `最近 ${latest.status} · ${formatDuration(latest.duration_ms)} · ${formatTime(latest.time)}`
      : '通过看板或 curl 发起一次请求',
    latest,
  }
}

export const services = computed(() => {
  const stats = gatewayState.stats
  const health = gatewayState.health

  const gatewayTone =
    health === 'online' ? 'success' : health === 'offline' ? 'danger' : 'idle'

  const gatewayStateLabel =
    health === 'online' ? '在线' : health === 'offline' ? '离线' : '检查中'

  let gatewayDetail = '等待首次健康检查'

  if (health === 'online') {
    gatewayDetail = stats
      ? `健康检查通过 · 已运行 ${formatUptime(stats.uptime_seconds)}`
      : '健康检查通过 · 登录后读取全局指标'
  } else if (health === 'offline') {
    gatewayDetail = '无法连接 /health，请确认网关已启动'
  }

  return [
    {
      key: 'gateway',
      name: '网关',
      endpoint: ':8080',
      tone: gatewayTone,
      stateLabel: gatewayStateLabel,
      detail: gatewayDetail,
    },
    upstreamService('用户服务', ':9090', '/api/users/'),
    upstreamService('订单服务', ':9091', '/api/orders/'),
  ]
})
