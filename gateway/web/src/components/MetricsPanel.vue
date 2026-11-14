<script setup>
import { computed, ref } from 'vue'
import { gatewayState } from '../composables/useGateway'
import { consoleState, sessionMetrics } from '../composables/useConsole'
import {
  formatDuration,
  formatNumber,
  formatTime,
  formatUptime,
} from '../lib/format'
import LatencyChart from './LatencyChart.vue'

const scope = ref('gateway')

const updatedLabel = computed(() =>
  gatewayState.lastUpdated ? formatTime(gatewayState.lastUpdated) : '',
)

const globalMetrics = computed(() => {
  const stats = gatewayState.stats

  if (!stats) return null

  const classes = stats.status_classes
  const total = stats.total_requests

  return {
    total,
    p95: stats.p95_latency_ms,
    avg: stats.avg_latency_ms,
    successRate: total ? Math.round((classes['2xx'] / total) * 100) : null,
    inFlight: stats.in_flight,
    classes,
    uptime: stats.uptime_seconds,
    points: (stats.recent || []).slice(-40).map((item) => ({
      duration: item.duration_ms,
      status: item.status,
    })),
  }
})

const sessionView = computed(() => ({
  ...sessionMetrics.value,
  uptime: null,
  points: [...consoleState.history].reverse().slice(-40).map((entry) => ({
    duration: entry.duration,
    status: entry.status,
  })),
}))

const metrics = computed(() =>
  scope.value === 'gateway' ? globalMetrics.value : sessionView.value,
)

const points = computed(() => metrics.value?.points || [])

const segments = computed(() => {
  const classes = metrics.value?.classes || {
    '2xx': 0,
    '3xx': 0,
    '4xx': 0,
    '5xx': 0,
  }

  const classified =
    classes['2xx'] + classes['3xx'] + classes['4xx'] + classes['5xx']

  const items = [
    { key: '2xx', label: '2xx 成功', color: '#5f8b4c', count: classes['2xx'] },
    { key: '3xx', label: '3xx 跳转', color: '#d9a441', count: classes['3xx'] },
    { key: '4xx', label: '4xx 请求', color: '#c08a2c', count: classes['4xx'] },
    { key: '5xx', label: '5xx 网关', color: '#c0453a', count: classes['5xx'] },
  ]

  return items.map((item) => ({
    ...item,
    width: classified ? (item.count / classified) * 100 : 0,
  }))
})

const visibleSegments = computed(() =>
  segments.value.filter((segment) => segment.width > 0),
)
</script>

