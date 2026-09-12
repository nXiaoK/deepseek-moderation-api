<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import BillingPanel from "./BillingPanel.vue";
import {
  api,
  APIError,
  setCSRF,
  type AuditLog,
  type AuditResponse,
  type ClientKey,
  type Config,
  type Credential,
  type Policy,
  type Version,
} from "./api";

const user = ref(""),
  initializing = ref(true),
  busy = ref(false),
  error = ref(""),
  notice = ref("");
const username = ref("admin"),
  password = ref("");
const page = ref("policies"),
  tab = ref("prompt");
const policies = ref<Policy[]>([]),
  selected = ref<Policy | null>(null),
  config = ref<Config | null>(null),
  policyName = ref(""),
  baseline = ref("");
const credentials = ref<Credential[]>([]),
  keys = ref<ClientKey[]>([]),
  versions = ref<Version[]>([]);
const overview = ref<Record<string, number>>({}),
  actions = ref<
    {
      username: string;
      action: string;
      resource_id: string;
      created_at: string;
    }[]
  >([]);
const testInput = ref(""),
  testSource = ref("draft"),
  testResult = ref<AuditResponse | null>(null),
  testError = ref(""),
  showPreview = ref(false);
const creating = ref(false),
  newName = ref(""),
  newAlias = ref(""),
  copySource = ref("");
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
const compareVersion = ref<Version | null>(null),
  currentPassword = ref(""),
  newPassword = ref("");
