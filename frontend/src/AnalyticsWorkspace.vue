<script setup lang="ts">
import { defineAsyncComponent, ref } from "vue";
import type { AnalysisLogFilter, ClientKey, ModelChannel, Policy } from "./api";

const AnalyticsPanel = defineAsyncComponent(
  () => import("./AnalyticsPanel.vue"),
);
const InputRankingPanel = defineAsyncComponent(
  () => import("./InputRankingPanel.vue"),
);
defineProps<{
  policies: Policy[];
  clients: ClientKey[];
  channels: ModelChannel[];
}>();
const emit = defineEmits<{
  logs: [filter: AnalysisLogFilter];
  unauthorized: [];
}>();
const tab = ref("calls");
</script>

<template>
  <div class="tabs" role="tablist" aria-label="数据分析视图">
    <button
      role="tab"
      :aria-selected="tab === 'calls'"
      :class="{ active: tab === 'calls' }"
      @click="tab = 'calls'"
    >
      调用统计
    </button>
    <button
      role="tab"
      :aria-selected="tab === 'inputs'"
      :class="{ active: tab === 'inputs' }"
      @click="tab = 'inputs'"
    >
      输入重复排行
    </button>
  </div>
  <KeepAlive>
    <AnalyticsPanel
      v-if="tab === 'calls'"
      :policies="policies"
      :clients="clients"
      :channels="channels"
      @logs="emit('logs', $event)"
      @unauthorized="emit('unauthorized')"
    />
    <InputRankingPanel
      v-else
      :policies="policies"
      :clients="clients"
      @logs="emit('logs', $event)"
    />
  </KeepAlive>
</template>
