<script setup>
import { computed, ref, watch } from 'vue'
import { consoleState } from '../composables/useConsole'
import {
  formatDuration,
  prettyBody,
  statusLabel,
  statusTone,
} from '../lib/format'

const tab = ref('body')
const copyLabel = ref('')

const response = computed(() => consoleState.response)
const tone = computed(() => statusTone(response.value?.status))
const body = computed(() => prettyBody(response.value?.body))
const headers = computed(() => response.value?.headers || [])

const target = computed(() => {
  const path = response.value?.path || consoleState.path

  if (path.startsWith('/api/users/')) {
    return { label: '用户服务', endpoint: ':9090', local: false }
  }

  if (path.startsWith('/api/orders/')) {
    return { label: '订单服务', endpoint: ':9091', local: false }
  }

  return { label: '网关本地', endpoint: ':8080', local: true }
})

const upstreamTone = computed(() => {
  if (target.value.local) return 'success'
  if (!response.value || response.value.status === 401) return 'idle'
  return tone.value
})

const gatewayTone = computed(() => {
  if (!response.value) return 'idle'
  if (response.value.status === 401) return 'warning'
  return tone.value
})

watch(
  () => consoleState.response,
  () => {
    tab.value = 'body'
  },
)

async function copyBody() {
  try {
    await navigator.clipboard.writeText(response.value?.body || '')
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
        <p class="eyebrow">Response</p>
        <h2 class="card-title">响应</h2>
      </div>
      <div v-if="response" class="head-status">
        <span class="chip" :class="`chip-${tone}`">
          {{ response.status }} {{ statusLabel(response.status) }}
        </span>
        <span class="mono head-duration">
          {{ formatDuration(response.duration) }}
        </span>
      </div>
    </div>

    <div v-if="consoleState.pending" class="pending" aria-live="polite">
      <span class="spinner" aria-hidden="true" />
      <p class="pending-title">等待网关响应</p>
      <p class="pending-time mono">{{ formatDuration(consoleState.elapsedMs) }}</p>
      <p class="empty-note">
        慢订单会在 2 秒超时后返回 504，同时取消对上游的请求。
      </p>
    </div>

    <div v-else-if="consoleState.error" class="failure" role="alert">
      <p class="failure-title">请求失败</p>
      <p class="failure-message">{{ consoleState.error }}</p>
    </div>

    <template v-else-if="response">
      <div class="response-meta">
        <div class="status-block">
          <span class="status-code mono" :class="`text-${tone}`">
            {{ response.status }}
          </span>
          <span class="status-label">{{ statusLabel(response.status) }}</span>
        </div>
        <div class="meta-item">
          <span class="meta-label">耗时</span>
          <span class="mono">{{ formatDuration(response.duration) }}</span>
        </div>
        <div class="meta-item">
          <span class="meta-label">路径</span>
          <span class="mono">{{ response.path }}</span>
        </div>
        <div class="meta-item">
          <span class="meta-label">Token</span>
          <span>{{ response.withAuth ? '已携带' : '未携带' }}</span>
        </div>
      </div>

      <div class="journey" aria-label="请求旅程">
        <div class="node node-success">
          <span class="node-title">浏览器</span>
          <span class="node-sub mono">fetch</span>
        </div>
        <span class="connector" aria-hidden="true" />
        <div class="node" :class="`node-${gatewayTone}`">
          <span class="node-title">网关</span>
          <span class="node-sub mono">:8080</span>
        </div>
        <span class="connector" aria-hidden="true" />
        <div class="node" :class="`node-${upstreamTone}`">
          <span class="node-title">{{ target.label }}</span>
          <span class="node-sub mono">
            {{ target.local ? '本地处理' : target.endpoint }}
          </span>
        </div>
      </div>

      <div class="body-head">
        <div class="tabs" role="tablist" aria-label="响应内容">
          <button
            class="tab"
            :class="{ active: tab === 'body' }"
            type="button"
            role="tab"
            :aria-selected="tab === 'body'"
            @click="tab = 'body'"
          >
            响应体
          </button>
          <button
            class="tab"
            :class="{ active: tab === 'headers' }"
            type="button"
            role="tab"
            :aria-selected="tab === 'headers'"
            @click="tab = 'headers'"
          >
            响应头
            <span class="tab-count mono">{{ headers.length }}</span>
          </button>
        </div>
        <button class="btn-quiet" type="button" @click="copyBody">
          {{ copyLabel || '复制响应' }}
        </button>
      </div>

      <pre v-if="tab === 'body'" class="code-block">{{ body || '（空响应体）' }}</pre>

      <div v-else class="header-list">
        <div
          v-for="[key, value] in headers"
          :key="key"
          class="header-row"
        >
          <span class="mono header-key">{{ key }}</span>
          <span class="mono header-value">{{ value }}</span>
        </div>
        <p v-if="!headers.length" class="empty-note">没有可展示的响应头。</p>
      </div>
    </template>

    <div v-else class="empty">
      <svg class="empty-art" viewBox="0 0 220 64" aria-hidden="true">
        <circle cx="22" cy="32" r="10" fill="#f2d6c3" />
        <circle cx="110" cy="32" r="10" fill="#ecd7bf" />
        <circle cx="198" cy="32" r="10" fill="#eddde2" />
        <path
          d="M36 32h60"
          fill="none"
          stroke="#e2c8b0"
          stroke-width="2"
          stroke-linecap="round"
          stroke-dasharray="3 6"
        />
        <path
          d="M124 32h60"
          fill="none"
          stroke="#e2c8b0"
          stroke-width="2"
          stroke-linecap="round"
          stroke-dasharray="3 6"
        />
      </svg>
      <p class="empty-title">还没有响应</p>
      <p class="empty-note">
        在左侧发送一个请求，这里会展示状态码、请求旅程和完整响应内容。
      </p>
      <p v-if="consoleState.notice" class="notice">
        {{ consoleState.notice }}
      </p>
    </div>
  </article>
</template>

<style scoped>
.head-status {
  display: flex;
  align-items: center;
  gap: 12px;
}

.head-duration {
  color: var(--ink-faint);
  font-size: var(--fs-caption);
}

.pending {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  min-height: 280px;
  padding: 32px;
}

.spinner {
  width: 30px;
  height: 30px;
  border: 3px solid var(--primary-soft);
  border-top-color: var(--primary);
  border-radius: 50%;
  animation: spin 0.9s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.pending-title {
  margin: 4px 0 0;
  font-size: var(--fs-body);
  font-weight: 600;
}

.pending-time {
  margin: 0;
  color: var(--primary-deep);
  font-size: var(--fs-title);
}

.failure {
  padding: 24px;
  border-radius: var(--radius-lg);
  background: var(--danger-soft);
}

.failure-title {
  margin: 0 0 8px;
  color: var(--danger);
  font-size: var(--fs-body);
  font-weight: 600;
}

.failure-message {
  margin: 0;
  color: var(--danger);
  font-size: var(--fs-caption);
  line-height: 1.6;
}

.response-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 18px 32px;
  margin-bottom: 24px;
}

.status-block {
  display: flex;
  align-items: baseline;
  gap: 10px;
}

.status-code {
  font-size: var(--fs-page);
  font-weight: 700;
  letter-spacing: -0.03em;
  line-height: 1;
}

.text-success {
  color: var(--success);
}

.text-warning {
  color: var(--warning);
}

.text-danger {
  color: var(--danger);
}

.text-idle {
  color: var(--ink-faint);
}

.status-label {
  color: var(--ink-soft);
  font-size: var(--fs-caption);
  font-weight: 600;
}

.meta-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  color: var(--ink-soft);
  font-size: var(--fs-body);
}

.meta-label {
  color: var(--ink-faint);
  font-size: var(--fs-caption);
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.journey {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 24px;
  padding: 16px 20px;
  border-radius: var(--radius-lg);
  background: linear-gradient(120deg, #fdf4e8 0%, #f8e9d8 100%);
}

.node {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 96px;
  padding: 10px 14px;
  border-radius: 14px;
  background: rgba(255, 248, 239, 0.72);
}

.node-title {
  color: var(--ink);
  font-size: var(--fs-caption);
  font-weight: 600;
  line-height: 1.3;
}

.node-sub {
  color: var(--ink-faint);
  font-size: var(--fs-caption);
  line-height: 1.3;
}

.node-success .node-title {
  color: var(--success);
}

.node-warning .node-title {
  color: var(--warning);
}

.node-danger .node-title {
  color: var(--danger);
}

.node-idle {
  opacity: 0.6;
}

.connector {
  flex: 1;
  min-width: 16px;
  height: 2px;
  border-radius: 2px;
  background: linear-gradient(90deg, #e6cdb7, #d8b79c);
}

.body-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.tabs {
  display: inline-flex;
  gap: 4px;
  padding: 4px;
  border-radius: var(--radius-pill);
  background: var(--surface-2);
}

.tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 7px 14px;
  border: none;
  border-radius: var(--radius-pill);
  background: transparent;
  color: var(--ink-soft);
  font-size: var(--fs-caption);
  font-weight: 600;
  transition:
    background 0.16s ease,
    color 0.16s ease;
}

.tab:hover {
  color: var(--primary-deep);
}

.tab.active {
  background: var(--surface);
  color: var(--primary-deep);
  box-shadow: 0 4px 12px -8px rgba(120, 63, 38, 0.45);
}

.tab-count {
  color: var(--ink-faint);
  font-size: var(--fs-caption);
}

.header-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 340px;
  padding: 8px;
  overflow: auto;
  border-radius: var(--radius-md);
  background: var(--surface-2);
}

.header-row {
  display: grid;
  grid-template-columns: minmax(120px, 0.4fr) 1fr;
  gap: 16px;
  padding: 8px 12px;
  border-radius: 10px;
  font-size: var(--fs-caption);
}

.header-row:hover {
  background: rgba(255, 248, 239, 0.6);
}

.header-key {
  color: var(--primary-deep);
  word-break: break-all;
}

.header-value {
  color: var(--ink-soft);
  word-break: break-all;
}

.notice {
  margin: 8px 0 0;
  padding: 8px 14px;
  border-radius: 12px;
  background: var(--warning-soft);
  color: var(--warning);
  font-size: var(--fs-caption);
}

.empty-art {
  width: 220px;
  max-width: 80%;
  height: auto;
}

@media (max-width: 640px) {
  .journey {
    flex-direction: column;
    align-items: stretch;
  }

  .connector {
    width: 2px;
    height: 16px;
    margin: 0 auto;
  }
}
</style>
