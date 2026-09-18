<script setup lang="ts">
import { computed, ref } from "vue";
import AppIcon from "./AppIcon.vue";
import { api, ignoreAPIError, type ModelChannel, type Credential } from "./api";
const props = defineProps<{
  channels: ModelChannel[];
  credentials: Credential[];
}>();
const emit = defineEmits<{ changed: []; credentials: []; error: [string] }>();
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
  try {
    await fn();
    emit("changed");
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
  });
}
async function clear(c: ModelChannel) {
  await work(async () => {
    await api(`/admin/model-channels/${c.id}/cache/clear`, "POST", {});
    if (form.value.id === c.id) form.value = empty();
    notice.value = "结果缓存已失效，连接健康状态已重置。";
  });
}
async function remove(c: ModelChannel) {
  await work(async () => {
    await api(`/admin/model-channels/${c.id}`, "DELETE");
    if (form.value.id === c.id) form.value = empty();
    notice.value = "模型通道已删除。";
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
          @click="emit('changed')"
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
      调用与失败统计从本服务进程启动开始累计；保存连接后重新学习健康状态。默认连续失败
      3 次冷却 30 分钟，可在“审核策略 →
      模型调度”中调整；单通道免除失败冷却，仍受并发和 RPM 限制。当前并发、RPM
      和健康协调适用于单进程部署。
    </p>
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
          ><br />Grok 使用 sub2api 标准 Responses
          API。配置模型单价后，文本请求可参与人民币预算；无单价的调用费用需核对。</template
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
