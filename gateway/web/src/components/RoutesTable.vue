<script setup>
import { computed } from 'vue'
import { isAuthenticated } from '../composables/useAuth'
import { gatewayState } from '../composables/useGateway'

const routes = computed(() => gatewayState.routes)

function methodClass(method) {
  if (method === 'POST') return 'chip-secondary'
  if (method === 'GET') return 'chip-success'
  return 'chip-warning'
}

function shortUpstream(route) {
  if (!route.upstream) return '网关本地'
  return route.upstream.replace(/^https?:\/\//, '')
}
</script>

<template>
  <article class="card">
    <div class="card-head">
      <div>
        <p class="eyebrow">Routes</p>
        <h2 class="card-title">路由与上游</h2>
      </div>
      <span v-if="routes.length" class="card-note">
        {{ routes.length }} 条
      </span>
    </div>

    <div v-if="routes.length" class="scroll-area routes-scroll">
      <table class="data-table routes-table">
        <thead>
          <tr>
            <th>路径</th>
            <th>方法</th>
            <th>上游</th>
            <th>认证</th>
            <th>超时</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="route in routes"
            :key="`${route.method}-${route.path}`"
          >
            <td>
              <span class="route-path mono" :title="route.description">
                {{ route.path }}
              </span>
            </td>
            <td>
              <span class="chip" :class="methodClass(route.method)">
                {{ route.method }}
              </span>
            </td>
            <td>
              <span class="mono route-upstream">
                {{ shortUpstream(route) }}
              </span>
            </td>
            <td>
              <span
                class="route-auth"
                :class="route.auth ? 'auth-yes' : 'auth-no'"
              >
                {{ route.auth ? '需 Token' : '公开' }}
              </span>
            </td>
            <td>
              <span class="mono route-timeout">
                {{ route.timeout_ms ? `${route.timeout_ms / 1000}s` : '—' }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else-if="!isAuthenticated" class="empty compact">
      <p class="empty-title">登录后读取路由表</p>
      <p class="empty-note">
        /api/routes 需要 JWT，返回网关当前代理的上游与超时配置。
      </p>
    </div>

    <div v-else class="empty compact">
      <p class="empty-title">
        {{ gatewayState.routesLoading ? '正在读取路由表…' : '路由表暂不可用' }}
      </p>
      <p v-if="gatewayState.routesError" class="empty-note">
        {{ gatewayState.routesError }}
      </p>
    </div>
  </article>
</template>

<style scoped>
.routes-scroll {
  max-height: 360px;
}

.routes-table {
  font-size: var(--fs-caption);
}

.routes-table th:first-child,
.routes-table td:first-child {
  padding-left: 4px;
}

.routes-table th:last-child,
.routes-table td:last-child {
  padding-right: 4px;
}

.route-path {
  display: inline-block;
  max-width: 150px;
  overflow: hidden;
  color: var(--ink);
  font-size: var(--fs-caption);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.route-upstream {
  color: var(--ink-soft);
  font-size: var(--fs-caption);
}

.route-auth {
  font-size: var(--fs-caption);
  font-weight: 600;
  white-space: nowrap;
}

.auth-yes {
  color: var(--primary-deep);
}

.auth-no {
  color: var(--ink-faint);
}

.route-timeout {
  color: var(--ink-soft);
  font-size: var(--fs-caption);
}

.compact {
  padding: 28px 20px;
}

@media (max-width: 640px) {
  .routes-table th:nth-child(3),
  .routes-table td:nth-child(3),
  .routes-table th:nth-child(5),
  .routes-table td:nth-child(5) {
    display: none;
  }
}
</style>
