<script setup>
import { ref } from 'vue'
import {
  cancelRequest,
  consoleState,
  sendRequest,
} from '../composables/useConsole'
import { formatDuration } from '../lib/format'

const presets = [
  { label: '当前用户', path: '/api/whoami', withAuth: true },
  { label: '用户详情', path: '/api/users/1', withAuth: true },
  { label: '慢订单', path: '/api/orders/slow', withAuth: true },
  { label: '健康检查', path: '/health', withAuth: false },
]

const copyLabel = ref('')

function applyPreset(preset) {
  consoleState.path = preset.path
  consoleState.withAuth = preset.withAuth
}

function submit() {
  sendRequest(consoleState.path, { withAuth: consoleState.withAuth })
}

function demoUnauthorized() {
  consoleState.path = '/api/whoami'
  consoleState.withAuth = false
  sendRequest('/api/whoami', { withAuth: false })
}

function buildCurl() {
  const needsAuth =
    consoleState.withAuth && consoleState.path.startsWith('/api/')
  const auth = needsAuth
    ? " -H 'Authorization: Bearer $TOKEN'"
    : ''

  return `curl -s 'http://127.0.0.1:8080${consoleState.path}'${auth}`
}

async function copyCurl() {
  try {
    await navigator.clipboard.writeText(buildCurl())
    copyLabel.value = '已复制'
  } catch {
    copyLabel.value = '复制失败'
  }

  window.setTimeout(() => {
    copyLabel.value = ''
  }, 1600)
}
</script>

<template>
  <article class="card">
    <div class="card-head">
      <div>
        <p class="eyebrow">Request</p>
        <h2 class="card-title">请求构造器</h2>
      </div>
      <span class="chip chip-primary">GET</span>
    </div>

    <div class="presets">
      <button
        v-for="preset in presets"
        :key="preset.path"
        class="preset"
        :class="{ active: consoleState.path === preset.path }"
        type="button"
        @click="applyPreset(preset)"
      >
        <span class="preset-label">{{ preset.label }}</span>
        <span class="preset-path mono">{{ preset.path }}</span>
      </button>
    </div>

    <label class="field path-field">
      <span class="field-label">请求路径</span>
      <div class="path-row">
        <span class="method-tag mono">GET</span>
        <input
          v-model="consoleState.path"
          class="input path-input mono"
          spellcheck="false"
          aria-label="请求路径"
          @keyup.enter="submit"
        />
      </div>
    </label>

    <div class="options">
      <label class="switch">
        <input v-model="consoleState.withAuth" type="checkbox" />
        <span class="switch-track"><span class="switch-thumb" /></span>
        <span class="switch-label">携带 Token</span>
      </label>
      <span class="inline-note">超时上限 2s</span>
    </div>

    <div class="actions">
      <button
        class="btn btn-primary btn-block"
        type="button"
        :disabled="consoleState.pending"
        @click="submit"
      >
        <span v-if="consoleState.pending" class="spinner" aria-hidden="true" />
        {{
          consoleState.pending
            ? `请求中 · ${formatDuration(consoleState.elapsedMs)}`
            : '发送请求'
        }}
      </button>
      <button
        v-if="consoleState.pending"
        class="btn btn-ghost btn-block"
        type="button"
        @click="cancelRequest"
      >
        取消请求
      </button>
    </div>

    <div class="quick">
      <button class="btn-quiet" type="button" @click="demoUnauthorized">
        演示 401
      </button>
      <button class="btn-quiet" type="button" @click="copyCurl">
        {{ copyLabel || '复制为 curl' }}
      </button>
    </div>
  </article>
</template>

<style scoped>
.presets {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 20px;
}

.preset {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 12px 14px;
  border: none;
  border-radius: var(--radius-md);
  background: var(--surface-2);
  color: var(--ink-soft);
  text-align: left;
  transition:
    background 0.18s ease,
    color 0.18s ease,
    transform 0.18s ease;
}

.preset:hover {
  background: var(--surface-3);
}

.preset:active {
  transform: translateY(1px);
}

.preset.active {
  background: var(--primary-soft);
  color: var(--primary-deep);
}

.preset-label {
  font-size: var(--fs-caption);
  font-weight: 600;
  line-height: 1.4;
}

.preset-path {
  overflow: hidden;
  font-size: var(--fs-caption);
  opacity: 0.72;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.path-field {
  margin-bottom: 18px;
}

.path-row {
  position: relative;
}

.method-tag {
  position: absolute;
  top: 50%;
  left: 12px;
  padding: 4px 8px;
  border-radius: 9px;
  background: var(--surface-3);
  color: var(--primary-deep);
  font-size: var(--fs-caption);
  font-weight: 600;
  line-height: 1.3;
  transform: translateY(-50%);
}

.path-input {
  padding-left: 68px;
}

.options {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 20px;
}

.switch {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  cursor: pointer;
  user-select: none;
}

.switch input {
  position: absolute;
  width: 0;
  height: 0;
  opacity: 0;
}

.switch-track {
  position: relative;
  width: 44px;
  height: 26px;
  border-radius: var(--radius-pill);
  background: var(--surface-3);
  transition: background 0.2s ease;
}

.switch-thumb {
  position: absolute;
  top: 3px;
  left: 3px;
  width: 20px;
  height: 20px;
  border-radius: 50%;
  background: #fff8ef;
  box-shadow: 0 2px 6px rgba(120, 63, 38, 0.28);
  transition: transform 0.2s ease;
}

.switch input:checked + .switch-track {
  background: var(--success-bright);
}

.switch input:checked + .switch-track .switch-thumb {
  transform: translateX(18px);
}

.switch input:focus-visible + .switch-track {
  outline: 2px solid var(--primary);
  outline-offset: 3px;
}

.switch-label {
  color: var(--ink-soft);
  font-size: var(--fs-caption);
  font-weight: 600;
}

.inline-note {
  color: var(--ink-faint);
  font-size: var(--fs-caption);
}

.actions {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.spinner {
  width: 16px;
  height: 16px;
  border: 2px solid rgba(255, 248, 239, 0.4);
  border-top-color: #fff8ef;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.quick {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 14px;
}
</style>
