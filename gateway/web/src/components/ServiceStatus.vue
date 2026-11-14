<script setup>
import { computed } from 'vue'
import { services, gatewayState } from '../composables/useGateway'
import { formatTime } from '../lib/format'

const updatedLabel = computed(() =>
  gatewayState.lastUpdated ? formatTime(gatewayState.lastUpdated) : '',
)
</script>

<template>
  <article class="card">
    <div class="card-head">
      <div>
        <p class="eyebrow">Upstreams</p>
        <h2 class="card-title">服务状态</h2>
      </div>
      <p class="card-note">
        {{
          updatedLabel
            ? `全局指标更新于 ${updatedLabel}`
            : '登录后读取网关全局指标'
        }}
      </p>
    </div>

    <div class="service-grid">
      <section
        v-for="service in services"
        :key="service.key"
        class="service"
        :class="`tone-${service.tone}`"
      >
        <header class="service-head">
          <span class="dot" />
          <h3 class="service-name">{{ service.name }}</h3>
          <span class="mono service-endpoint">{{ service.endpoint }}</span>
        </header>
        <p class="service-state">{{ service.stateLabel }}</p>
        <p class="service-detail">{{ service.detail }}</p>
      </section>
    </div>

    <p v-if="gatewayState.statsError" class="service-error">
      {{ gatewayState.statsError }}
    </p>

    <ul class="rules">
      <li><span class="rule-key">认证</span><span>Bearer JWT</span></li>
      <li><span class="rule-key">超时</span><span>2 秒</span></li>
      <li><span class="rule-key">转发</span><span>剥离 /api 前缀</span></li>
    </ul>
  </article>
</template>

<style scoped>
.card {
  display: flex;
  flex-direction: column;
}

.service-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}

.service {
  padding: 20px;
  border-radius: 18px;
  background: linear-gradient(155deg, #fffaf3 0%, #f6e5d2 100%);
}

.service-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: currentColor;
}

.tone-success {
  color: var(--success);
}

.tone-success .dot {
  background: var(--success-bright);
  box-shadow: 0 0 0 4px rgba(95, 139, 76, 0.16);
}

.tone-warning {
  color: var(--warning);
}

.tone-warning .dot {
  background: var(--honey);
  box-shadow: 0 0 0 4px rgba(217, 164, 65, 0.18);
}

.tone-danger {
  color: var(--danger);
}

.tone-danger .dot {
  background: var(--danger);
  box-shadow: 0 0 0 4px rgba(192, 69, 58, 0.16);
}

.tone-idle {
  color: var(--ink-faint);
}

.tone-idle .dot {
  background: var(--ink-faint);
  box-shadow: 0 0 0 4px rgba(160, 132, 114, 0.14);
}

.service-name {
  margin: 0;
  color: var(--ink);
  font-size: var(--fs-caption);
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

.service-endpoint {
  margin-left: auto;
  color: var(--ink-faint);
  font-size: var(--fs-caption);
}

.service-state {
  margin: 14px 0 6px;
  color: currentColor;
  font-size: var(--fs-body);
  font-weight: 600;
  line-height: 1.3;
}

.service-detail {
  margin: 0;
  color: var(--ink-soft);
  font-size: var(--fs-caption);
  line-height: 1.6;
}

.service-error {
  margin: 16px 0 0;
  color: var(--danger);
  font-size: var(--fs-caption);
}

.rules {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 24px;
  margin: auto 0 0;
  padding: 18px 0 0;
  list-style: none;
  box-shadow: inset 0 1px 0 rgba(217, 117, 69, 0.12);
  color: var(--ink-faint);
  font-size: var(--fs-caption);
}

.rules li {
  display: flex;
  align-items: baseline;
  gap: 6px;
}

.rule-key {
  color: var(--ink-soft);
  font-weight: 600;
  letter-spacing: 0.06em;
}

@media (max-width: 720px) {
  .service-grid {
    grid-template-columns: 1fr;
  }
}
</style>
