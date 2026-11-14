<script setup>
import { computed, ref } from 'vue'
import {
  authState,
  countdown,
  expiresLabel,
  isAuthenticated,
  issuedLabel,
  remainingRatio,
  signIn,
  signOut,
  tokenJTI,
  tokenLevel,
  userID,
} from '../composables/useAuth'

const username = ref('')
const password = ref('')

const circumference = 2 * Math.PI * 26

const dashOffset = computed(
  () => circumference * (1 - remainingRatio.value),
)

async function submit() {
  await signIn(username.value, password.value)
  password.value = ''
}
</script>

<template>
  <article class="card">
    <div class="card-head">
      <div>
        <p class="eyebrow">Session</p>
        <h2 class="card-title">认证</h2>
      </div>
      <span
        v-if="isAuthenticated"
        class="chip"
        :class="`chip-${tokenLevel}`"
      >
        {{ countdown }}
      </span>
    </div>

    <form v-if="!isAuthenticated" class="login" @submit.prevent="submit">
      <p class="hint">
        账号来自网关启动时的环境变量。Token 只保存在当前标签页，关闭即清除。
      </p>

      <label class="field">
        <span class="field-label">用户名</span>
        <input
          v-model="username"
          class="input"
          name="username"
          autocomplete="username"
          placeholder="GATEWAY_ADMIN_USERNAME"
        />
      </label>

      <label class="field">
        <span class="field-label">密码</span>
        <input
          v-model="password"
          class="input"
          type="password"
          name="password"
          autocomplete="current-password"
          placeholder="GATEWAY_ADMIN_PASSWORD"
        />
      </label>

      <p v-if="authState.error" class="form-error" role="alert">
        {{ authState.error }}
      </p>

      <button
        class="btn btn-primary btn-block"
        type="submit"
        :disabled="authState.pending"
      >
        {{ authState.pending ? '正在登录…' : '登录并获取 Token' }}
      </button>
    </form>

    <div v-else class="identity">
      <div class="identity-main">
        <span class="avatar" aria-hidden="true">G</span>
        <div class="identity-copy">
          <p class="identity-label">当前用户</p>
          <p class="identity-id mono">{{ userID }}</p>
        </div>

        <div class="ring-wrap">
          <svg
            class="ring"
            viewBox="0 0 64 64"
            role="img"
            :aria-label="`Token 剩余 ${countdown}`"
          >
            <circle class="ring-track" cx="32" cy="32" r="26" />
            <circle
              class="ring-value"
              :class="`stroke-${tokenLevel}`"
              cx="32"
              cy="32"
              r="26"
              :stroke-dasharray="circumference"
              :stroke-dashoffset="dashOffset"
            />
          </svg>
          <span class="ring-label mono">{{ countdown }}</span>
        </div>
      </div>

      <dl class="facts">
        <div class="fact">
          <dt>签发时间</dt>
          <dd class="mono">{{ issuedLabel }}</dd>
        </div>
        <div class="fact">
          <dt>到期时间</dt>
          <dd class="mono">{{ expiresLabel }}</dd>
        </div>
        <div class="fact">
          <dt>JTI</dt>
          <dd class="mono ellipsis" :title="tokenJTI">{{ tokenJTI }}</dd>
        </div>
      </dl>

      <button class="btn btn-ghost btn-block" type="button" @click="signOut">
        退出并清除 Token
      </button>
    </div>
  </article>
</template>

<style scoped>
.login {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.hint {
  margin: 0;
  color: var(--ink-faint);
  font-size: var(--fs-caption);
  line-height: 1.6;
}

.form-error {
  margin: 0;
  padding: 10px 14px;
  border-radius: 12px;
  background: var(--danger-soft);
  color: var(--danger);
  font-size: var(--fs-caption);
  font-weight: 600;
  line-height: 1.5;
}

.identity {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.identity-main {
  display: flex;
  align-items: center;
  gap: 14px;
}

.avatar {
  display: grid;
  flex: 0 0 auto;
  width: 48px;
  height: 48px;
  place-items: center;
  border-radius: 50%;
  background: var(--primary-soft);
  color: var(--primary-deep);
  font-family: var(--font-display);
  font-size: var(--fs-title);
  line-height: 1;
}

.identity-copy {
  min-width: 0;
  flex: 1;
}

.identity-label {
  margin: 0 0 2px;
  color: var(--ink-faint);
  font-size: var(--fs-caption);
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.identity-id {
  overflow: hidden;
  margin: 0;
  font-size: var(--fs-body);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ring-wrap {
  position: relative;
  display: grid;
  flex: 0 0 auto;
  width: 72px;
  height: 72px;
  place-items: center;
}

.ring {
  width: 72px;
  height: 72px;
  transform: rotate(-90deg);
}

.ring-track,
.ring-value {
  fill: none;
  stroke-width: 5;
}

.ring-track {
  stroke: var(--surface-2);
}

.ring-value {
  stroke-linecap: round;
  transition:
    stroke-dashoffset 0.6s linear,
    stroke 0.3s ease;
}

.stroke-success {
  stroke: var(--success-bright);
}

.stroke-warning {
  stroke: var(--honey);
}

.stroke-danger {
  stroke: var(--danger);
}

.stroke-idle {
  stroke: var(--ink-faint);
}

.ring-label {
  position: absolute;
  color: var(--ink-soft);
  font-size: var(--fs-caption);
  font-weight: 600;
}

.facts {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin: 0;
}

.fact {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  padding-bottom: 10px;
  box-shadow: inset 0 -1px 0 rgba(217, 117, 69, 0.12);
}

.fact:last-child {
  padding-bottom: 0;
  box-shadow: none;
}

.fact dt {
  flex: 0 0 auto;
  color: var(--ink-faint);
  font-size: var(--fs-caption);
}

.fact dd {
  overflow: hidden;
  margin: 0;
  color: var(--ink-soft);
  font-size: var(--fs-caption);
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ellipsis {
  max-width: 180px;
}
</style>
