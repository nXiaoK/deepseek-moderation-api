<script setup lang="ts">
import type { AuditAttempt } from "./api";
import { auditError, formatModelOutput } from "./auditDisplay";
defineProps<{ attempts: AuditAttempt[] }>();
</script>
<template>
  <div v-if="attempts.length" class="attempt-list">
    <h3>调用过程</h3>
    <div v-for="(a, i) in attempts" :key="a.id" class="attempt-item">
      <div class="attempt-summary">
        <div>
          <strong
            >{{ i + 1 }}. {{ a.channel_name }} ·
            {{ a.usage.actual_model || a.model }}</strong
          >
          <p class="small" :class="a.error_code ? 'attempt-error' : 'muted'">
            {{
              a.cache_hit
                ? "命中结果缓存"
                : a.error_code
                  ? auditError(a.error_code, a.error_message)
                  : a.sent
                    ? "审核成功"
                    : "未发送请求"
            }}
          </p>
          <p class="muted small">
            {{ a.latency_ms }} ms ·
            {{ a.usage.reported ? a.usage.total_tokens + " tokens" : "用量未知"
            }}<template v-if="a.error_code">
              · 错误码：{{ a.error_code }}</template
            >
          </p>
        </div>
        <span class="attempt-cost">{{
          a.cost?.amount_cny != null ? "¥" + a.cost.amount_cny : "费用待核对"
        }}</span>
      </div>
      <details v-if="a.model_output" class="attempt-output">
        <summary>查看此次模型返回</summary>
        <p v-if="a.error_code" class="muted small">
          当时保存的模型原始输出（已脱敏）；审核失败时，原始评分不作为有效判定。
        </p>
        <pre>{{ formatModelOutput(a.model_output) }}</pre>
      </details>
      <p v-else-if="a.error_code" class="muted small">
        此次调用没有可显示的模型返回内容。
      </p>
    </div>
  </div>
</template>
