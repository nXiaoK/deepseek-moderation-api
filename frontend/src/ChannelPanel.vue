<script setup lang="ts">
import { computed, ref } from "vue";
import AppIcon from "./AppIcon.vue";
import {
  api,
  ignoreAPIError,
  type ModelChannel,
  type Credential,
  type ChannelTestResult,
} from "./api";
const props = defineProps<{
  channels: ModelChannel[];
  credentials: Credential[];
  reload: () => Promise<void>;
}>();
const emit = defineEmits<{ credentials: []; error: [string] }>();
const empty = () => ({
  id: "",
  name: "",
  model: "deepseek-flash",
  credential_id: "",
  timeout_ms: 4000,
  max_tokens: 512,
  max_concurrency: 8,
  rpm: 0,
  text_only: false,
  enabled: true,
  expected_revision: 0,
});
const form = ref(empty()),
  busy = ref(false),
  notice = ref("");
const testingID = ref("");
const testResult = ref<ChannelTestResult | null>(null);
const testError = ref("");
const testName = ref("");
const affected = computed(
  () => props.channels.find((c) => c.id === form.value.id)?.policy_names || [],
);
const connection = computed(() =>
  props.credentials.find((c) => c.id === form.value.credential_id),
);
const health = (c: ModelChannel) =>
  !c.enabled
    ? "已停用"
    : !c.credential_active
      ? "密钥不可用"
      : c.health.status === "ready" && c.health.last_error_code
        ? "可重试"
        : c.health.status === "ready" && c.health.verified === false
          ? "尚未验证"
          : {
              ready: "可用",
              cooling: "冷却中",
              half_open: "等待恢复试探",
              configuration_error: "配置异常",
            }[c.health.status] || c.health.status;
