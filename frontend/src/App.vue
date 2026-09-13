<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import BillingPanel from "./BillingPanel.vue";
import PolicyEditor from "./PolicyEditor.vue";
import ChannelPanel from "./ChannelPanel.vue";
import AttemptList from "./AttemptList.vue";
import {
  auditError,
  auditStage,
  auditResult,
  auditInput,
  formatModelOutput,
  lastModelOutput,
} from "./auditDisplay";
import {
  api,
  APIError,
  setCSRF,
  type AuditLog,
  type ClientKey,
  type Config,
  type Credential,
  type Policy,
  type Provider,
  type ModelChannel,
} from "./api";

const user = ref(""),
  initializing = ref(true),
  busy = ref(false),
  error = ref(""),
  notice = ref("");
const username = ref("admin"),
  password = ref("");
const page = ref("policies");
const policies = ref<Policy[]>([]),
  selected = ref<Policy | null>(null),
  config = ref<Config | null>(null),
  policyName = ref(""),
  baseline = ref("");
const credentials = ref<Credential[]>([]),
  keys = ref<ClientKey[]>([]),
  channels = ref<ModelChannel[]>([]);
const overview = ref<Record<string, number>>({}),
  actions = ref<
    {
      username: string;
      action: string;
      resource_id: string;
      created_at: string;
    }[]
  >([]);
const creating = ref(false),
  newName = ref(""),
  newAlias = ref(""),
  copySource = ref("");
const credentialProvider = ref<Provider>("deepseek"),
  credentialBaseURL = ref("https://api.deepseek.com");
const credentialName = ref(""),
  credentialSecret = ref(""),
  credentialEditID = ref("");
const keyName = ref(""),
  keyPolicies = ref<string[]>([]),
  keyRPM = ref(60),
  newToken = ref("");
const logItems = ref<AuditLog[]>([]),
  logTotal = ref(0),
  logPage = ref(1),
  logKind = ref(""),
  logResult = ref(""),
  logPolicy = ref(""),
  logClient = ref(""),
  logFrom = ref(""),
  logTo = ref(""),
  detail = ref<AuditLog | null>(null);
const currentPassword = ref(""),
  newPassword = ref("");
const nav = [
  ["overview", "运行概览", "01"],
  ["policies", "审核策略", "02"],
  ["channels", "审核模型", "03"],
  ["credentials", "连接密钥", "04"],
  ["keys", "访问密钥", "05"],
  ["logs", "审核记录", "06"],
  ["billing", "成本与预算", "07"],
  ["settings", "系统设置", "08"],
];
const title = computed(
  () => nav.find((n) => n[0] === page.value)?.[1] || "审核策略",
);
const dirty = computed(
  () =>
    JSON.stringify({ name: policyName.value, config: config.value }) !==
      baseline.value && !!selected.value,
);
const time = (v: string) =>
  new Date(v).toLocaleString("zh-CN", { hour12: false });
const modelOutputText = (log: AuditLog) => {
  const output = lastModelOutput(log);
  if (output) return formatModelOutput(output);
  if (log.confidence != null && log.reason) {
    return JSON.stringify(
      { confidence: log.confidence, reason: log.reason },
      null,
      2,
    );
  }
  return "";
};
const detailModelOutput = computed(() =>
  detail.value ? modelOutputText(detail.value) : "",
);
const showStandaloneOutput = computed(() => {
  const log = detail.value;
  if (!log?.attempts?.length) return true;
  return (
    !!detailModelOutput.value &&
    !log.attempts.some(
      (attempt) =>
        attempt.model_output &&
        formatModelOutput(attempt.model_output) === detailModelOutput.value,
    )
  );
});
const policyLabel = (id: string) =>
  policies.value.find((p) => p.id === id)?.name || id;
const clientLabel = (id: string) =>
  keys.value.find((k) => k.id === id)?.name || id;
