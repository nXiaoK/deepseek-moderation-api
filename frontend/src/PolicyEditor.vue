<script setup lang="ts">
import { computed, ref } from "vue";
import AppIcon from "./AppIcon.vue";
import {
  api,
  type Config,
  type Policy,
  type ModelChannel,
  type AuditResponse,
} from "./api";
import AttemptList from "./AttemptList.vue";
const config = defineModel<Config>({ required: true });
const name = defineModel<string>("name", { required: true });
const props = defineProps<{
  policy: Policy;
  channels: ModelChannel[];
  busy: boolean;
}>();
const emit = defineEmits<{ models: []; error: [string] }>();
const tab = ref("rules"),
  input = ref(""),
  selectedChannel = ref(""),
  testing = ref(false),
  result = ref<AuditResponse | null>(null),
  testError = ref("");
const channel = (id: string) => props.channels.find((c) => c.id === id);
const options = computed(() =>
  props.channels.filter(
    (c) => !config.value.channels.some((b) => b.channel_id === c.id),
  ),
);
const addID = ref("");
function add() {
  if (!addID.value) return;
  config.value.channels.push({
    channel_id: addID.value,
    priority: 1,
    weight: 100,
    enabled: true,
  });
  addID.value = "";
}
async function test() {
  testing.value = true;
  result.value = null;
  testError.value = "";
  try {
    result.value = await api<AuditResponse>(
      `/admin/policies/${props.policy.id}/test`,
      "POST",
      {
        input: input.value,
        config: config.value,
        channel_id: selectedChannel.value,
      },
    );
  } catch (e) {
    testError.value = e instanceof Error ? e.message : "试跑失败";
    emit("error", testError.value);
  } finally {
    testing.value = false;
  }
}
</script>
<template>
  <div class="tabs" role="tablist" aria-label="策略配置">
    <button
      v-for="t in [
        ['rules', '审核规则'],
        ['routing', '模型调度'],
        ['test', '审核试跑'],
      ]"
      :key="t[0]"
      role="tab"
      :aria-selected="tab === t[0]"
      :class="{ active: tab === t[0] }"
      @click="tab = t[0]"
    >
      {{ t[1] }}
    </button>
  </div>
  <section v-if="tab === 'rules'" class="panel config-panel">
    <div class="panel-heading">
      <div>
        <h2>审核规则</h2>
        <p class="muted small">所有绑定模型使用相同提示词与判定阈值。</p>
      </div>
    </div>
    <div class="form-grid">
      <label
        >策略名称<input
          v-model="name"
          :disabled="busy"
          maxlength="200" /></label
      ><label
        >拦截阈值<input
          v-model.number="config.threshold"
          type="number"
          min="0"
          max="1"
          step="0.01"
          :disabled="busy"
        /><small
          >评分 ≥ 阈值时命中。sub2api 接收命中 100% / 未命中
          0%，真实评分在本后台查看。</small
        ></label
      >
    </div>
    <label
      >审核提示词<textarea
        v-model="config.prompt"
        class="prompt-editor"
        spellcheck="false"
        :disabled="busy"
      />
    </label>
    <div class="contract">
      <code>{"confidence": 0.00, "reason": "..."}</code
      ><span class="muted small"
        >评分 0～1，原因最多 80 字；有效结果直接返回。</span
      >
    </div>
    <div class="form-grid">
      <label
        >结果缓存（秒）<input
          v-model.number="config.result_cache_ttl_seconds"
          type="number"
          min="0"
          max="3600"
          :disabled="busy"
        /><small>0 为关闭；按调用方、当前规则和模型通道隔离。</small></label
      ><label
        >审核记录保留（天）<input
          v-model.number="config.retention_days"
          type="number"
          min="1"
          max="365"
          :disabled="busy"
      /></label>
    </div>
    <label class="check-row"
      ><input
        v-model="config.store_input"
        type="checkbox"
        :disabled="busy"
      /><span
        >加密保存输入原文<small
          >保存并生效后，新请求（含缓存命中）会保存完整输入，管理员可在审核记录中查看；旧记录不会补存，到期自动清理。</small
        ></span
      ></label
    >
  </section>
  <section v-if="tab === 'routing'" class="panel config-panel">
    <div class="panel-heading">
      <div>
        <h2>模型调度</h2>
        <p class="muted small">
          数字越小越优先；同级按权重分流，同层不可用时切换到下一级。
        </p>
      </div>
      <button @click="emit('models')">
        <AppIcon name="cpu" :size="16" />管理审核模型
      </button>
    </div>
    <div class="row">
      <label
        >添加模型通道<select v-model="addID">
          <option value="">请选择</option>
          <option v-for="c in options" :key="c.id" :value="c.id">
            {{ c.name }} · {{ c.model }}
          </option>
        </select></label
      ><button @click="add" :disabled="busy || !addID">
        <AppIcon name="plus" :size="16" />添加
      </button>
    </div>
    <div v-if="!config.channels.length" class="empty">
      尚未绑定模型通道。先在“审核模型”添加通道，再加入此策略。
    </div>
    <div v-else class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>参与调度</th>
            <th>通道 / 模型</th>
            <th>优先级</th>
            <th>权重</th>
            <th>状态</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(b, i) in config.channels" :key="b.channel_id">
            <td>
              <input
                v-model="b.enabled"
                type="checkbox"
                :disabled="busy"
                aria-label="参与调度"
              />
            </td>
            <td>
              <strong>{{ channel(b.channel_id)?.name || "通道已删除" }}</strong
              ><small>{{ channel(b.channel_id)?.model }}</small>
            </td>
            <td>
              <input
                v-model.number="b.priority"
                class="compact-input"
                type="number"
                min="1"
                max="100"
                :disabled="busy"
                aria-label="优先级"
              />
            </td>
            <td>
              <input
                v-model.number="b.weight"
                class="compact-input"
                type="number"
                min="1"
                max="1000"
                :disabled="busy"
                aria-label="权重"
              />
            </td>
            <td>
              {{
                !channel(b.channel_id)?.enabled
                  ? "已停用"
                  : !channel(b.channel_id)?.credential_active
                    ? "密钥不可用"
                    : "已启用"
              }}
            </td>
            <td>
              <button
                class="icon-button danger-button"
                title="移除通道"
                :aria-label="
                  '移除 ' + (channel(b.channel_id)?.name || b.channel_id)
                "
                @click="config.channels.splice(i, 1)"
                :disabled="busy"
              >
                <AppIcon name="trash" :size="16" />
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p class="hint">
      例如：两个主用通道设为优先级 1、权重 70 和 30；备用通道设为优先级
      2。比例按大量请求统计，不保证每批请求完全一致。
    </p>
    <div class="form-grid">
      <label
        >总调用时限（毫秒）<input
          v-model.number="config.total_timeout_ms"
          type="number"
          min="1000"
          max="25000"
          step="1000"
          :disabled="busy" /></label
      ><label
        >最多调用次数<input
          v-model.number="config.max_attempts"
          type="number"
          min="1"
          max="5"
          :disabled="busy"
        /><small>包含首次调用。每个通道最多尝试一次。</small></label
      >
    </div>
    <p class="muted small">
      sub2api HTTP 超时需覆盖总调用时限及约 5 秒结算时间，重试次数建议为
      0。全部模型失败时返回审核错误，由 sub2api 的失败策略处理。
    </p>
  </section>
  <section v-if="tab === 'test'" class="panel config-panel">
    <div class="panel-heading">
      <div>
        <h2>审核试跑</h2>
        <p class="muted small">
          直接验证当前编辑内容，不保存正式配置；真实模型调用会产生费用。
        </p>
      </div>
    </div>
    <label
      >调用方式<select v-model="selectedChannel" :disabled="testing">
        <option value="">按调度规则</option>
        <option
          v-for="b in config.channels.filter((b) => b.enabled)"
          :key="b.channel_id"
          :value="b.channel_id"
        >
          仅调用 {{ channel(b.channel_id)?.name || b.channel_id }}
        </option>
      </select></label
    >
    <label
      >待审核内容<textarea
        v-model="input"
        rows="7"
        :disabled="testing"
        placeholder="输入测试样本…"
      />
    </label>
    <button
      class="primary"
      @click="test"
      :disabled="testing || busy || !input.trim()"
    >
      <AppIcon
        :name="testing ? 'refresh' : 'play'"
        :class="{ spinning: testing }"
        :size="16"
      />{{ testing ? "正在审核…" : "运行审核" }}
    </button>
    <p class="hint">
      试跑不使用正式结果缓存，不触发 sub2api 封禁或通知。指定通道时只调用一次。
    </p>
    <div v-if="testError" class="banner error" role="alert">
      {{ testError }}；可在“审核记录”查看失败过程。
    </div>
    <div v-if="result" class="test-result">
      <span
        class="badge"
        :class="result.results[0].flagged ? 'red' : 'green'"
        >{{ result.results[0].flagged ? "命中策略" : "未命中" }}</span
      >
      <div class="score">
        {{ result.results[0].audit.confidence.toFixed(2)
        }}<span>模型评分 · 阈值 {{ result.results[0].audit.threshold }}</span>
      </div>
      <p>{{ result.results[0].audit.reason }}</p>
      <p class="muted">
        {{ result.actual_model }} · {{ result.latency_ms }} ms · 实际调用
        {{ result.attempt_count }} 次 ·
        {{
          result.cost?.amount_cny != null
            ? "¥" + result.cost.amount_cny
            : "费用待核对"
        }}
      </p>
      <AttemptList :attempts="result.attempts || []" />
    </div>
  </section>
</template>
