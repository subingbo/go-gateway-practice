export const TONE_HEX = {
  success: '#5f8b4c',
  warning: '#c08a2c',
  danger: '#c0453a',
  idle: '#a08472',
}

export function formatDuration(ms) {
  if (!Number.isFinite(ms) || ms <= 0) return '0 ms'
  if (ms < 10) return `${ms.toFixed(1)} ms`
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(2)} s`
}

export function formatTime(value) {
  const date = value instanceof Date ? value : new Date(value)

  if (Number.isNaN(date.getTime())) return '—'

  return date.toLocaleTimeString('zh-CN', {
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

export function formatDateTime(value) {
  const date = value instanceof Date ? value : new Date(value)

  if (Number.isNaN(date.getTime())) return '—'

  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  const time = date.toLocaleTimeString('zh-CN', {
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })

  return `${month}-${day} ${time}`
}

export function formatUptime(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return '—'

  const total = Math.floor(seconds)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const rest = total % 60

  if (hours > 0) {
    return `${hours} 小时 ${String(minutes).padStart(2, '0')} 分`
  }

  if (minutes > 0) {
    return `${minutes} 分 ${String(rest).padStart(2, '0')} 秒`
  }

  return `${rest} 秒`
}

export function formatNumber(value) {
  if (!Number.isFinite(value)) return '—'
  return new Intl.NumberFormat('zh-CN').format(value)
}

export function statusTone(status) {
  if (!status) return 'idle'
  if (status >= 200 && status < 300) return 'success'
  if (status === 401 || status === 403) return 'danger'
  if (status >= 400 && status < 500) return 'warning'
  if (status >= 500) return 'danger'
  return 'warning'
}

export function statusLabel(status) {
  if (!status) return '网络错误'
  if (status >= 200 && status < 300) return '成功'
  if (status === 400) return '请求有误'
  if (status === 401) return '未授权'
  if (status === 404) return '未找到'
  if (status === 502) return '上游不可达'
  if (status === 504) return '网关超时'
  if (status >= 500) return '网关错误'
  return `HTTP ${status}`
}

export function prettyBody(body) {
  if (!body) return ''

  try {
    return JSON.stringify(JSON.parse(body), null, 2)
  } catch {
    return body
  }
}

export function percent(part, total) {
  if (!total) return 0
  return Math.round((part / total) * 100)
}