const message = (e: unknown) => (e instanceof Error ? e.message : "操作失败");
async function run(fn: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await fn();
  } catch (e) {
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) {
      user.value = "";
      setCSRF("");
    }
  } finally {
    busy.value = false;
  }
}
function usePolicy(p: Policy) {
  selected.value = p;
  config.value = structuredClone(p.config);
  policyName.value = p.name;
  baseline.value = JSON.stringify({ name: p.name, config: config.value });
}
async function loadPolicy(id: string) {
  const p = await api<Policy>(`/admin/policies/${id}`);
  usePolicy(p);
}
async function refresh() {
  const [ps, cs, ks, chs] = await Promise.all([
    api<Policy[]>("/admin/policies"),
    api<Credential[]>("/admin/credentials"),
    api<ClientKey[]>("/admin/api-keys"),
    api<ModelChannel[]>("/admin/model-channels"),
  ]);
  policies.value = ps;
  credentials.value = cs;
  keys.value = ks;
  channels.value = chs;
  if (!selected.value && ps[0]) {
    await loadPolicy(ps[0].id);
    keyPolicies.value = [ps[0].id];
  }
}
async function login() {
  await run(async () => {
    const s = await api<{ username: string; csrf: string }>(
      "/admin/auth/login",
      "POST",
      { username: username.value, password: password.value },
    );
    setCSRF(s.csrf);
    user.value = s.username;
    password.value = "";
    await refresh();
  });
}
async function logout() {
  await run(async () => {
    await api("/admin/auth/logout", "POST", {});
    user.value = "";
    setCSRF("");
    selected.value = null;
    config.value = null;
    newToken.value = "";
  });
}
async function selectPolicy(event: Event) {
  const id = (event.target as HTMLSelectElement).value;
  if (
    dirty.value &&
    !window.confirm("切换策略会丢弃未保存的编辑，是否切换？")
  ) {
    (event.target as HTMLSelectElement).value = selected.value?.id || "";
    return;
  }
  await run(() => loadPolicy(id));
}
async function navigate(target: string) {
  page.value = target;
  error.value = "";
  notice.value = "";
  await run(async () => {
    if (target === "overview") overview.value = await api("/admin/overview");
    if (target === "policies" || target === "channels")
      channels.value = await api("/admin/model-channels");
    if (target === "logs") await loadLogs();
    if (target === "credentials")
      credentials.value = await api("/admin/credentials");
    if (target === "keys") keys.value = await api("/admin/api-keys");
    if (target === "settings") actions.value = await api("/admin/actions");
  });
}
async function save() {
  if (!selected.value || !config.value) return;
  const p = await api<Policy>(
    `/admin/policies/${selected.value.id}/config`,
    "PUT",
    {
      expected_revision: selected.value.revision,
      name: policyName.value,
      config: config.value,
    },
  );
  usePolicy(p);
  policies.value = await api("/admin/policies");
}
async function saveCurrent() {
  await run(async () => {
    await save();
    notice.value = "配置已保存，后续请求直接使用当前配置。";
  });
}
async function reloadChannels() {
  channels.value = await api("/admin/model-channels");
}
async function createPolicy() {
  await run(async () => {
    const p = await api<Policy>("/admin/policies", "POST", {
      name: newName.value,
      alias: newAlias.value,
      source_id: copySource.value,
    });
    policies.value = await api("/admin/policies");
    await loadPolicy(p.id);
    creating.value = false;
    newName.value = "";
    newAlias.value = "";
    notice.value = "策略已创建，配置模型通道后即可启用。";
  });
}
async function togglePolicy() {
  await run(async () => {
    if (!selected.value) return;
    const p = await api<Policy>(
      `/admin/policies/${selected.value.id}/state`,
      "PUT",
      {
        enabled: !selected.value.enabled,
        expected_revision: selected.value.revision,
      },
    );
    selected.value.enabled = p.enabled;
    selected.value.revision = p.revision;
    policies.value = await api("/admin/policies");
    notice.value = p.enabled
      ? "策略已启用。"
      : "策略已停用，正式调用将返回策略不可用。";
  });
}
async function saveCredential() {
  await run(async () => {
    await api<{ id: string }>(
      `/admin/credentials${credentialEditID.value ? "/" + credentialEditID.value : ""}`,
      credentialEditID.value ? "PUT" : "POST",
      {
        provider: credentialProvider.value,
        base_url: credentialBaseURL.value,
        name: credentialName.value,
        api_key: credentialSecret.value,
        active: true,
      },
    );
    credentials.value = await api("/admin/credentials");
    credentialEditID.value = "";
    credentialName.value = "";
    credentialSecret.value = "";
    notice.value = "模型密钥已加密保存。";
  });
}
async function toggleCredential(c: Credential) {
  await run(async () => {
    await api(`/admin/credentials/${c.id}`, "PUT", {
      provider: c.provider,
      base_url: c.base_url,
      name: c.name,
      api_key: "",
      active: !c.active,
    });
    credentials.value = await api("/admin/credentials");
    notice.value = c.active ? "密钥已停用。" : "密钥已启用。";
  });
}
async function createKey() {
  await run(async () => {
    const data = await api<{ token: string }>("/admin/api-keys", "POST", {
      name: keyName.value,
      policy_ids: keyPolicies.value,
      rpm: keyRPM.value,
    });
    newToken.value = data.token;
    keyName.value = "";
    keys.value = await api("/admin/api-keys");
  });
}
async function revokeKey(k: ClientKey) {
  await run(async () => {
    await api(`/admin/api-keys/${k.id}/revoke`, "POST", {});
    keys.value = await api("/admin/api-keys");
    notice.value = "访问密钥已停用。";
  });
}
async function copyToken() {
  try {
    await navigator.clipboard.writeText(newToken.value);
    notice.value = "访问密钥已复制。";
  } catch {
    error.value = "复制失败，请选中密钥手动复制。";
  }
}
async function loadLogs() {
  const q = new URLSearchParams({
    page: String(logPage.value),
    page_size: "20",
    kind: logKind.value,
    result: logResult.value,
    policy_id: logPolicy.value,
    client_id: logClient.value,
  });
  if (logFrom.value) q.set("from", new Date(logFrom.value).toISOString());
  if (logTo.value) q.set("to", new Date(logTo.value).toISOString());
  const data = await api<{ items: AuditLog[]; total: number }>(
    `/admin/audit-logs?${q}`,
  );
  logItems.value = data.items;
  logTotal.value = data.total;
}
async function filterLogs() {
  logPage.value = 1;
  await run(loadLogs);
}
async function openLog(id: string) {
  await run(async () => {
    detail.value = await api(`/admin/audit-logs/${id}`);
  });
}
async function changePassword() {
  await run(async () => {
    await api("/admin/auth/password", "PUT", {
      current: currentPassword.value,
      password: newPassword.value,
    });
    user.value = "";
    setCSRF("");
    currentPassword.value = "";
    newPassword.value = "";
    notice.value = "密码已更新，请重新登录。";
  });
}
onMounted(async () => {
  try {
    const s = await api<{ username: string; csrf: string }>("/admin/session");
    user.value = s.username;
    setCSRF(s.csrf);
    await refresh();
  } catch (e) {
    if (!(e instanceof APIError && e.status === 401)) error.value = message(e);
  } finally {
    initializing.value = false;
  }
});
window.addEventListener("beforeunload", (e) => {
  if (dirty.value) {
    e.preventDefault();
    e.returnValue = "";
  }
});
</script>