const nav = [
  ["overview", "运行概览", "01"],
  ["policies", "审核策略", "02"],
  ["credentials", "模型密钥", "03"],
  ["keys", "访问密钥", "04"],
  ["logs", "审核记录", "05"],
  ["billing", "成本与预算", "06"],
  ["settings", "系统设置", "07"],
];
const title = computed(
  () => nav.find((n) => n[0] === page.value)?.[1] || "审核策略",
);
const dirty = computed(
  () =>
    JSON.stringify({ name: policyName.value, config: config.value }) !==
      baseline.value && !!selected.value,
);
const credentialReady = computed(() =>
  credentials.value.some(
    (c) => c.id === config.value?.credential_id && c.active,
  ),
);
const preview = computed(() =>
  JSON.stringify(
    {
      model: config.value?.model,
      thinking: { type: "disabled" },
      response_format: { type: "json_object" },
      messages: [
        { role: "system", content: config.value?.prompt },
        {
          role: "user",
          content: `<user_input>${testInput.value}</user_input>`,
        },
      ],
    },
    null,
    2,
  ),
);
const time = (v: string) =>
  new Date(v).toLocaleString("zh-CN", { hour12: false });
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
  config.value = structuredClone(p.draft);
  policyName.value = p.name;
  baseline.value = JSON.stringify({ name: p.name, config: config.value });
  testResult.value = null;
  testError.value = "";
}
async function loadPolicy(id: string) {
  const p = await api<Policy>(`/admin/policies/${id}`);
  usePolicy(p);
  versions.value = await api<Version[]>(`/admin/policies/${id}/versions`);
}
async function refresh() {
  const [ps, cs, ks] = await Promise.all([
    api<Policy[]>("/admin/policies"),
    api<Credential[]>("/admin/credentials"),
    api<ClientKey[]>("/admin/api-keys"),
  ]);
  policies.value = ps;
  credentials.value = cs;
  keys.value = ks;
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
    `/admin/policies/${selected.value.id}/draft`,
    "PUT",
    {
      expected_revision: selected.value.draft_revision,
      name: policyName.value,
      config: config.value,
    },
  );
  const prev = testResult.value;
  usePolicy(p);
  testResult.value = prev;
  policies.value = await api("/admin/policies");
}
async function saveDraft() {
  await run(async () => {
    await save();
    notice.value = "草稿已保存，正式审核配置保持当前发布版本。";
  });
}
async function publish() {
  await run(async () => {
    if (dirty.value) await save();
    if (!selected.value) return;
    await api(`/admin/policies/${selected.value.id}/publish`, "POST", {
      expected_revision: selected.value.draft_revision,
    });
    await loadPolicy(selected.value.id);
    policies.value = await api("/admin/policies");
    notice.value = "配置已发布，后续请求使用新版本。";
  });
}
async function test() {
  await run(async () => {
    testError.value = "";
    testResult.value = null;
    try {
      if (testSource.value === "draft" && dirty.value) await save();
      if (!selected.value) return;
      testResult.value = await api<AuditResponse>(
        `/admin/policies/${selected.value.id}/test`,
        "POST",
        {
          input: testInput.value,
          source: testSource.value,
          expected_revision: selected.value.draft_revision,
        },
      );
    } catch (e) {
      testError.value = message(e);
      throw e;
    }
  });
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
    notice.value = "策略草稿已创建。";
  });
}
async function rollback(v: Version) {
  await run(async () => {
    if (!selected.value) return;
    await api(`/admin/policies/${selected.value.id}/rollback`, "POST", {
      expected_revision: selected.value.draft_revision,
      version: v.version,
    });
    await loadPolicy(selected.value.id);
    compareVersion.value = null;
    notice.value = `已将 v${v.version} 的配置发布为新版本。`;
  });
}
async function togglePolicy() {
  await run(async () => {
    if (!selected.value) return;
    const p = await api<Policy>(
      `/admin/policies/${selected.value.id}/state`,
      "PUT",
      { enabled: !selected.value.enabled },
    );
    selected.value.enabled = p.enabled;
    policies.value = await api("/admin/policies");
    notice.value = p.enabled
      ? "策略已启用。"
      : "策略已停用，正式调用将返回策略不可用。";
  });
}
async function saveCredential() {
  await run(async () => {
    const result = await api<{ id: string }>(
      `/admin/credentials${credentialEditID.value ? "/" + credentialEditID.value : ""}`,
      credentialEditID.value ? "PUT" : "POST",
      {
        name: credentialName.value,
        api_key: credentialSecret.value,
        active: true,
      },
    );
    credentials.value = await api("/admin/credentials");
    if (config.value && !config.value.credential_id)
      config.value.credential_id = result.id;
    credentialEditID.value = "";
    credentialName.value = "";
    credentialSecret.value = "";
    notice.value = "模型密钥已加密保存。";
  });
}
async function toggleCredential(c: Credential) {
  await run(async () => {
    await api(`/admin/credentials/${c.id}`, "PUT", {
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
      <span class="login-foot">DeepSeek 内容审核系统</span>
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
                  ? "编辑你的审核规则，试跑验证后发布。"
                  : page === "overview"
                    ? "过去 24 小时的正式审核请求。"
                    : page === "credentials"
                      ? "管理审核系统调用 DeepSeek 所使用的凭证。"
                      : page === "keys"
                        ? "为 sub2api 和其他调用方分配独立访问凭证。"
                        : page === "logs"
                          ? "追踪每次判定使用的策略版本、评分与原因。"
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
            ><code>{{ selected.alias }}</code
            ><span class="badge" :class="selected.enabled ? 'green' : 'gray'">{{
              selected.enabled ? "已启用" : "已停用"
            }}</span
            ><span class="muted">{{
              selected.active_version
                ? `线上 v${selected.active_version}`
                : "尚未发布"
            }}</span
            ><button class="text-button" @click="togglePolicy" :disabled="busy">
              {{ selected.enabled ? "停用策略" : "启用策略" }}
            </button>
          </div>
          <div class="tabs" role="tablist" aria-label="策略配置">
            <button
              v-for="t in [
                ['prompt', '提示词配置'],
                ['model', '模型与判定'],
                ['versions', '版本历史'],
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
          <div v-if="tab === 'prompt'" class="editor-layout">
            <section class="panel editor-panel">
              <div class="panel-heading">
                <div>
                  <h2>审核提示词</h2>
                  <p class="muted small">完整内容作为模型的系统提示词发送。</p>
                </div>
                <span class="badge" :class="dirty ? 'amber' : 'gray'">{{
                  dirty ? "未保存" : "草稿已保存"
                }}</span>
              </div>
              <label class="sr-only" for="prompt-editor">审核提示词</label
              ><textarea
                id="prompt-editor"
                v-model="config.prompt"
                class="prompt-editor"
                spellcheck="false"
                :disabled="busy"
              ></textarea>
              <div class="editor-foot">
                <span
                  >{{
                    Array.from(config.prompt).length.toLocaleString()
                  }}
                  字符</span
                ><button
                  class="text-button"
                  @click="showPreview = !showPreview"
                >
                  {{ showPreview ? "收起请求预览" : "预览实际模型请求" }}
                </button>
              </div>
              <div class="contract">
                <span class="tiny-label">输出协议</span
                ><code>{"confidence": 0.00, "reason": "..."}</code
                ><span class="muted small"
                  >reason 最多 20 字；评分范围 0～1。</span
                >
              </div>
              <pre v-if="showPreview" class="request-preview">{{
                preview
              }}</pre>
            </section>
            <section class="panel test-panel">
              <div class="panel-heading">
                <div>
                  <h2>审核试跑</h2>
                  <p class="muted small">调用真实模型验证效果。</p>
                </div>
                <span class="live-dot" aria-label="真实 API 调用"></span>
              </div>
              <label
                >使用版本<select v-model="testSource" :disabled="busy">
                  <option value="draft">
                    当前草稿{{ dirty ? "（先保存）" : "" }}
                  </option>
                  <option
                    value="published"
                    :disabled="!selected.active_version"
                  >
                    已发布 v{{ selected.active_version }}
                  </option>
                </select></label
              ><label
                >待审核内容<textarea
                  v-model="testInput"
                  rows="7"
                  placeholder="粘贴一段内容，查看模型如何判定…"
                  :disabled="busy"
                ></textarea>
              </label>
              <div v-if="!credentialReady" class="hint">
                尚未选择可用模型密钥。<button
                  class="text-button"
                  @click="tab = 'model'"
                >
                  前往配置
                </button>
              </div>
              <button
                class="primary full"
                @click="test"
                :disabled="busy || !testInput.trim()"
              >
                {{
                  busy
                    ? "正在处理…"
                    : testSource === "draft" && dirty
                      ? "保存并试跑"
                      : "运行审核"
                }}
              </button>
              <p class="muted small">试跑不触发正式请求的封禁或通知。</p>
              <div v-if="testError" class="test-error" role="alert">
                <strong>审核未完成</strong>
                <p>{{ testError }}</p>
              </div>
              <div
                v-else-if="testResult"
                class="test-result"
                aria-live="polite"
              >
                <div class="result-heading">
                  <span
                    class="badge"
                    :class="testResult.results[0].flagged ? 'red' : 'green'"
                    >{{
                      testResult.results[0].flagged ? "命中策略" : "未命中"
                    }}</span
                  ><span class="muted small"
                    >{{ testResult.latency_ms }} ms</span
                  >
                </div>
                <div class="score">
                  {{ testResult.results[0].audit.confidence.toFixed(2)
                  }}<span>模型违规评分</span>
                </div>
                <div class="meter">
                  <span
                    :style="{
                      width: `${testResult.results[0].audit.confidence * 100}%`,
                    }"
                    :class="{ hit: testResult.results[0].flagged }"
                  ></span>
                </div>
                <div class="result-line">
                  <span>判定阈值</span
                  ><strong>{{ testResult.results[0].audit.threshold }}</strong>
                </div>
                <div class="result-line">
                  <span>使用版本</span
                  ><strong>{{
                    testResult.results[0].audit.policy_version
                      ? `v${testResult.results[0].audit.policy_version}`
                      : "草稿"
                  }}</strong>
                </div>
                <p class="reason">
                  {{ testResult.results[0].audit.reason || "模型未填写原因。" }}
                </p>
                <p v-if="testResult.cost" class="muted small">
                  本次费用：{{
                    testResult.cost.amount_cny === null
                      ? "待核对"
                      : "¥" + testResult.cost.amount_cny
                  }}
                  ·
                  {{
                    testResult.cost.status === "estimated"
                      ? "估算"
                      : testResult.cost.status === "pending"
                        ? "等待费用核对"
                        : "按用量计算"
                  }}
                </p>
                <span class="muted small"
                  >{{
                    testResult.usage.reported
                      ? testResult.usage.total_tokens + " tokens"
                      : "用量未返回"
                  }}
                  · {{ testResult.id.slice(0, 18) }}…</span
                >
              </div>
              <div v-else class="test-empty">
                <span>◇</span>
                <p>等待一次审核</p>
                <small>评分与判定会显示在这里</small>
              </div>
            </section>
          </div>
          <section v-if="tab === 'model'" class="panel settings-panel">
            <div class="panel-heading">
              <h2>模型连接与判定</h2>
              <span class="badge gray">草稿配置</span>
            </div>
            <div class="form-grid">
              <label
                >策略名称<input v-model="policyName" :disabled="busy" /></label
              ><label
                >DeepSeek 模型<input
                  v-model="config.model"
                  :disabled="busy" /></label
              ><label
                >DeepSeek Base URL<input
                  v-model="config.base_url"
                  type="url"
                  :disabled="busy" /></label
              ><label
                >上游密钥<select
                  v-model="config.credential_id"
                  :disabled="busy"
                >
                  <option value="">请选择密钥</option>
                  <option
                    v-for="c in credentials"
                    :key="c.id"
                    :value="c.id"
                    :disabled="!c.active"
                  >
                    {{ c.name }} · {{ c.masked
                    }}{{ c.active ? "" : "（已停用）" }}
                  </option></select
                ><button class="text-button" @click="navigate('credentials')">
                  管理模型密钥 →
                </button></label
              ><label
                >拦截阈值<input
                  v-model.number="config.threshold"
                  type="number"
                  min="0"
                  max="1"
                  step="0.01"
                  :disabled="busy"
                /><small class="muted"
                  >评分 ≥ 阈值时命中。数值越低，越容易拦截。</small
                ></label
              ><label
                >请求超时（毫秒）<input
                  v-model.number="config.timeout_ms"
                  type="number"
                  min="1000"
                  max="30000"
                  step="1000"
                  :disabled="busy" /></label
              ><label
                >最大输出 tokens<input
                  v-model.number="config.max_tokens"
                  type="number"
                  min="64"
                  max="4096"
                  :disabled="busy" /></label
              ><label
                >审核记录保留（天）<input
                  v-model.number="config.retention_days"
                  type="number"
                  min="1"
                  max="365"
                  :disabled="busy"
              /></label>
            </div>
            <label class="cache-config"
              >重复请求结果缓存（秒）
              <input
                v-model.number="config.result_cache_ttl_seconds"
                type="number"
                min="0"
                max="3600"
                :disabled="busy"
              />
              <small class="muted"
                >0
                为关闭。启用并发布后，相同调用方、策略版本、模型配置、密钥和完整输入可复用结果；后台试跑始终调用模型。</small
              >
            </label>
            <label class="check-row"
              ><input
                v-model="config.store_input"
                type="checkbox"
                :disabled="busy"
              /><span
                >保存审核输入原文<small
                  >启用后加密存储，管理员可在记录详情中查看；到期清理。</small
                ></span
              ></label
            >
            <p class="hint">
              模型使用非思考模式，返回 JSON。连接验证可在“审核试跑”中完成。
            </p>
          </section>
          <section v-if="tab === 'versions'" class="panel">
            <div class="panel-heading">
              <h2>发布历史</h2>
              <span class="muted small">回滚会创建一个新版本</span>
            </div>
            <div v-if="!versions.length" class="empty">
              还没有发布版本。配置模型密钥后即可首次发布。
            </div>
            <div v-for="v in versions" :key="v.version" class="version-row">
              <div>
                <strong>v{{ v.version }}</strong
                ><span
                  v-if="v.version === selected.active_version"
                  class="badge green"
                  >当前线上</span
                >
                <p class="muted small">
                  {{ time(v.created_at) }} · {{ v.author }} · 阈值
                  {{ v.config.threshold }} · {{ v.config.model }}
                </p>
              </div>
              <div class="row">
                <button @click="compareVersion = v">查看差异</button
                ><button
                  @click="rollback(v)"
                  :disabled="busy || v.version === selected.active_version"
                >
                  恢复并发布
                </button>
              </div>
            </div>
          </section>
          <footer class="save-bar">
            <div>
              <span class="status-dot" :class="{ unsaved: dirty }"></span
              >{{ dirty ? "有未保存的更改" : "草稿已保存"
              }}<small>发布后影响后续正式请求</small>
            </div>
            <div class="row">
              <button @click="saveDraft" :disabled="busy || !dirty">
                保存草稿</button
              ><button
                class="primary"
                @click="publish"
                :disabled="busy || !credentialReady"
              >
                {{ dirty ? "保存并发布" : "发布配置" }}
              </button>
            </div>
          </footer>
        </template>

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
            <div v-for="p in policies" :key="p.id" class="version-row">
              <div>
                <strong>{{ p.name }}</strong>
                <p class="muted small">{{ p.alias }}</p>
              </div>
              <span
                class="badge"
                :class="p.enabled && p.active_version ? 'green' : 'gray'"
                >{{
                  p.active_version ? `发布版本 v${p.active_version}` : "待发布"
                }}</span
              >
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
                  >名称<input
                    v-model="credentialName"
                    placeholder="例如：DeepSeek 主账户"
                    required
                    maxlength="200" /></label
                ><label
                  >DeepSeek API Key<input
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
                添加你的第一把 DeepSeek 密钥。
              </div>
              <div v-for="c in credentials" :key="c.id" class="credential-row">
                <div>
                  <strong>{{ c.name }}</strong>
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
              选择“自定义审核服务”，Base URL
              填写本系统服务地址；使用此处创建的访问密钥，模型名填写策略别名。
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
                  <option value="error">审核失败</option>
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
                    <th>策略 / 版本</th>
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
                          : clientLabel(l.client_id)
                      }}</small>
                    </td>
                    <td>
                      {{ policyLabel(l.policy_id)
                      }}<small>{{
                        l.policy_version ? `v${l.policy_version}` : "草稿"
                      }}</small>
                    </td>
                    <td>
                      <span
                        class="badge"
                        :class="
                          l.error_code ? 'amber' : l.flagged ? 'red' : 'green'
                        "
                        >{{
                          l.error_code ? "失败" : l.flagged ? "命中" : "未命中"
                        }}</span
                      >
                    </td>
                    <td>
                      {{
                        l.confidence === null ? "—" : l.confidence.toFixed(2)
                      }}
                    </td>
                    <td>{{ l.error_code || l.reason || "—" }}</td>
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
            <div v-for="(a, i) in actions" :key="i" class="version-row">
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
        ><button class="primary" :disabled="busy">创建草稿</button>
      </form>
    </section>
  </div>
  <div
    v-if="compareVersion && config"
    class="modal-backdrop"
    @click.self="compareVersion = null"
  >
    <section
      class="modal wide"
      role="dialog"
      aria-modal="true"
      aria-label="版本比较"
    >
      <div class="panel-heading">
        <h2>历史 v{{ compareVersion.version }} 与当前草稿</h2>
        <button @click="compareVersion = null" aria-label="关闭">×</button>
      </div>
      <div class="compare-grid">
        <div>
          <h3>历史版本 · 阈值 {{ compareVersion.config.threshold }}</h3>
          <p class="muted small">
            {{ compareVersion.config.model }} ·
            {{ compareVersion.config.timeout_ms }} ms
          </p>
          <pre>{{ compareVersion.config.prompt }}</pre>
        </div>
        <div>
          <h3>当前草稿 · 阈值 {{ config.threshold }}</h3>
          <p class="muted small">
            {{ config.model }} · {{ config.timeout_ms }} ms
          </p>
          <pre>{{ config.prompt }}</pre>
        </div>
      </div>
      <button
        class="primary"
        @click="rollback(compareVersion)"
        :disabled="busy"
      >
        恢复此版本并发布
      </button>
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
        <dt>策略</dt>
        <dd>
          {{ policyLabel(detail.policy_id) }} ·
          {{ detail.policy_version ? `v${detail.policy_version}` : "草稿" }}
        </dd>
        <dt>模型</dt>
        <dd>{{ detail.model }}</dd>
        <dt>判定</dt>
        <dd>
          {{
            detail.error_code ? "审核失败" : detail.flagged ? "命中" : "未命中"
          }}
        </dd>
        <dt>评分 / 阈值</dt>
        <dd>{{ detail.confidence ?? "—" }} / {{ detail.threshold }}</dd>
        <dt>原因或错误</dt>
        <dd>{{ detail.error_code || detail.reason || "—" }}</dd>
        <dt>耗时 / Tokens</dt>
        <dd>
          {{ detail.latency_ms }} ms /
          {{ detail.usage.reported ? detail.usage.total_tokens : "用量未返回" }}
        </dd>
      </dl>
      <h3>审核输入</h3>
      <pre v-if="detail.input_stored" class="input-detail">{{
        detail.input
      }}</pre>
      <p v-else class="hint">此请求未保存输入原文。</p>
    </section>
  </div>
</template>
