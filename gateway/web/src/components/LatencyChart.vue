<script setup>
import { computed } from 'vue'
import { statusTone, TONE_HEX } from '../lib/format'

const props = defineProps({
  points: {
    type: Array,
    default: () => [],
  },
})

const WIDTH = 640
const HEIGHT = 190
const PAD_X = 10
const PAD_TOP = 14
const PAD_BOTTOM = 20

const maxValue = computed(() => {
  const longest = Math.max(0, ...props.points.map((point) => point.duration))
  return Math.max(100, Math.ceil(longest / 100) * 100)
})

const coords = computed(() => {
  const points = props.points

  if (!points.length) return []

  const step =
    points.length > 1 ? (WIDTH - PAD_X * 2) / (points.length - 1) : 0

  return points.map((point, index) => ({
    x: points.length === 1 ? WIDTH / 2 : PAD_X + index * step,
    y:
      PAD_TOP +
      (1 - Math.min(1, point.duration / maxValue.value)) *
        (HEIGHT - PAD_TOP - PAD_BOTTOM),
    color: TONE_HEX[statusTone(point.status)],
  }))
})

const linePath = computed(() =>
  coords.value
    .map(
      (coord, index) =>
        `${index === 0 ? 'M' : 'L'}${coord.x.toFixed(1)},${coord.y.toFixed(1)}`,
    )
    .join(' '),
)

const areaPath = computed(() => {
  if (!coords.value.length) return ''

  const first = coords.value[0]
  const last = coords.value[coords.value.length - 1]

  return `${linePath.value} L${last.x.toFixed(1)},${
    HEIGHT - PAD_BOTTOM
  } L${first.x.toFixed(1)},${HEIGHT - PAD_BOTTOM} Z`
})

const gridLines = computed(() =>
  [0.25, 0.5, 0.75].map(
    (ratio) => PAD_TOP + ratio * (HEIGHT - PAD_TOP - PAD_BOTTOM),
  ),
)
</script>

<template>
  <div class="chart">
    <div v-if="!points.length" class="chart-empty">
      还没有延迟采样。发送请求后这里会出现折线。
    </div>

    <template v-else>
      <svg
        class="chart-svg"
        :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
        role="img"
        aria-label="延迟趋势"
      >
        <defs>
          <linearGradient id="latency-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="#d97545" stop-opacity="0.24" />
            <stop offset="100%" stop-color="#d97545" stop-opacity="0" />
          </linearGradient>
        </defs>

        <line
          v-for="y in gridLines"
          :key="y"
          :x1="PAD_X"
          :x2="WIDTH - PAD_X"
          :y1="y"
          :y2="y"
          class="grid-line"
        />

        <path :d="areaPath" fill="url(#latency-fill)" />
        <path :d="linePath" class="line" />

        <circle
          v-for="(coord, index) in coords"
          :key="index"
          :cx="coord.x"
          :cy="coord.y"
          r="3.5"
          :fill="coord.color"
          class="point"
        />
      </svg>

      <div class="axis">
        <span class="mono">0 ms</span>
        <span class="mono">{{ maxValue }} ms</span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.chart {
  min-height: 190px;
}

.chart-empty {
  display: grid;
  min-height: 190px;
  padding: 24px;
  place-items: center;
  border-radius: var(--radius-md);
  background: var(--surface-2);
  color: var(--ink-faint);
  font-size: var(--fs-caption);
  text-align: center;
}

.chart-svg {
  display: block;
  width: 100%;
  height: auto;
}

.grid-line {
  stroke: #ead6c2;
  stroke-width: 1;
  stroke-dasharray: 3 6;
}

.line {
  fill: none;
  stroke: var(--primary);
  stroke-width: 2.5;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.point {
  opacity: 0.9;
}

.axis {
  display: flex;
  justify-content: space-between;
  margin-top: 4px;
  color: var(--ink-faint);
  font-size: var(--fs-caption);
}
</style>