<template>
  <div v-if="initializing" class="loading">正在连接审核系统…</div>
  <div v-else-if="!user" class="login-shell">
    <div class="login-story">
      <span class="brand-mark">A</span>
      <p class="eyebrow">AUDIT CONSOLE</p>
      <h1>审核规则，<br />由你定义。</h1>
      <p>管理提示词、验证审核效果，<br />让每次判定都有据可查。</p>
      <span class="login-foot">多模型内容审核系统</span>
    </div>
    <form class="login-form" @submit.prevent="login">
      <p class="eyebrow">管理控制台</p>
      <h2>欢迎回来</h2>
      <p class="muted">使用部署时创建的管理员账户登录。</p>
      <label
        >用户名<input
          v-model="username"
          autocomplete="username"
          required
          maxlength="80" /></label
      ><label
        >密码<input
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
          maxlength="256"
      /></label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <p v-if="notice" class="success" role="status">{{ notice }}</p>
      <button class="primary" :disabled="busy">
        {{ busy ? "正在登录…" : "登录控制台" }}
      </button>
      <p class="muted small">首次登录密码位于部署目录的 .env 文件。</p>
    </form>
  </div>
  <div v-else class="app-shell">
    <aside class="sidebar">
      <a class="brand" href="#" @click.prevent="navigate('policies')"
        ><span class="brand-mark">A</span
        ><span>内容审核<small>AUDIT CONSOLE</small></span></a
      >
      <p class="nav-caption">管理工作区</p>
      <nav>
        <button
          v-for="item in nav"
          :key="item[0]"
          :class="{ active: page === item[0] }"
          @click="navigate(item[0])"
        >
          <span>{{ item[2] }}</span
          >{{ item[1] }}
        </button>
      </nav>
      <div class="side-bottom">
        <span class="avatar">{{ user.slice(0, 1).toUpperCase() }}</span>
        <div>{{ user }}<small>管理员</small></div>
        <button class="logout" @click="logout" :disabled="busy">退出</button>
      </div>
    </aside>
    <main>
      <header class="topbar">
        <span>工作区 <span class="slash">/</span> {{ title }}</span
        ><span class="subtle">独立审核服务</span>
      </header>
      <div class="workspace">
        <div class="page-heading">
          <div>
            <p class="eyebrow">
              {{
                page === "policies" ? "POLICY WORKSPACE" : "AUDIT MANAGEMENT"
              }}
            </p>
            <h1>{{ title }}</h1>
            <p class="muted">
              {{
                page === "policies"
                  ? "配置审核规则与模型调度，保存后直接生效。"
                  : page === "overview"
                    ? "过去 24 小时的正式审核请求。"
                    : page === "channels"
                      ? "管理多个审核模型通道，查看并发与连接状态。"
                      : page === "credentials"
                        ? "管理 DeepSeek 官方或第三方接口，以及 sub2api Grok 连接。"
                        : page === "keys"
                          ? "为 sub2api 和其他调用方分配独立访问凭证。"
                          : page === "logs"
                            ? "追踪实际模型、调用过程、评分与原因。"
                            : page === "billing"
                              ? "追踪上游 token 成本，设置调用方的日预算与月预算。"
                              : "管理账户和查看后台操作记录。"
              }}
            </p>
          </div>
          <button
            v-if="page === 'policies'"
            @click="creating = true"
            :disabled="busy"
          >
            ＋ 新建策略</button
          ><button
            v-else-if="page !== 'billing'"
            @click="navigate(page)"
            :disabled="busy"
          >
            刷新
          </button>
        </div>
        <div v-if="error" class="banner error" role="alert">
          {{ error
          }}<button @click="error = ''" aria-label="关闭错误提示">×</button>
        </div>
        <div v-if="notice" class="banner success" role="status">
          {{ notice
          }}<button @click="notice = ''" aria-label="关闭提示">×</button>
        </div>

        <template v-if="page === 'policies' && selected && config">
          <div class="policy-bar">
            <label class="policy-picker"
              >当前策略<select
                :value="selected.id"
                @change="selectPolicy"
                :disabled="busy"
              >
                <option v-for="p in policies" :value="p.id" :key="p.id">
                  {{ p.name }}
                </option>
              </select></label
            >
            <code>{{ selected.alias }}</code
            ><span class="badge" :class="selected.enabled ? 'green' : 'gray'">{{
              selected.enabled ? "已启用" : "已停用"
            }}</span>
            <button @click="togglePolicy" :disabled="busy || dirty">
              {{ selected.enabled ? "停用策略" : "启用策略" }}
            </button>
          </div>
          <PolicyEditor
            :key="selected.id"
            v-model="config"
            v-model:name="policyName"
            :policy="selected"
            :channels="channels"
            :busy="busy"
            @models="navigate('channels')"
            @error="error = $event"
          />
          <footer class="save-bar">
            <div>
              <span class="status-dot" :class="{ unsaved: dirty }"></span
              >{{ dirty ? "有未保存的修改" : "已保存"
              }}<small>保存后影响后续请求；启停前请先保存修改</small>
            </div>
            <button
              class="primary"
              @click="saveCurrent"
              :disabled="busy || !dirty"
            >
              保存并生效
            </button>
          </footer>
        </template>
        <ChannelPanel
          v-if="page === 'channels'"
          :channels="channels"
          :credentials="credentials"
          @changed="reloadChannels"
          @credentials="navigate('credentials')"
          @error="error = $event"
        />

        <BillingPanel
          v-if="page === 'billing'"
          :clients="keys"
          @unauthorized="
            user = '';
            setCSRF('');
          "
        />

        <template v-if="page === 'overview'"
          ><div class="metric-grid">
            <section
              v-for="m in [
                ['requests', '审核请求'],
                ['flagged', '策略命中'],
                ['errors', '审核失败'],
                ['tokens', 'Token 用量'],
              ]"
              :key="m[0]"
              class="panel metric"
            >
              <span class="muted">{{ m[1] }}</span
              ><strong>{{ (overview[m[0]] || 0).toLocaleString() }}</strong
              ><small>过去 24 小时 · 正式请求</small>
            </section>
          </div>
          <section class="panel">
            <div class="panel-heading"><h2>延迟与策略</h2></div>
            <div class="latency-row">
              <span
                >平均审核延迟
                <strong
                  >{{ Math.round(overview.avg_latency_ms || 0) }} ms</strong
                ></span
              ><span
                >P95 审核延迟
                <strong
                  >{{ Math.round(overview.p95_latency_ms || 0) }} ms</strong
                ></span
              >
            </div>
            <div v-for="p in policies" :key="p.id" class="record-row">
              <div>
                <strong>{{ p.name }}</strong>
                <p class="muted small">{{ p.alias }}</p>
              </div>
              <span class="badge" :class="p.enabled ? 'green' : 'gray'">{{
                p.enabled ? "已启用" : "已停用"
              }}</span>
            </div>
            <p v-if="!overview.requests" class="empty">
              暂无正式审核请求。可以先到审核策略页面试跑。
            </p>
          </section></template
        >

        <template v-if="page === 'credentials'"
          ><div class="two-columns">
            <section class="panel">
              <div class="panel-heading">
                <h2>
                  {{ credentialEditID ? "替换模型密钥" : "添加模型密钥" }}
                </h2>
              </div>
              <form class="stack-form" @submit.prevent="saveCredential">
                <label
                  >连接类型<select
                    v-model="credentialProvider"
                    :disabled="!!credentialEditID || busy"
                    @change="
                      credentialBaseURL =
                        credentialProvider === 'deepseek'
                          ? 'https://api.deepseek.com'
                          : ''
                    "
                  >
                    <option value="deepseek">DeepSeek（官方 / 第三方）</option>
                    <option value="grok_via_sub2api">Grok · sub2api API</option>
                  </select></label
                ><label
                  >{{
                    credentialProvider === "deepseek"
                      ? "DeepSeek API 地址"
                      : "sub2api 服务根地址"
                  }}<input
                    v-model="credentialBaseURL"
                    type="url"
                    required
                    :readonly="!!credentialEditID"
                    :placeholder="
                      credentialProvider === 'deepseek'
                        ? 'https://api.deepseek.com 或 https://api.example.com/v1'
                        : 'https://sub2api.example.com'
                    "
                  /><small class="muted"
                    ><template v-if="credentialProvider === 'deepseek'"
                      >支持官方地址或兼容 Chat Completions 的第三方 HTTP(S)
                      接口。第三方地址可填根地址或 /v1，系统自动调用
                      /v1/chat/completions。</template
                    ><template v-else
                      >审核系统的 AUDIT_SUB2API_ORIGINS 需允许此地址，sub2api
                      无需新增配置。</template
                    >凭证保存后地址固定，换地址请新建连接。</small
                  ></label
                >
                <label
                  >名称<input
                    v-model="credentialName"
                    placeholder="例如：DeepSeek 主账户"
                    required
                    maxlength="200" /></label
                ><label
                  >{{
                    credentialProvider === "deepseek"
                      ? "DeepSeek API Key"
                      : "sub2api API Key"
                  }}<input
                    v-model="credentialSecret"
                    type="password"
                    autocomplete="off"
                    placeholder="sk-…"
                    required
                    minlength="8"
                    maxlength="512" /></label
                ><button class="primary" :disabled="busy">加密保存</button
                ><button
                  v-if="credentialEditID"
                  type="button"
                  @click="
                    credentialEditID = '';
                    credentialName = '';
                    credentialSecret = '';
                  "
                >
                  取消替换
                </button>
                <p class="muted small">
                  密钥只在创建或替换时输入，保存后仅显示掩码。
                </p>
              </form>
            </section>
            <section class="panel">
              <div class="panel-heading"><h2>已配置密钥</h2></div>
              <div v-if="!credentials.length" class="empty">
                添加 DeepSeek 或 sub2api Grok 连接密钥。
              </div>
              <div v-for="c in credentials" :key="c.id" class="credential-row">
                <div>
                  <strong>{{ c.name }}</strong>
                  <p class="muted small">
                    {{
                      c.provider === "grok_via_sub2api"
                        ? "Grok / sub2api"
                        : "DeepSeek"
                    }}
                    · {{ c.base_url }}
                  </p>
                  <p>
                    <code>{{ c.masked }}</code
                    ><span class="badge" :class="c.active ? 'green' : 'gray'">{{
                      c.active ? "可用" : "已停用"
                    }}</span>
                  </p>
                </div>
                <div class="row">
                  <button
                    @click="
                      credentialEditID = c.id;
                      credentialName = c.name;
                      credentialProvider = c.provider;
                      credentialBaseURL = c.base_url;
                      credentialSecret = '';
                    "
                    :disabled="busy"
                  >
                    替换</button
                  ><button @click="toggleCredential(c)" :disabled="busy">
                    {{ c.active ? "停用" : "启用" }}
                  </button>
                </div>
              </div>
            </section>
          </div></template
        >

        <template v-if="page === 'keys'"
          ><section v-if="newToken" class="token-reveal">
            <strong>新访问密钥 · 明文仅显示这一次</strong
            ><code>{{ newToken }}</code>
            <div class="row">
              <button @click="copyToken">复制密钥</button
              ><button @click="newToken = ''">已保存，隐藏</button>
            </div>
          </section>
          <div class="two-columns">
            <section class="panel">
              <div class="panel-heading"><h2>创建访问密钥</h2></div>
              <form class="stack-form" @submit.prevent="createKey">
                <label
                  >调用方名称<input
                    v-model="keyName"
                    placeholder="例如：sub2api 生产环境"
                    required /></label
                ><label
                  >每分钟请求上限<input
                    v-model.number="keyRPM"
                    type="number"
                    min="1"
                    max="10000"
                    required
                /></label>
                <fieldset>
                  <legend>允许调用的策略</legend>
                  <label v-for="p in policies" :key="p.id" class="check-row"
                    ><input
                      v-model="keyPolicies"
                      :value="p.id"
                      type="checkbox"
                    />{{ p.name }}</label
                  >
                </fieldset>
                <button class="primary" :disabled="busy || !keyPolicies.length">
                  生成访问密钥
                </button>
              </form>
            </section>
            <section class="panel">
              <div class="panel-heading"><h2>调用方</h2></div>
              <div v-if="!keys.length" class="empty">尚未创建访问密钥。</div>
              <div v-for="k in keys" :key="k.id" class="credential-row">
                <div>
                  <strong>{{ k.name }}</strong>
                  <p class="muted small">
                    {{ k.prefix }}… · {{ k.rpm }} 次/分钟
                  </p>
                  <p class="muted small">
                    {{ k.policy_ids.map(policyLabel).join("、") }}
                  </p>
                </div>
                <button @click="revokeKey(k)" :disabled="busy || !k.active">
                  {{ k.active ? "停用" : "已停用" }}
                </button>
              </div>
            </section>
          </div>
          <section class="panel integration-note">
            <h2>sub2api 接入</h2>
            <p>
              在原版 sub2api 的内容审计设置中，将 Base URL
              填写为本系统根地址（不加
              /v1）；使用此处创建的访问密钥，模型名填写策略别名。
            </p>
            <p>
              运行模式选择“前置拦截”。本服务将命中结果通过 illicit
              分类传递，命中分数为 100%，未命中为
              0%；真实评分和原因在本服务的审核记录中查看。 sub2api
              分类阈值保持大于 0（可沿用默认值）。
            </p>
            <code
              >POST /v1/moderations · model:
              {{ selected?.alias || "abuse-audit-v1" }}</code
            >
          </section></template
        >

        <template v-if="page === 'logs'"
          ><section class="panel">
            <form class="log-filters" @submit.prevent="filterLogs">
              <label
                >来源<select v-model="logKind">
                  <option value="">全部</option>
                  <option value="production">正式请求</option>
                  <option value="test">后台试跑</option>
                </select></label
              ><label
                >结果<select v-model="logResult">
                  <option value="">全部</option>
                  <option value="flagged">命中</option>
                  <option value="allow">未命中</option>
                  <option value="error">失败 / 拒绝</option>
                </select></label
              ><label
                >策略<select v-model="logPolicy">
                  <option value="">全部策略</option>
                  <option v-for="p in policies" :key="p.id" :value="p.id">
                    {{ p.name }}
                  </option>
                </select></label
              ><label
                >调用方<select v-model="logClient">
                  <option value="">全部</option>
                  <option v-for="k in keys" :key="k.id" :value="k.id">
                    {{ k.name }}
                  </option>
                </select></label
              ><label
                >开始<input v-model="logFrom" type="datetime-local" /></label
              ><label>结束<input v-model="logTo" type="datetime-local" /></label
              ><button :disabled="busy">筛选</button>
            </form>
            <div class="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>时间 / 来源</th>
                    <th>策略 / 调用次数</th>
                    <th>判定</th>
                    <th>评分</th>
                    <th>原因</th>
                    <th>耗时</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="l in logItems" :key="l.id">
                    <td>
                      {{ time(l.created_at)
                      }}<small>{{
                        l.kind === "test"
                          ? "后台试跑"
                          : l.client_id
                            ? clientLabel(l.client_id)
                            : "未通过身份验证"
                      }}</small>
                    </td>
                    <td>
                      {{
                        l.policy_id
                          ? policyLabel(l.policy_id)
                          : l.request?.model || "未确定策略"
                      }}<small>{{
                        `${l.model || l.request?.model || "未调用模型"} · ${l.attempt_count} 次调用`
                      }}</small>
                    </td>
                    <td>
                      <span
                        class="badge"
                        :class="
                          l.error_code ? 'amber' : l.flagged ? 'red' : 'green'
                        "
                        >{{ auditResult(l) }}</span
                      >
                    </td>
                    <td>
                      {{
                        l.confidence === null ? "—" : l.confidence.toFixed(2)
                      }}
                    </td>
                    <td>
                      {{
                        l.error_code
                          ? auditError(l.error_code, l.error_message)
                          : l.reason || "—"
                      }}
                      <small v-if="l.request"
                        >HTTP {{ l.request.http_status }} ·
                        {{ auditStage(l.request.stage) }}</small
                      >
                    </td>
                    <td>{{ l.latency_ms }} ms</td>
                    <td>
                      <button class="text-button" @click="openLog(l.id)">
                        详情
                      </button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-if="!logItems.length" class="empty">
              没有符合条件的审核记录。
            </div>
            <div class="pagination">
              <span>共 {{ logTotal }} 条</span>
              <div class="row">
                <button
                  :disabled="busy || logPage <= 1"
                  @click="
                    logPage--;
                    run(loadLogs);
                  "
                >
                  上一页</button
                ><span>{{ logPage }}</span
                ><button
                  :disabled="busy || logPage * 20 >= logTotal"
                  @click="
                    logPage++;
                    run(loadLogs);
                  "
                >
                  下一页
                </button>
              </div>
            </div>
          </section></template
        >

        <template v-if="page === 'settings'"
          ><section class="panel settings-panel">
            <div class="panel-heading"><h2>管理员账户</h2></div>
            <form class="stack-form narrow" @submit.prevent="changePassword">
              <label
                >当前密码<input
                  v-model="currentPassword"
                  type="password"
                  autocomplete="current-password"
                  required /></label
              ><label
                >新密码<input
                  v-model="newPassword"
                  type="password"
                  autocomplete="new-password"
                  required
                  minlength="16"
                  maxlength="256" /></label
              ><button class="primary" :disabled="busy">
                修改密码并重新登录
              </button>
            </form>
          </section>
          <section class="panel">
            <div class="panel-heading">
              <h2>管理操作记录</h2>
              <span class="muted small">最近 100 条</span>
            </div>
            <div v-for="(a, i) in actions" :key="i" class="record-row">
              <div>
                <strong>{{ a.action }}</strong>
                <p class="muted small">
                  {{ a.username }} · {{ a.resource_id }}
                </p>
              </div>
              <time class="muted small">{{ time(a.created_at) }}</time>
            </div>
            <p v-if="!actions.length" class="empty">暂无管理操作。</p>
          </section></template
        >
      </div>
    </main>
  </div>

  <div v-if="creating" class="modal-backdrop" @click.self="creating = false">
    <section
      class="modal"
      role="dialog"
      aria-modal="true"
      aria-label="新建审核策略"
    >
      <div class="panel-heading">
        <h2>新建审核策略</h2>
        <button @click="creating = false" aria-label="关闭">×</button>
      </div>
      <form class="stack-form" @submit.prevent="createPolicy">
        <label>策略名称<input v-model="newName" required /></label
        ><label
          >模型别名<input
            v-model="newAlias"
            placeholder="例如：abuse-audit-v2"
            required
            pattern="[a-zA-Z0-9][a-zA-Z0-9._\-]{0,79}" /></label
        ><label
          >初始内容<select v-model="copySource">
            <option value="">使用初始提示词</option>
            <option v-for="p in policies" :key="p.id" :value="p.id">
              复制：{{ p.name }}
            </option>
          </select></label
        ><button class="primary" :disabled="busy">创建策略</button>
      </form>
    </section>
  </div>
  <div v-if="detail" class="modal-backdrop" @click.self="detail = null">
    <section
      class="modal wide"
      role="dialog"
      aria-modal="true"
      aria-label="审核记录详情"
    >
      <div class="panel-heading">
        <h2>审核详情</h2>
        <button @click="detail = null" aria-label="关闭">×</button>
      </div>
      <dl class="detail-grid">
        <dt>请求 ID</dt>
        <dd>{{ detail.id }}</dd>
        <template v-if="detail.request">
          <dt>请求接口</dt>
          <dd>{{ detail.request.method }} {{ detail.request.path }}</dd>
          <dt>请求模型 / 策略别名</dt>
          <dd>{{ detail.request.model || "未读取" }}</dd>
          <dt>HTTP 状态 / 处理阶段</dt>
          <dd>
            {{ detail.request.http_status }} ·
            {{ auditStage(detail.request.stage) }}
          </dd>
          <dt>输入概况</dt>
          <dd>{{ auditInput(detail) }}</dd>
        </template>
        <dt>策略</dt>
        <dd>
          {{ detail.policy_id ? policyLabel(detail.policy_id) : "未确定策略" }}
          ·
          {{ `${detail.attempt_count} 次调用` }}
        </dd>
        <dt>审核模型</dt>
        <dd v-if="detail.model">
          {{
            detail.provider === "grok_via_sub2api"
              ? "Grok / sub2api"
              : "DeepSeek"
          }}
          · {{ detail.model }}
        </dd>
        <dd v-else>未调用模型</dd>
        <dt v-if="detail.usage.actual_model">实际响应模型</dt>
        <dd v-if="detail.usage.actual_model">
          {{ detail.usage.actual_model }}
        </dd>
        <dt v-if="detail.usage.upstream_request_id">模型响应 ID</dt>
        <dd v-if="detail.usage.upstream_request_id">
          {{ detail.usage.upstream_request_id }}
        </dd>
        <dt>判定</dt>
        <dd>
          {{ auditResult(detail) }}
        </dd>
        <dt>评分 / 阈值</dt>
        <dd>
          {{ detail.confidence ?? "—" }} /
          {{ detail.policy_id ? detail.threshold : "—" }}
        </dd>
        <dt>原因或错误</dt>
        <dd>
          {{
            detail.error_code
              ? auditError(detail.error_code, detail.error_message)
              : detail.reason || "—"
          }}
          <small v-if="detail.error_code" class="muted error-code"
            >错误码：{{ detail.error_code }}</small
          >
          <small
            v-if="detail.error_code && !detail.error_message"
            class="muted error-code"
            >此记录未保存具体错误消息；可查看下方当时保存的模型返回。</small
          >
        </dd>
        <dt>耗时 / Tokens</dt>
        <dd>
          {{ detail.latency_ms }} ms /
          <template v-if="detail.usage.reported">
            {{ detail.usage.total_tokens }}
            <small class="muted">
              （输入 {{ detail.usage.prompt_tokens }} · 输出
              {{ detail.usage.completion_tokens
              }}<template v-if="detail.usage.reasoning_tokens">
                · 推理 {{ detail.usage.reasoning_tokens }}</template
              >）
            </small>
          </template>
          <template v-else>用量未返回</template>
        </dd>
      </dl>
      <AttemptList :attempts="detail.attempts || []" />
      <template v-if="showStandaloneOutput">
        <h3>模型返回</h3>
        <p v-if="detail.error_code && detailModelOutput" class="hint">
          以下为当时保存的模型原始输出（已脱敏）。审核失败时，原始评分不作为有效判定。
        </p>
        <pre v-if="detailModelOutput" class="input-detail">{{
          detailModelOutput
        }}</pre>
        <p v-else class="hint">此请求没有保存模型返回内容。</p>
      </template>
      <h3>审核输入</h3>
      <p v-if="detail.request?.text_only_fallback" class="hint">
        请求体超过 1 MiB，图片已跳过，仅审核文本。审核结果不覆盖图片内容。
      </p>
      <p v-if="detail.request?.image_count" class="hint">
        请求包含
        {{
          detail.request.image_count
        }}
        张图片。此处仅保留图片数量和按策略保存的文本，不保存图片内容或地址。
      </p>
      <p
        v-if="detail.request && detail.request.text_chars > 64000"
        class="hint"
      >
        请求文本超过 64000 字；开启输入保存时最多保留前 64000 字。
      </p>
      <pre v-if="detail.input_stored" class="input-detail">{{
        detail.input
      }}</pre>
      <p v-else-if="detail.request && !detail.policy_id" class="hint">
        此请求在确认可用策略前已失败或被拒绝，仅保留请求信息与错误原因。
      </p>
      <p v-else class="hint">
        此请求审核时未开启“加密保存输入原文”，原文未留存，无法恢复。
        可在“审核策略 →
        审核规则”中开启并点击“保存并生效”；仅对后续请求生效，缓存命中的请求也会保存。
      </p>
    </section>
  </div>
</template>
