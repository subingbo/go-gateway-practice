import { computed, reactive } from 'vue'
import { request } from '../lib/api'

const MAX_HISTORY = 50

export const consoleState = reactive({
  method: 'GET',
  path: '/api/whoami',
  withAuth: true,
  pending: false,
  elapsedMs: 0,
  response: null,
  error: '',
  notice: '',
  history: [],
})

let controller = null
let elapsedTimer = null

function startElapsed() {
  const startedAt = performance.now()

  consoleState.elapsedMs = 0

  elapsedTimer = window.setInterval(() => {
    consoleState.elapsedMs = performance.now() - startedAt
  }, 100)
}

function stopElapsed() {
  if (elapsedTimer !== null) {
    window.clearInterval(elapsedTimer)
    elapsedTimer = null
  }
}

function pushHistory(response) {
  consoleState.history.unshift({
    id: `${Date.now()}-${Math.random().toString(16).slice(2, 8)}`,
    at: response.at,
    method: consoleState.method,
    path: response.path,
    status: response.status,
    duration: response.duration,
    withAuth: response.withAuth,
  })

  if (consoleState.history.length > MAX_HISTORY) {
    consoleState.history.length = MAX_HISTORY
  }
}

export async function sendRequest(path = consoleState.path, options = {}) {
  if (consoleState.pending) return null

  const target = String(path || '').trim()

  if (!target) {
    consoleState.error = '请输入请求路径'
    return null
  }

  consoleState.path = target
  consoleState.withAuth = options.withAuth ?? true
  consoleState.pending = true
  consoleState.response = null
  consoleState.error = ''
  consoleState.notice = ''

  controller = new AbortController()

  startElapsed()

  try {
    const response = await request(target, {
      withAuth: consoleState.withAuth,
      signal: controller.signal,
    })

    consoleState.response = response
    pushHistory(response)

    return response
  } catch (error) {
    if (error.name === 'AbortError') {
      consoleState.notice = '请求已取消'
      return null
    }

    consoleState.error = error.message

    pushHistory({
      at: new Date(),
      path: target,
      status: 0,
      duration: error.duration || 0,
      withAuth: consoleState.withAuth,
    })

    return null
  } finally {
    stopElapsed()
    consoleState.pending = false
    controller = null
  }
}

export function cancelRequest() {
  controller?.abort()
}

export function replay(entry) {
  if (consoleState.pending) return

  consoleState.path = entry.path
  consoleState.withAuth = entry.withAuth

  sendRequest(entry.path, { withAuth: entry.withAuth })
}

export function clearHistory() {
  consoleState.history.length = 0
}

export const sessionMetrics = computed(() => {
  const history = consoleState.history
  const total = history.length
  const classes = {
    '2xx': 0,
    '3xx': 0,
    '4xx': 0,
    '5xx': 0,
  }

  let durationTotal = 0

  for (const entry of history) {
    durationTotal += entry.duration

    if (entry.status >= 200 && entry.status < 300) {
      classes['2xx'] += 1
    } else if (entry.status >= 300 && entry.status < 400) {
      classes['3xx'] += 1
    } else if (entry.status >= 400 && entry.status < 500) {
      classes['4xx'] += 1
    } else if (entry.status >= 500) {
      classes['5xx'] += 1
    }
  }

  const durations = history
    .map((entry) => entry.duration)
    .sort((left, right) => left - right)

  const p95 = durations.length
    ? durations[
        Math.min(
          durations.length - 1,
          Math.ceil(0.95 * durations.length) - 1,
        )
      ]
    : 0

  const success = classes['2xx']

  return {
    total,
    success,
    errors: total - success,
    successRate: total ? Math.round((success / total) * 100) : null,
    avg: total ? durationTotal / total : 0,
    p95,
    classes,
    inFlight: consoleState.pending ? 1 : 0,
  }
})