function edit(c: ModelChannel) {
  form.value = {
    id: c.id,
    name: c.name,
    model: c.model,
    credential_id: c.credential_id,
    timeout_ms: c.timeout_ms,
    max_tokens: c.max_tokens,
    max_concurrency: c.max_concurrency,
    rpm: c.rpm || 0,
    text_only: c.text_only,
    enabled: c.enabled,
    expected_revision: c.revision,
  };
  notice.value = "";
}
async function work(fn: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  notice.value = "";
  emit("error", "");
  try {
    await fn();
  } catch (e) {
    if (ignoreAPIError(e)) return;
    emit("error", e instanceof Error ? e.message : "操作失败");
  } finally {
    busy.value = false;
  }
}
async function save() {
  await work(async () => {
    const { id, ...body } = form.value;
    await api(
      `/admin/model-channels${id ? "/" + id : ""}`,
      id ? "PUT" : "POST",
      body,
    );
    form.value = empty();
    notice.value = "模型通道已保存。前往审核策略绑定通道并试跑。";
    await props.reload();
  });
}
async function clear(c: ModelChannel) {
  await work(async () => {
    await api(`/admin/model-channels/${c.id}/cache/clear`, "POST", {});
    if (form.value.id === c.id) form.value = empty();
    notice.value = "结果缓存已失效，连接健康状态已重置。";
    await props.reload();
  });
}
async function testConnection(c: ModelChannel) {
  await work(async () => {
    testingID.value = c.id;
    testName.value = c.name;
    testResult.value = null;
    testError.value = "";
    try {
      testResult.value = await api<ChannelTestResult>(
        `/admin/model-channels/${c.id}/test`,
        "POST",
        { expected_revision: c.revision },
      );
    } catch (e) {
      if (ignoreAPIError(e)) return;
      testError.value =
        e instanceof Error ? e.message : "测试请求失败，请检查网络后重试";
    } finally {
      testingID.value = "";
    }
    await props.reload();
  });
}
async function remove(c: ModelChannel) {
  await work(async () => {
    await api(`/admin/model-channels/${c.id}`, "DELETE");
    if (form.value.id === c.id) form.value = empty();
    notice.value = "模型通道已删除。";
    await props.reload();
  });
}
</script>
<template>
  <div v-if="notice" class="banner success" role="status">{{ notice }}</div>
  <section class="panel config-panel">
    <div class="panel-heading">
      <div>
        <h2>模型通道</h2>
        <p class="muted small">
          每个通道绑定一个连接密钥和模型。并发和 RPM 上限由所有引用策略共享。
        </p>
      </div>
      <div class="row">
        <button
          class="icon-button"
          title="刷新状态"
          aria-label="刷新状态"
          @click="work(reload)"
          :disabled="busy"
        >
          <AppIcon name="refresh" :size="16" /></button
        ><button @click="emit('credentials')">
          <AppIcon name="link" :size="16" />管理连接密钥
        </button>
      </div>
    </div>
    <div v-if="!channels.length" class="empty">
      还没有审核模型，请在下方创建第一个通道。
    </div>
    <div v-else class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>通道 / 模型</th>
            <th>状态</th>
            <th>并发</th>
            <th>RPM 已用 / 上限</th>
            <th>调用 / 失败</th>
            <th>引用策略</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in channels" :key="c.id">
            <td>
              <strong>{{ c.name }}</strong
              ><small
                >{{ c.model }} ·
                {{ c.provider === "deepseek" ? "DeepSeek" : "Grok" }}</small
              >
              <small v-if="c.text_only">文本模型 · 自动忽略图片</small>
            </td>
            <td>
              <span
                class="badge"
                :class="
                  !c.enabled
                    ? 'gray'
                    : !c.credential_active ||
                        c.health.status === 'configuration_error'
                      ? 'red'
                      : c.health.status === 'ready'
                        ? c.health.verified === false
                          ? 'gray'
                          : c.health.last_error_code
                            ? 'amber'
                            : 'green'
                        : 'amber'
                "
                >{{ health(c) }}</span
              >
              <small v-if="c.health.last_success_at"
                >成功
                {{
                  new Date(c.health.last_success_at).toLocaleString("zh-CN", {
                    timeZone: "Asia/Shanghai",
                    hour12: false,
                  })
                }}</small
              >
              <small v-if="c.health.last_error_code">{{
                c.health.last_error_code
              }}</small>
            </td>
            <td>{{ c.health.in_flight }} / {{ c.max_concurrency }}</td>
            <td>{{ c.health.rpm_used || 0 }} / {{ c.rpm || "不限额" }}</td>
            <td>{{ c.health.calls }} / {{ c.health.failures }}</td>
            <td>{{ c.policy_names.join("、") || "未绑定" }}</td>
            <td>
              <div class="row">
                <button
                  :aria-label="'测试 ' + c.name"
                  @click="testConnection(c)"
                  :disabled="busy"
                >
                  <AppIcon name="play" :size="16" />{{
                    testingID === c.id ? "测试中…" : "测试"
                  }}
                </button>
                <button
                  class="icon-button"
                  title="编辑通道"
                  :aria-label="'编辑 ' + c.name"
                  @click="edit(c)"
                  :disabled="busy"
                >
                  <AppIcon name="edit" :size="16" /></button
                ><button
                  class="icon-button"
                  title="清缓存 / 重试连接"
                  :aria-label="'清缓存 / 重试连接 ' + c.name"
                  @click="clear(c)"
                  :disabled="busy"
                >
                  <AppIcon name="refresh" :size="16" /></button
                ><button
                  class="icon-button danger-button"
                  :title="
                    c.policy_names.length ? '被策略引用，无法删除' : '删除通道'
                  "
                  :aria-label="'删除 ' + c.name"
                  @click="remove(c)"
                  :disabled="busy || c.policy_names.length > 0"
                >
                  <AppIcon name="trash" :size="16" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p class="muted small">
      点击“测试”会按已保存配置发送一条固定文本，实际调用一次模型（可能产生上游费用），不使用缓存或切换其他通道；无需绑定策略，也可测试已停用的通道。这里只验证文本调用。
    </p>
    <p class="muted small">
      调用与失败统计从本服务进程启动开始累计；保存连接后重新学习健康状态。默认连续失败
      3 次冷却 30 分钟，可在“审核策略 →
      模型调度”中调整；单通道免除失败冷却，仍受并发和 RPM 限制。当前并发、RPM
      和健康协调适用于单进程部署。
    </p>
  </section>
  <section
    v-if="testingID || testResult || testError"
    class="panel channel-test-result"
    aria-label="模型测试结果"
    aria-live="polite"
  >
    <div class="panel-heading">
      <h2>模型测试 · {{ testName }}</h2>
      <span
        class="badge"
        :class="testingID ? 'gray' : testResult?.ok ? 'green' : 'red'"
        >{{
          testingID ? "测试中…" : testResult?.ok ? "测试成功" : "测试失败"
        }}</span
      >
    </div>
    <p v-if="testingID">
      正在按已保存的地址、接口类型和模型名发起请求，请等待通道超时时限内的结果。
    </p>
    <p v-if="testError" class="banner error" role="alert">{{ testError }}</p>
    <template v-if="testResult">
      <p>
        <strong>实际请求地址：</strong><code>{{ testResult.endpoint }}</code>
      </p>
      <p>
        模型：<code>{{ testResult.model }}</code> · 接口：{{
          testResult.api_format === "responses"
            ? "/responses"
            : "/chat/completions"
        }}
      </p>
      <p>
        耗时 {{ testResult.latency_ms }} ms · 超时
        {{ testResult.timeout_ms }} ms · 最大输出
        {{ testResult.max_tokens }} tokens ·
        {{
          testResult.http_status
            ? "HTTP " + testResult.http_status
            : "未收到 HTTP 响应"
        }}
      </p>
      <p v-if="!testResult.attempted" class="hint">请求尚未发送到上游。</p>
      <div v-if="!testResult.ok" class="banner error" role="alert">
        <strong>{{ testResult.error_code }}</strong>
        <p>{{ testResult.error_message }}</p>
        <p v-if="testResult.hint">建议：{{ testResult.hint }}</p>
      </div>
      <p v-if="testResult.assessment">
        模型回复：{{ testResult.assessment.reason }}（违规置信度
        {{ testResult.assessment.confidence }}）
      </p>
      <p v-if="testResult.usage.reported">
        用量：{{ testResult.usage.total_tokens }} tokens
      </p>
      <details v-if="testResult.model_output">
        <summary>查看模型输出</summary>
        <pre>{{ testResult.model_output }}</pre>
      </details>
    </template>
  </section>
  <section class="panel config-panel">
    <div class="panel-heading">
      <h2>{{ form.id ? "编辑模型通道" : "新增模型通道" }}</h2>
      <button v-if="form.id" @click="form = empty()" :disabled="busy">
        取消编辑
      </button>
    </div>
    <form @submit.prevent="save">
      <div class="form-grid">
        <label
          >通道名称<input
            v-model="form.name"
            required
            maxlength="200"
            :disabled="busy"
            placeholder="例如：DeepSeek 主用 A" /></label
        ><label
          >连接密钥<select
            v-model="form.credential_id"
            required
            :disabled="busy"
          >
            <option value="">请选择</option>
            <option v-for="c in credentials" :key="c.id" :value="c.id">
              {{ c.name }} · {{ c.provider === "deepseek" ? "DeepSeek" : "Grok"
              }}{{ c.active ? "" : "（已停用）" }}
            </option>
          </select></label
        ><label
          >模型名<input
            v-model="form.model"
            required
            maxlength="100"
            :disabled="busy"
          /><small>填写该连接实际支持的模型名称。</small></label
        ><label
          >单次超时（毫秒）<input
            v-model.number="form.timeout_ms"
            type="number"
            min="1000"
            max="30000"
            required
            :disabled="busy" /></label
        ><label
          >最大输出 tokens<input
            v-model.number="form.max_tokens"
            type="number"
            min="64"
            max="4096"
            required
            :disabled="busy" /></label
        ><label
          >并发上限<input
            v-model.number="form.max_concurrency"
            type="number"
            min="1"
            max="256"
            required
            :disabled="busy"
        /></label>
        <label>
          RPM（每分钟调用上限）
          <input
            v-model.number="form.rpm"
            type="number"
            min="0"
            max="100000"
            step="1"
            required
            :disabled="busy"
          />
          <small
            >0 表示不限额；按最近 60
            秒的调用统计，失败调用也计入，缓存命中不占用额度。</small
          >
        </label>
      </div>
      <p class="hint">
        同优先级按“剩余 RPM ×
        权重”分流，额度用完后切换其他通道；不限额通道按同级最高剩余额度参与分流。
        只有一个通道时也遵守 RPM
        上限。额度由正式审核、试跑和评测共享，重启服务后重置。
      </p>
      <p v-if="connection" class="hint">
        连接地址：{{ connection.base_url
        }}<template
          v-if="
            connection.provider === 'deepseek' &&
            connection.base_url !== 'https://api.deepseek.com'
          "
          ><br />第三方接口按配置模型名对应的单价计费；配置单价后，文本请求可参与人民币预算。</template
        ><template v-if="connection.provider === 'grok_via_sub2api'"
          ><br />Grok 使用连接中选择的 API
          接口类型。配置模型单价后，文本请求可参与人民币预算；无单价的调用费用需核对。</template
        >
      </p>
      <label class="check-row">
        <input v-model="form.text_only" type="checkbox" :disabled="busy" />
        <span>此模型为文本模型</span>
      </label>
      <p class="hint">
        勾选后自动忽略图片，只审核文本；没有文本时不会调用此通道。未勾选时，将文本和图片一起发送给模型，请使用支持图片的模型。含图请求体最多
        32 MiB。
      </p>
      <p v-if="!form.text_only" class="hint">
        图片费用暂不支持预估，设置了日/月预算的访问密钥暂不能使用此通道审核图片。
      </p>
      <label class="check-row"
        ><input v-model="form.enabled" type="checkbox" :disabled="busy" /><span
          >启用通道</span
        ></label
      >
      <p v-if="affected.length" class="hint">
        保存将影响：{{
          affected.join("、")
        }}。停用后，这些策略会改用其他可用通道。
      </p>
      <button class="primary" :disabled="busy">
        <AppIcon name="save" :size="16" />保存通道
      </button>
    </form>
  </section>
</template>
<style scoped>
.channel-test-result {
  overflow-wrap: anywhere;
}
.channel-test-result pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