<template>
  <article class="card">
    <div class="card-head">
      <div>
        <p class="eyebrow">Telemetry</p>
        <h2 class="card-title">指标</h2>
      </div>

      <div class="head-tools">
        <span v-if="scope === 'gateway' && updatedLabel" class="card-note">
          更新于 {{ updatedLabel }}
        </span>
        <div class="segmented" role="tablist" aria-label="指标范围">
          <button
            class="segment"
            :class="{ active: scope === 'gateway' }"
            type="button"
            role="tab"
            :aria-selected="scope === 'gateway'"
            @click="scope = 'gateway'"
          >
            网关全局
          </button>
          <button
            class="segment"
            :class="{ active: scope === 'session' }"
            type="button"
            role="tab"
            :aria-selected="scope === 'session'"
            @click="scope = 'session'"
          >
            本次会话
          </button>
        </div>
      </div>
    </div>

    <div v-if="metrics" class="metrics">
      <div class="stat-grid">
        <div class="stat stat-lead">
          <p class="stat-label">总请求</p>
          <p class="stat-value stat-hero mono">
            {{ formatNumber(metrics.total) }}
          </p>
          <p class="stat-note">
            {{
              scope === 'gateway'
                ? `已运行 ${formatUptime(metrics.uptime)}`
                : '本次页面会话'
            }}
          </p>
        </div>

        <div class="stat">
          <p class="stat-label">成功率</p>
          <p class="stat-value mono">
            {{ metrics.successRate === null ? '—' : `${metrics.successRate}%` }}
          </p>
          <p class="stat-note">2xx / 全部请求</p>
        </div>

        <div class="stat">
          <p class="stat-label">P95 延迟</p>
          <p class="stat-value mono">{{ formatDuration(metrics.p95) }}</p>
          <p class="stat-note">
            {{ scope === 'gateway' ? '最近 200 次采样' : '本次会话采样' }}
          </p>
        </div>

        <div class="stat">
          <p class="stat-label">进行中</p>
          <p class="stat-value mono">{{ metrics.inFlight }}</p>
          <p class="stat-note">正在穿过网关</p>
        </div>
      </div>

      <div class="lower">
        <div class="chart-block">
          <div class="block-head">
            <span>延迟趋势</span>
            <span class="card-note">{{ points.length }} 个采样</span>
          </div>
          <LatencyChart :points="points" />
        </div>

        <div class="distribution">
          <div class="block-head">
            <span>状态分布</span>
            <span class="card-note">{{ formatNumber(metrics.total) }} 次</span>
          </div>

          <div class="bar" aria-hidden="true">
            <span
              v-for="segment in visibleSegments"
              :key="segment.key"
              class="bar-segment"
              :style="{ width: `${segment.width}%`, background: segment.color }"
            />
          </div>

          <div class="legend">
            <div
              v-for="segment in segments"
              :key="segment.key"
              class="legend-row"
            >
              <span
                class="legend-dot"
                :style="{ background: segment.color }"
              />
              <span class="legend-label">{{ segment.label }}</span>
              <span class="mono legend-count">{{ segment.count }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div v-else class="empty">
      <p class="empty-title">还没有网关指标</p>
      <p class="empty-note">
        登录后每 4 秒读取一次 /api/stats。也可以先切到「本次会话」，查看当前页面发出的请求。
      </p>
    </div>
  </article>
</template>

<style scoped>
.head-tools {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
}

.segmented {
  display: inline-flex;
  gap: 4px;
  padding: 4px;
  border-radius: var(--radius-pill);
  background: var(--surface-2);
}

.segment {
  padding: 8px 16px;
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

.segment:hover {
  color: var(--primary-deep);
}

.segment.active {
  background: var(--surface);
  color: var(--primary-deep);
  box-shadow: 0 4px 12px -8px rgba(120, 63, 38, 0.45);
}

.metrics {
  display: flex;
  flex-direction: column;
  gap: 28px;
}

.stat-grid {
  display: grid;
  grid-template-columns: 1.35fr repeat(3, minmax(0, 1fr));
  gap: 16px;
}

.stat {
  padding: 20px;
  border-radius: 18px;
  background: linear-gradient(155deg, #fffaf3 0%, #f7e7d5 100%);
}

.stat-lead {
  background: linear-gradient(150deg, #fbe9d7 0%, #f4d8c2 100%);
}

.stat-label {
  margin: 0 0 8px;
  color: var(--ink-faint);
  font-size: var(--fs-caption);
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.stat-value {
  margin: 0;
  color: var(--ink);
  font-size: var(--fs-page);
  font-weight: 700;
  letter-spacing: -0.03em;
  line-height: 1.05;
}

.stat-hero {
  font-size: var(--fs-hero);
  color: var(--primary-deep);
}

.stat-note {
  margin: 8px 0 0;
  color: var(--ink-faint);
  font-size: var(--fs-caption);
  line-height: 1.5;
}

.lower {
  display: grid;
  grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr);
  gap: 32px;
}

.block-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
  color: var(--ink-soft);
  font-size: var(--fs-caption);
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

.bar {
  display: flex;
  height: 14px;
  overflow: hidden;
  border-radius: var(--radius-pill);
  background: var(--surface-2);
}

.bar-segment {
  height: 100%;
  transition: width 0.4s ease;
}

.legend {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-top: 18px;
}

.legend-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.legend-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.legend-label {
  flex: 1;
  color: var(--ink-soft);
  font-size: var(--fs-caption);
}

.legend-count {
  color: var(--ink);
  font-size: var(--fs-caption);
  font-weight: 600;
}

@media (max-width: 980px) {
  .stat-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .lower {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 560px) {
  .stat-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .stat-lead {
    grid-column: 1 / -1;
  }

  .stat {
    padding: 16px;
  }

  .stat-hero {
    font-size: var(--fs-page);
  }
}
</style>
