const TOKEN_KEY = 'gateway.console.token'

const LOGIN_ERRORS = {
  'Invalid username or password': '用户名或密码不正确',
  'Missing credentials': '请填写用户名和密码',
  'Invalid request body': '请求格式不正确',
}

export function getToken() {
  return sessionStorage.getItem(TOKEN_KEY) || ''
}

export function saveToken(token) {
  sessionStorage.setItem(TOKEN_KEY, token)
}

export function clearToken() {
  sessionStorage.removeItem(TOKEN_KEY)
}

export function decodeJwtPayload(token) {
  if (!token) return null

  const parts = token.split('.')
  if (parts.length < 2) return null

  const base64 = parts[1].replace(/-/g, '+').replace(/_/g, '/')
  const padded = base64.padEnd(Math.ceil(base64.length / 4) * 4, '=')

  try {
    const bytes = Uint8Array.from(atob(padded), (char) => char.charCodeAt(0))
    return JSON.parse(new TextDecoder().decode(bytes))
  } catch {
    return null
  }
}

export async function loginRequest(username, password) {
  let response

  try {
    response = await fetch('/auth/login', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ username, password }),
    })
  } catch {
    throw new Error('无法连接网关，请确认 :8080 已启动')
  }

  if (!response.ok) {
    const text = (await response.text()).trim()
    throw new Error(
      LOGIN_ERRORS[text] || text || `登录失败（${response.status}）`,
    )
  }

  const data = await response.json()

  if (!data.access_token) {
    throw new Error('网关没有返回 Token')
  }

  saveToken(data.access_token)
  return data
}

export async function fetchJSON(path) {
  const headers = {}
  const token = getToken()

  if (token && path.startsWith('/api/')) {
    headers.Authorization = `Bearer ${token}`
  }

  let response

  try {
    response = await fetch(path, { headers, cache: 'no-store' })
  } catch {
    throw new Error('无法连接网关，请确认 :8080 已启动')
  }

  if (!response.ok) {
    const text = (await response.text()).trim()
    const error = new Error(text || `${response.status} ${response.statusText}`)
    error.status = response.status
    throw error
  }

  return response.json()
}

export async function request(path, { withAuth = true, signal } = {}) {
  const headers = {
    Accept: 'application/json, text/plain, */*',
  }

  const token = getToken()

  if (withAuth && token && path.startsWith('/api/')) {
    headers.Authorization = `Bearer ${token}`
  }

  const started = performance.now()
  let response

  try {
    response = await fetch(path, {
      headers,
      signal,
      cache: 'no-store',
    })
  } catch (error) {
    if (error.name === 'AbortError') {
      throw error
    }

    const networkError = new Error('无法连接网关，请确认 :8080 已启动')
    networkError.duration = performance.now() - started
    throw networkError
  }

  const body = await response.text()

  return {
    status: response.status,
    statusText: response.statusText,
    ok: response.ok,
    headers: [...response.headers.entries()],
    body,
    duration: performance.now() - started,
    at: new Date(),
    path,
    withAuth,
  }
}
