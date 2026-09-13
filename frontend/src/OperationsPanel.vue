<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { api, ignoreAPIError, type RuntimeLimits } from "./api";
import AppIcon from "./AppIcon.vue";
interface Report {
  uptime_seconds: number;
  runtime: RuntimeLimits;
  active_requests: number;
  request_body_bytes: number;
  model_in_flight: number;
  trial_in_flight: number;
  migration_version: number;
  pending_costs: number;
  estimated_costs: number;
  running_evaluations: number;
  database: {
    open: number;
    in_use: number;
    idle: number;
    limit: number;
    wait_count: number;
    wait_ms: number;
  };
  alerts: { id: string; severity: string; message: string; page: string }[];
}
const emit = defineEmits<{ navigate: [page: string] }>();
const report = ref<Report | null>(null),
  busy = ref(false),
  error = ref("");
const abort = new AbortController();
let timer: ReturnType<typeof setInterval> | undefined;
const uptime = (seconds: number) =>
  seconds >= 86400
    ? Math.floor(seconds / 86400) +
      " 天 " +
      Math.floor((seconds % 86400) / 3600) +
      " 小时"
    : seconds >= 3600
      ? Math.floor(seconds / 3600) +
        " 小时 " +
        Math.floor((seconds % 3600) / 60) +
        " 分"
      : Math.floor(seconds / 60) + " 分钟";
async function load() {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    report.value = await api<Report>(
      "/admin/operations",
      "GET",
      undefined,
      abort.signal,
    );
  } catch (e) {
    if (!ignoreAPIError(e))
      error.value = e instanceof Error ? e.message : "运行状态加载失败";
  } finally {
    busy.value = false;
  }
}
onMounted(() => {
  load();
  timer = setInterval(() => {
    if (!document.hidden) load();
  }, 15000);
});
onBeforeUnmount(() => {
  abort.abort();
  if (timer) clearInterval(timer);
});
</script>
<template>
  <section class="panel operations-panel">
    <div class="panel-heading">
      <div>
        <h2>运行状态</h2>
        <p v-if="report" class="muted small">
          运行 {{ uptime(report.uptime_seconds) }} · 数据库迁移 #{{
            report.migration_version
          }}
        </p>
      </div>
      <button
        class="icon-button"
        title="刷新运行状态"
        aria-label="刷新运行状态"
        :disabled="busy"
        @click="load"
      >
        <AppIcon name="refresh" :size="16" :class="{ spinning: busy }" />
      </button>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <template v-if="report">
      <div class="operation-metrics">
        <div>
          <span>模型并发</span
          ><strong
            >{{ report.model_in_flight }}
            <small>/ {{ report.runtime.model_concurrency }}</small></strong
          >
        </div>
        <div>
          <span>在途请求</span
          ><strong
            >{{ report.active_requests }}
            <small>/ {{ report.runtime.request_concurrency }}</small></strong
          >
        </div>
        <div>
          <span>请求体预算</span
          ><strong
            >{{ (report.request_body_bytes / 1024 / 1024).toFixed(1) }}
            <small>/ {{ report.runtime.request_body_mib }} MiB</small></strong
          >
        </div>
        <div>
          <span>试跑并发</span
          ><strong
            >{{ report.trial_in_flight }}
            <small>/ {{ report.runtime.trial_concurrency }}</small></strong
          >
        </div>
      </div>
      <div class="operation-details">
        <span
          >数据库连接 {{ report.database.in_use }} 使用中 /
          {{ report.database.open }} 已打开 /
          {{ report.database.limit }} 上限</span
        >
        <span
          >连接池累计等待 {{ report.database.wait_count }} 次 ·
          {{ report.database.wait_ms }} ms</span
        >
        <span
          >待核对 {{ report.pending_costs }} 笔 · 估算
          {{ report.estimated_costs }} 笔</span
        >
        <span
          >运行中评测 {{ report.running_evaluations }} 项 · 每次最多
          {{ report.runtime.max_images }} 张图片</span
        >
      </div>
      <div class="operational-alerts">
        <div
          v-for="item in report.alerts"
          :key="item.id"
          class="operational-alert"
        >
          <span
            class="badge"
            :class="
              item.severity === 'critical'
                ? 'red'
                : item.severity === 'warning'
                  ? 'amber'
                  : 'gray'
            "
            >{{
              item.severity === "critical"
                ? "不可用"
                : item.severity === "warning"
                  ? "需关注"
                  : "待验证"
            }}</span
          ><span>{{ item.message }}</span
          ><button
            class="icon-button"
            :title="'查看' + item.message"
            :aria-label="'查看' + item.message"
            @click="emit('navigate', item.page)"
          >
            <AppIcon name="next" :size="15" />
          </button>
        </div>
        <p v-if="!report.alerts.length" class="muted small">
          <AppIcon name="check" :size="14" /> 当前没有运行告警
        </p>
      </div>
    </template>
  </section>
</template>
<style scoped>
.operation-metrics {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 20px;
  padding: 10px 0 22px;
}
.operation-metrics > div {
  min-width: 0;
}
.operation-metrics span {
  font-size: 11px;
  color: var(--muted);
}
.operation-metrics strong {
  display: block;
  font-size: 24px;
  font-weight: 550;
  color: #46536b;
  overflow-wrap: anywhere;
}
.operation-metrics small {
  font-size: 11px;
  font-weight: 400;
  color: var(--muted);
}
.operation-details {
  display: flex;
  flex-wrap: wrap;
  gap: 9px 24px;
  color: var(--muted);
  font-size: 11px;
  padding: 15px 0;
  border-top: 1px solid var(--border);
}
.operational-alert {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 11px 0;
  border-top: 1px solid var(--border);
  font-size: 12px;
}
.operational-alert > span:nth-child(2) {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
}
.operational-alerts > p {
  margin: 12px 0;
}
@media (max-width: 760px) {
  .operation-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 16px;
  }
}
</style>
