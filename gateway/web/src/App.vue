<script setup>
import { onMounted } from 'vue'
import AppHeader from './components/AppHeader.vue'
import AuthCard from './components/AuthCard.vue'
import ServiceStatus from './components/ServiceStatus.vue'
import RequestBuilder from './components/RequestBuilder.vue'
import ResponsePanel from './components/ResponsePanel.vue'
import MetricsPanel from './components/MetricsPanel.vue'
import RoutesTable from './components/RoutesTable.vue'
import HistoryTable from './components/HistoryTable.vue'
import { startGatewayPolling } from './composables/useGateway'

onMounted(() => {
  startGatewayPolling()
})
</script>

<template>
  <div class="app-shell">
    <AppHeader />

    <main class="dashboard">
      <AuthCard class="span-4" />
      <ServiceStatus class="span-8" />
      <RequestBuilder class="span-4" />
      <ResponsePanel class="span-8" />
      <MetricsPanel class="span-12" />
      <RoutesTable class="span-5" />
      <HistoryTable class="span-7" />
    </main>

    <footer class="app-footer">
      <span>go-gateway-practice</span>
      <span class="mono">:8080</span>
      <span>指标保存在网关进程内，重启即清零；请求历史只存在于当前标签页。</span>
    </footer>
  </div>
</template>
