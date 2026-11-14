import { computed, reactive } from 'vue'
import {
  clearToken,
  decodeJwtPayload,
  getToken,
  loginRequest,
} from '../lib/api'
import { formatDateTime } from '../lib/format'

const initialToken = getToken()

export const authState = reactive({
  token: initialToken,
  payload: decodeJwtPayload(initialToken),
  pending: false,
  error: '',
  now: Date.now(),
})

function tick() {
  authState.now = Date.now()

  if (
    authState.token &&
    authState.payload?.exp &&
    authState.now >= authState.payload.exp * 1000
  ) {
    signOut()
  }
}

tick()
window.setInterval(tick, 1000)

export const isAuthenticated = computed(() => Boolean(authState.token))

export const userID = computed(() => authState.payload?.user_id || '')

export const issuedAt = computed(() =>
  authState.payload?.iat ? authState.payload.iat * 1000 : 0,
)

export const expiresAt = computed(() =>
  authState.payload?.exp ? authState.payload.exp * 1000 : 0,
)

export const issuedLabel = computed(() => formatDateTime(issuedAt.value))

export const expiresLabel = computed(() => formatDateTime(expiresAt.value))

export const remainingMs = computed(() => {
  if (!expiresAt.value) return 0
  return Math.max(0, expiresAt.value - authState.now)
})

export const remainingRatio = computed(() => {
  if (!expiresAt.value) return 0

  const total = Math.max(1, expiresAt.value - issuedAt.value)
  return Math.min(1, remainingMs.value / total)
})

export const countdown = computed(() => {
  if (!authState.token) return '—'

  const totalSeconds = Math.ceil(remainingMs.value / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60

  return `${minutes}:${String(seconds).padStart(2, '0')}`
})

export const tokenLevel = computed(() => {
  if (!authState.token) return 'idle'
  if (remainingMs.value <= 0) return 'danger'
  if (remainingMs.value <= 60 * 1000) return 'warning'
  return 'success'
})

export const tokenJTI = computed(() => authState.payload?.jti || '')

export async function signIn(username, password) {
  if (authState.pending) return

  authState.error = ''

  if (!username.trim() || !password) {
    authState.error = '请填写用户名和密码'
    return
  }

  authState.pending = true

  try {
    const data = await loginRequest(username.trim(), password)
    authState.token = data.access_token
    authState.payload = decodeJwtPayload(data.access_token)
    authState.now = Date.now()
  } catch (error) {
    authState.error = error.message
  } finally {
    authState.pending = false
  }
}

export function signOut() {
  clearToken()
  authState.token = ''
  authState.payload = null
  authState.error = ''
}
