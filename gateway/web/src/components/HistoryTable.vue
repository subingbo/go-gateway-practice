<script setup>
import { computed } from 'vue'
import {
  clearHistory,
  consoleState,
  replay,
} from '../composables/useConsole'
import {
  formatDuration,
  formatTime,
  statusLabel,
  statusTone,
} from '../lib/format'

const history = computed(() => consoleState.history)
</script>

<template>
  <article class="card">
    <div class="card-head">
      <div>
        <p class="eyebrow">Session Log</p>
        <h2 class="card-title">请求历史</h2>
      </div>
      <div class="head-tools">
        <span class="card-note">{{ history.length }} / 50</span>
        <button
          class="btn-quiet"
          type="button"
          :disabled="!history.length"
          @click="clearHistory"
        >
          清空
        </button>
      </div>
    </div>

    <div v-if="history.length" class="scroll-area">
      <table class="data-table history-table">
        <thead>
          <tr>
            <th>时间</th>
            <th>状态</th>
            <th>请求</th>
            <th>耗时</th>
            <th><span class="visually-hidden">操作</span></th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="entry in history"
            :key="entry.id"
            :title="statusLabel(entry.status)"
          >
            <td>
              <span class="mono history-time">{{ formatTime(entry.at) }}</span>
            </td>
            <td>
              <span class="chip" :class="`chip-${statusTone(entry.status)}`">
                {{ entry.status || 'ERR' }}
              </span>
            </td>
            <td>
              <span class="history-path">
                <span class="chip chip-primary">GET</span>
                <span class="mono history-route">{{ entry.path }}</span>
              </span>
            </td>
            <td>
              <span class="mono history-duration">
                {{ formatDuration(entry.duration) }}
              </span>
            </td>
            <td class="action-cell">
              <button
                class="btn-quiet"
                type="button"
                :disabled="consoleState.pending"
                @click="replay(entry)"
              >
                重放
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else class="empty">
      <p class="empty-title">还没有请求记录</p>
      <p class="empty-note">
        每次发送请求都会记在这里，最多保留 50 条；点击「重放」可以再发一次。
      </p>
    </div>
  </article>
</template>

<style scoped>
.head-tools {
  display: flex;
  align-items: center;
  gap: 8px;
}

.history-table {
  font-size: var(--fs-caption);
}

.history-time,
.history-duration {
  color: var(--ink-soft);
  font-size: var(--fs-caption);
}

.history-path {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.history-route {
  overflow: hidden;
  color: var(--ink);
  font-size: var(--fs-caption);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.action-cell {
  text-align: right;
}

.visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}

@media (max-width: 640px) {
  .history-route {
    max-width: 150px;
  }
}
</style>
