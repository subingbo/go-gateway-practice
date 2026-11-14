<script setup>
import { computed } from 'vue'
import { countdown, isAuthenticated, tokenLevel } from '../composables/useAuth'
import { gatewayState } from '../composables/useGateway'

const gatewayTone = computed(() => {
  if (gatewayState.health === 'online') return 'success'
  if (gatewayState.health === 'offline') return 'danger'
  return 'idle'
})

const gatewayLabel = computed(() => {
  if (gatewayState.health === 'online') return '网关在线'
  if (gatewayState.health === 'offline') return '网关离线'
  return '检查中'
})
</script>

<template>
  <header class="hero">
    <div class="hero-copy">
      <p class="eyebrow">Go Gateway Practice</p>
      <h1>网关控制台</h1>
      <p class="lede">
        签发一枚 JWT，看它穿过认证、超时与反向代理，最后落在上游服务的日志里。
      </p>
    </div>

    <div class="hero-meta">
      <span class="pill" :class="`tone-${gatewayTone}`">
        <span class="dot" />
        {{ gatewayLabel }}
      </span>

      <span v-if="isAuthenticated" class="pill">
        <span class="pill-label">Token</span>
        <span class="mono" :class="`text-${tokenLevel}`">{{ countdown }}</span>
      </span>

      <span v-else class="pill tone-idle">
        <span class="dot" />
        未登录
      </span>

      <a
        class="pill pill-link"
        href="/health"
        target="_blank"
        rel="noreferrer"
      >
        /health
      </a>
    </div>
  </header>
</template>

<style scoped>
.hero {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  justify-content: space-between;
  gap: 24px;
  margin-bottom: 32px;
  padding: 8px 4px 0;
}

.hero-copy {
  max-width: 720px;
}

.eyebrow {
  margin: 0 0 10px;
  color: var(--secondary);
  font-size: var(--fs-caption);
  font-weight: 600;
  letter-spacing: 0.18em;
  line-height: var(--lh-caption);
  text-transform: uppercase;
}

h1 {
  margin: 0;
  font-family: var(--font-display);
  font-size: var(--fs-page);
  font-weight: 500;
  letter-spacing: -0.02em;
  line-height: var(--lh-page);
}

.lede {
  max-width: 720px;
  margin: 14px 0 0;
  color: var(--ink-soft);
  font-size: var(--fs-body);
}

.hero-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
}

.pill {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 9px 16px;
  border-radius: var(--radius-pill);
  background: rgba(255, 248, 239, 0.82);
  box-shadow: 0 10px 30px -24px rgba(120, 63, 38, 0.6);
  color: var(--ink-soft);
  font-size: var(--fs-caption);
  font-weight: 600;
  line-height: 1.4;
  backdrop-filter: blur(8px);
}

.pill-label {
  color: var(--ink-faint);
  letter-spacing: 0.12em;
  text-transform: uppercase;
}

.pill-link {
  color: var(--primary-deep);
  font-family: var(--font-mono);
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
  background: var(--success);
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

@media (max-width: 640px) {
  .hero {
    align-items: flex-start;
  }
}
</style>
