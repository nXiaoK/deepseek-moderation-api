<script setup lang="ts">
import {
  computed,
  defineAsyncComponent,
  nextTick,
  onMounted,
  onBeforeUnmount,
  ref,
  watch,
} from "vue";
import AppIcon from "./AppIcon.vue";
import BillingPanel from "./BillingPanel.vue";
import PolicyEditor from "./PolicyEditor.vue";
import PolicyTools from "./PolicyTools.vue";
import OperationsPanel from "./OperationsPanel.vue";
import EmailSettingsPanel from "./EmailSettingsPanel.vue";
import ChannelPanel from "./ChannelPanel.vue";
import AttemptList from "./AttemptList.vue";
import {
  auditError,
  auditStage,
  auditResult,
  auditInput,
  auditCost,
  auditCostStatus,
  formatModelOutput,
  lastModelOutput,
} from "./auditDisplay";
import {
  api,
  APIError,
  setCSRF,
  setUnauthorizedHandler,
  ignoreAPIError,
  downloadFile,
  type AuditLog,
  type ClientKey,
  type Config,
  type Credential,
  type Policy,
  type Provider,
  type ModelChannel,
  type AnalysisLogFilter,
  type RuntimeLimits,
  type EvaluationSampleSeed,
} from "./api";

const AnalyticsPanel = defineAsyncComponent(
  () => import("./AnalyticsPanel.vue"),
);
const EvaluationPanel = defineAsyncComponent(
  () => import("./EvaluationPanel.vue"),
);
const user = ref(""),
  initializing = ref(true),
  busy = ref(false),
  error = ref(""),
  notice = ref("");
const username = ref("admin"),
  password = ref("");
const page = ref("policies");
const mobileNav = ref(false);
const navigationElement = ref<HTMLElement>();
const menuButton = ref<HTMLButtonElement>();
watch(mobileNav, async (open) => {
  await nextTick();
  if (open)
    navigationElement.value
      ?.querySelector<HTMLElement>("[aria-current='page']")
      ?.focus();
  else menuButton.value?.focus({ preventScroll: true });
});
function navigationKeydown(event: KeyboardEvent) {
  if (!mobileNav.value || window.matchMedia("(min-width: 761px)").matches)
    return;
  if (event.key === "Escape") {
    mobileNav.value = false;
    return;
  }
  if (event.key !== "Tab") return;
  const elements = navigationElement.value?.querySelectorAll<HTMLElement>(
    "a, button:not(:disabled)",
  );
  if (!elements?.length) return;
  const first = elements[0],
    last = elements[elements.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}
const policies = ref<Policy[]>([]),
  selected = ref<Policy | null>(null),
  config = ref<Config | null>(null),
  policyName = ref(""),
  baseline = ref("");
const archivedPolicies = ref<Policy[]>([]);
const evaluationSeed = ref<EvaluationSampleSeed | null>(null);
const runtime = ref<RuntimeLimits>({
  model_concurrency: 16,
  request_concurrency: 32,
  request_body_mib: 128,
  trial_concurrency: 2,
  max_images: 16,
});
const allPolicies = computed(() => [
  ...policies.value,
  ...archivedPolicies.value,
]);
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
      details?: Record<string, unknown>;
    }[]
  >([]);
const actionFilter = ref(""),
  actionResource = ref("");
const actionLabels: Record<string, string> = {
  "settings.email": "修改邮件提醒设置",
  "policy.save": "保存策略",
  "policy.create": "创建策略",
  "policy.state": "启停策略",
  "policy.archive": "归档策略",
  "policy.delete": "删除策略",
  "policy.import": "导入策略",
  "credential.update": "更新连接密钥",
  "credential.delete": "删除连接密钥",
  "channel.save": "保存模型通道",
  "channel.delete": "删除模型通道",
  "key.create": "创建访问密钥",
  "key.update": "编辑访问密钥",
  "key.rotate": "轮换访问密钥",
  "key.revoke": "停用访问密钥",
  "key.delete": "删除访问密钥",
  "budget.update": "修改预算",
  "price.publish": "更新价格",
  "price.reset": "取消价格覆盖",
  "cost.reconcile": "核对费用",
  "evaluation.start": "开始评测",
  "evaluation.cancel": "取消评测",
  "admin.password": "修改管理员密码",
};
async function loadActions() {
  const q = new URLSearchParams({
    action: actionFilter.value,
    resource_id: actionResource.value,
  });
  actions.value = await api("/admin/actions?" + q);
}
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
  keyExpires = ref(""),
  newToken = ref("");
const keyEditor = ref<(ClientKey & { expiry_input: string }) | null>(null);
const localDateInput = (value: string) => {
  const date = new Date(value);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, -1);
};
const keyExpired = (key: ClientKey) =>
  !!key.expires_at && Date.parse(key.expires_at) <= Date.now();
function editKey(key: ClientKey) {
  keyEditor.value = {
    ...key,
    policy_ids: [...key.policy_ids],
    expiry_input: key.expires_at ? localDateInput(key.expires_at) : "",
  };
}
const logItems = ref<AuditLog[]>([]),
  logTotal = ref(0),
  logPage = ref(1),
  logKind = ref(""),
  logResult = ref(""),
  logKeywordIgnore = ref("exclude"),
  logLatencyGT = ref(""),
  logPolicy = ref(""),
  logClient = ref(""),
  logFrom = ref(""),
  logRequestID = ref(""),
  logModel = ref(""),
  logChannel = ref(""),
  logErrorCode = ref(""),
  logTo = ref(""),
  detail = ref<AuditLog | null>(null);
const currentPassword = ref(""),
  newPassword = ref("");
const nav = [
  ["overview", "运行概览", "overview"],
  ["analytics", "数据分析", "chart"],
  ["logs", "审核记录", "file"],
  ["evaluations", "审核评测", "flask"],
  ["policies", "审核策略", "shield"],
  ["channels", "审核模型", "cpu"],
  ["credentials", "连接密钥", "link"],
  ["keys", "访问密钥", "key"],
  ["billing", "成本与预算", "wallet"],
  ["settings", "系统设置", "settings"],
] as const;
const pageMeta: Record<string, [string, string]> = {
  overview: ["OVERVIEW", "过去 24 小时 · 正式请求"],
  analytics: ["ANALYTICS", "模型用量与性能"],
  logs: ["AUDIT LOGS", "审核请求与判定记录"],
  evaluations: ["EVALUATIONS", "标注样本与模型对照"],
  policies: ["POLICIES", "当前生效的规则与模型调度"],
  channels: ["MODEL CHANNELS", "模型连接与运行状态"],
  credentials: ["CONNECTIONS", "上游模型访问凭证"],
  keys: ["ACCESS KEYS", "调用方访问权限"],
  billing: ["BILLING", "费用明细与预算"],
  settings: ["SETTINGS", "邮件提醒、账户安全与管理记录"],
};
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
  if (log?.keyword_ignored) return false;
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
  allPolicies.value.find((p) => p.id === id)?.name || id;
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
    if (ignoreAPIError(e)) return;
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}
function usePolicy(p: Policy) {
  selected.value = p;
  config.value = structuredClone(p.config);
  config.value.failure_threshold ||= 3;
  config.value.failure_cooldown_minutes ||= 30;
  policyName.value = p.name;
  baseline.value = JSON.stringify({ name: p.name, config: config.value });
}
async function loadPolicy(id: string) {
  const p = await api<Policy>(`/admin/policies/${id}`);
  usePolicy(p);
}
async function refresh() {
  const [ps, cs, ks, chs, limits] = await Promise.all([
    api<Policy[]>("/admin/policies?include_archived=1"),
    api<Credential[]>("/admin/credentials"),
    api<ClientKey[]>("/admin/api-keys"),
    api<ModelChannel[]>("/admin/model-channels"),
    api<RuntimeLimits>("/admin/runtime"),
  ]);
  policies.value = ps.filter((p) => !p.archived);
  archivedPolicies.value = ps.filter((p) => p.archived);
  credentials.value = cs;
  keys.value = ks;
  channels.value = chs;
  runtime.value = limits;
  if (!selected.value && policies.value[0]) {
    await loadPolicy(policies.value[0].id);
    keyPolicies.value = [policies.value[0].id];
  }
}
async function reloadPolicies(id?: string) {
  const all = await api<Policy[]>("/admin/policies?include_archived=1");
  policies.value = all.filter((p) => !p.archived);
  archivedPolicies.value = all.filter((p) => p.archived);
  keyPolicies.value = keyPolicies.value.filter((id) =>
    policies.value.some((p) => p.id === id),
  );
  if (id) await loadPolicy(id);
  else if (
    (selected.value &&
      !policies.value.some((p) => p.id === selected.value?.id)) ||
    !selected.value
  ) {
    if (policies.value[0]) await loadPolicy(policies.value[0].id);
    else {
      selected.value = null;
      config.value = null;
      baseline.value = "";
      policyName.value = "";
    }
  }
}
function policyToolBusy(value: boolean) {
  busy.value = value;
  if (value) {
    error.value = "";
    notice.value = "";
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
    clearSession();
  });
}
function clearSession() {
  evaluationSeed.value = null;
  user.value = "";
  setCSRF("");
  selected.value = null;
  config.value = null;
  baseline.value = "";
  creating.value = false;
  detail.value = null;
  keyEditor.value = null;
  newToken.value = "";
  credentialSecret.value = "";
  credentialEditID.value = "";
  credentialName.value = "";
  currentPassword.value = "";
  newPassword.value = "";
  password.value = "";
  policies.value = [];
  archivedPolicies.value = [];
  channels.value = [];
  credentials.value = [];
  keys.value = [];
  keyPolicies.value = [];
  keyName.value = "";
  keyExpires.value = "";
}
setUnauthorizedHandler(clearSession);
onBeforeUnmount(() => setUnauthorizedHandler(null));
function confirmPolicySwitch() {
  return (
    !dirty.value || window.confirm("切换策略会丢弃未保存的编辑，是否切换？")
  );
}
async function selectPolicy(event: Event) {
  const id = (event.target as HTMLSelectElement).value;
  if (!confirmPolicySwitch()) {
    (event.target as HTMLSelectElement).value = selected.value?.id || "";
    return;
  }
  await run(() => loadPolicy(id));
}
async function navigate(target: string) {
  mobileNav.value = false;
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
    if (target === "settings") await loadActions();
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
  if (busy.value || !confirmPolicySwitch()) return;
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
async function deleteCredential(c: Credential) {
  if (
    busy.value ||
    !window.confirm(
      `确定删除连接密钥“${c.name}”（${c.masked}）？保存的凭证将被永久删除，无法恢复。仍被模型通道引用的密钥不能删除。`,
    )
  )
    return;
  await run(async () => {
    await api(`/admin/credentials/${c.id}`, "DELETE");
    credentials.value = credentials.value.filter((item) => item.id !== c.id);
    if (credentialEditID.value === c.id) {
      credentialEditID.value = "";
      credentialName.value = "";
      credentialSecret.value = "";
    }
    notice.value = "连接密钥已删除。";
  });
}
async function createKey() {
  await run(async () => {
    const data = await api<{ token: string }>("/admin/api-keys", "POST", {
      name: keyName.value,
      policy_ids: keyPolicies.value,
      rpm: keyRPM.value,
      expires_at: keyExpires.value
        ? new Date(keyExpires.value).toISOString()
        : null,
    });
    newToken.value = data.token;
    keyName.value = "";
    keyExpires.value = "";
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
async function saveKey() {
  await run(async () => {
    const k = keyEditor.value;
    if (!k) return;
    await api("/admin/api-keys/" + k.id, "PUT", {
      name: k.name,
      policy_ids: k.policy_ids,
      rpm: k.rpm,
      active: k.active,
      expires_at: k.expiry_input
        ? new Date(k.expiry_input).toISOString()
        : null,
      expected_revision: k.revision || 1,
    });
    keyEditor.value = null;
    keys.value = await api("/admin/api-keys");
    notice.value = "访问密钥设置已保存。";
  });
}
async function rotateKey(k: ClientKey) {
  if (
    !window.confirm(
      "轮换“" +
        k.name +
        "”的访问密钥？旧密钥立即失效；调用方身份、策略授权和预算历史保持不变。",
    )
  )
    return;
  await run(async () => {
    const result = await api<{ token: string }>(
      "/admin/api-keys/" + k.id + "/rotate",
      "POST",
      { expected_revision: k.revision || 1 },
    );
    newToken.value = result.token;
    keys.value = await api("/admin/api-keys");
    notice.value = "密钥已轮换，请更新调用方凭证。启停与到期设置保持不变。";
  });
}
async function deleteKey(k: ClientKey) {
  if (
    busy.value ||
    !window.confirm(
      `确定删除访问密钥“${k.name}”（${k.prefix}…）？删除后无法恢复，使用该密钥的新请求将无法通过认证。历史审核和费用记录会保留。`,
    )
  ) {
    return;
  }
  await run(async () => {
    await api(`/admin/api-keys/${k.id}`, "DELETE");
    keys.value = keys.value.filter((key) => key.id !== k.id);
    if (newToken.value.startsWith(k.prefix)) newToken.value = "";
    notice.value = "访问密钥已删除，历史审核和费用记录已保留。";
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
function logQuery() {
  const q = new URLSearchParams({
    page: String(logPage.value),
    page_size: "20",
    request_id: logRequestID.value.trim(),
    model: logModel.value,
    channel_id: logChannel.value,
    error_code: logErrorCode.value,
    kind: logKind.value,
    result: logResult.value,
    keyword_ignore: logKeywordIgnore.value,
    latency_gt_ms: logLatencyGT.value,
    policy_id: logPolicy.value,
    client_id: logClient.value,
  });
  if (logFrom.value) q.set("from", new Date(logFrom.value).toISOString());
  if (logTo.value) q.set("to", new Date(logTo.value).toISOString());
  return q;
}
async function exportAuditLogs() {
  await run(() =>
    downloadFile("/admin/audit-logs/export?" + logQuery(), "audit-records.csv"),
  );
}
async function loadLogs() {
  const q = logQuery();
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
async function sampleFromAudit() {
  const log = detail.value;
  if (!log?.input_stored || !log.input?.trim()) return;
  evaluationSeed.value = {
    name:
      Array.from(policyLabel(log.policy_id)).slice(0, 40).join("") +
      " · " +
      log.id.slice(-8),
    input: log.input,
    expected: "manual",
    note: "来源审核请求 " + log.id,
  };
  detail.value = null;
  await navigate("evaluations");
}
async function inspectAnalyticsLogs(filter: AnalysisLogFilter) {
  logFrom.value = localDateInput(filter.from);
  logTo.value = localDateInput(filter.to);
  logKind.value = filter.kind === "all" ? "" : filter.kind;
  logModel.value = filter.model;
  logPolicy.value = filter.policy_id;
  logClient.value = filter.client_id;
  logChannel.value = filter.channel_id;
  logErrorCode.value = filter.error_code || "";
  logRequestID.value = "";
  logResult.value = "";
  logKeywordIgnore.value = "include";
  logLatencyGT.value = "";
  logPage.value = 1;
  await navigate("logs");
}
async function changePassword() {
  await run(async () => {
    await api("/admin/auth/password", "PUT", {
      current: currentPassword.value,
      password: newPassword.value,
    });
    clearSession();
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
  <div v-if="initializing" class="loading">
    <AppIcon name="refresh" class="spinning" />正在连接审核系统…
  </div>
  <div v-else-if="!user" class="login-shell">
    <a class="login-brand brand" href="#"
      ><span class="brand-mark"><AppIcon name="shield" :size="23" /></span
      ><span>内容审核<small>AUDIT CONSOLE</small></span></a
    >
    <form class="login-form" @submit.prevent="login">
      <span class="login-symbol"><AppIcon name="lock" :size="26" /></span>
      <div>
        <h1>登录控制台</h1>
        <p class="muted">DeepSeek 内容审核系统</p>
      </div>
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
        {{ busy ? "正在登录…" : "登录"
        }}<AppIcon
          :name="busy ? 'refresh' : 'next'"
          :class="{ spinning: busy }"
        />
      </button>
    </form>
    <footer class="login-foot">AUDIT CONSOLE · 管理员访问</footer>
  </div>
  <div v-else class="app-shell">
    <button
      v-if="mobileNav"
      class="nav-backdrop"
      aria-label="关闭导航"
      @click="mobileNav = false"
    />
    <aside
      ref="navigationElement"
      id="workspace-navigation"
      class="sidebar"
      :class="{ 'is-open': mobileNav }"
      @keydown="navigationKeydown"
    >
      <a class="brand" href="#" @click.prevent="navigate('policies')"
        ><span class="brand-mark"><AppIcon name="shield" :size="23" /></span
        ><span>内容审核<small>AUDIT CONSOLE</small></span></a
      >
      <p class="nav-caption">工作空间</p>
      <nav aria-label="主导航">
        <button
          v-for="item in nav"
          :key="item[0]"
          :class="{ active: page === item[0] }"
          :aria-current="page === item[0] ? 'page' : undefined"
          :disabled="busy"
          @click="navigate(item[0])"
        >
          <AppIcon :name="item[2]" />{{ item[1]
          }}<span v-if="page === item[0]" class="nav-active-dot" />
        </button>
      </nav>
      <div class="side-bottom">
        <span class="avatar">{{ user.slice(0, 1).toUpperCase() }}</span>
        <div>{{ user }}<small>管理员</small></div>
        <button
          class="icon-button logout"
          title="退出登录"
          aria-label="退出登录"
          @click="logout"
          :disabled="busy"
        >
          <AppIcon name="logout" />
        </button>
      </div>
    </aside>
    <main>
      <header class="topbar">
        <div class="row">
          <button
            ref="menuButton"
            class="icon-button mobile-menu"
            aria-label="打开导航"
            :aria-expanded="mobileNav"
            aria-controls="workspace-navigation"
            @click="mobileNav = !mobileNav"
          >
            <AppIcon :name="mobileNav ? 'close' : 'menu'" /></button
          ><span class="muted breadcrumb-root">工作空间</span
          ><AppIcon
            class="breadcrumb-root muted"
            name="chevron"
            :size="14"
          /><span>{{ title }}</span>
        </div>
        <span class="workspace-label"
          ><AppIcon name="shield" :size="14" />管理控制台</span
        >
      </header>
      <div class="workspace">
        <div class="page-heading">
          <div>
            <p class="eyebrow">
              {{ pageMeta[page]?.[0] }}
            </p>
            <h1>{{ title }}</h1>
            <p class="muted">
              {{ pageMeta[page]?.[1] }}
            </p>
          </div>
          <button
            v-if="page === 'policies'"
            class="primary"
            @click="creating = true"
            :disabled="busy"
          >
            <AppIcon name="plus" :size="16" />新建策略</button
          ><button
            v-else-if="
              !['billing', 'analytics', 'evaluations', 'settings'].includes(
                page,
              )
            "
            @click="navigate(page)"
            :disabled="busy"
            class="icon-button"
            title="刷新数据"
            aria-label="刷新数据"
          >
            <AppIcon name="refresh" :class="{ spinning: busy }" />
          </button>
        </div>
        <div v-if="error" class="banner error" role="alert">
          {{ error
          }}<button @click="error = ''" aria-label="关闭错误提示">
            <AppIcon name="close" />
          </button>
        </div>
        <div v-if="notice" class="banner success" role="status">
          {{ notice
          }}<button @click="notice = ''" aria-label="关闭提示">
            <AppIcon name="close" />
          </button>
        </div>

        <PolicyTools
          v-if="page === 'policies'"
          :selected="selected"
          :archived="archivedPolicies"
          :dirty="dirty"
          :busy="busy"
          :reload="reloadPolicies"
          @busy="policyToolBusy"
          @error="error = $event"
          @notice="notice = $event"
          @unauthorized="
            user = '';
            setCSRF('');
          "
        />
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
            <button
              class="switch"
              role="switch"
              :aria-checked="selected.enabled"
              :aria-label="selected.enabled ? '停用策略' : '启用策略'"
              :title="selected.enabled ? '停用策略' : '启用策略'"
              @click="togglePolicy"
              :disabled="busy || dirty"
            >
              <span />
            </button>
          </div>
          <PolicyEditor
            :key="selected.id"
            v-model="config"
            v-model:name="policyName"
            :policy="selected"
            :channels="channels"
            :busy="busy"
            :max-images="runtime.max_images"
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
              <AppIcon name="save" :size="16" />保存并生效
            </button>
          </footer>
        </template>
        <ChannelPanel
          v-if="page === 'channels'"
          :channels="channels"
          :credentials="credentials"
          :reload="reloadChannels"
          @credentials="navigate('credentials')"
          @error="error = $event"
        />

        <BillingPanel
          v-if="page === 'billing'"
          :clients="keys"
          :credentials="credentials"
          @unauthorized="
            user = '';
            setCSRF('');
          "
        />

        <EvaluationPanel
          v-if="page === 'evaluations'"
          :policies="policies"
          :channels="channels"
          :seed="evaluationSeed"
          @seeded="evaluationSeed = null"
          @log="openLog"
          @unauthorized="
            user = '';
            setCSRF('');
          "
        />
        <AnalyticsPanel
          v-if="page === 'analytics'"
          :policies="allPolicies"
          :clients="keys"
          :channels="channels"
          @logs="inspectAnalyticsLogs"
          @unauthorized="
            user = '';
            setCSRF('');
          "
        />

        <template v-if="page === 'overview'"
          ><div class="metric-grid">
            <section
              v-for="m in [
                ['requests', '审核请求', 'activity'],
                ['flagged', '策略命中', 'shield'],
                ['errors', '审核失败', 'gauge'],
                ['tokens', 'Token 用量', 'database'],
              ] as const"
              :key="m[0]"
              class="panel metric"
            >
              <span class="metric-label"
                >{{ m[1] }}<AppIcon :name="m[2]" /></span
              ><strong>{{ (overview[m[0]] || 0).toLocaleString() }}</strong
              ><small>过去 24 小时 · 正式请求</small>
            </section>
          </div>
          <div class="overview-health">
            <div>
              <AppIcon name="clock" /><span
                >平均审核延迟<strong
                  >{{
                    Math.round(overview.avg_latency_ms || 0).toLocaleString()
                  }}
                  <small>ms</small></strong
                ></span
              >
            </div>
            <div>
              <AppIcon name="gauge" /><span
                >P95 审核延迟<strong
                  >{{
                    Math.round(overview.p95_latency_ms || 0).toLocaleString()
                  }}
                  <small>ms</small></strong
                ></span
              >
            </div>
            <div>
              <AppIcon name="shield" /><span
                >启用策略<strong
                  >{{ policies.filter((p) => p.enabled).length }}
                  <small>/ {{ policies.length }}</small></strong
                ></span
              >
            </div>
            <button class="text-button" @click="navigate('analytics')">
              模型数据分析<AppIcon name="next" :size="16" />
            </button>
          </div>
          <section class="panel">
            <div class="panel-heading">
              <h2>策略状态</h2>
              <span class="muted small">{{ policies.length }} 个策略</span>
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
                ><button class="primary" :disabled="busy">
                  <AppIcon name="lock" :size="16" />加密保存</button
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
                  <button
                    class="icon-button danger-button"
                    title="删除连接密钥"
                    :aria-label="'删除连接密钥 ' + c.name"
                    @click="deleteCredential(c)"
                    :disabled="busy"
                  >
                    <AppIcon name="trash" :size="16" />
                  </button>
                </div>
              </div>
            </section></div
        ></template>

        <template v-if="page === 'keys'"
          ><section v-if="newToken" class="token-reveal">
            <strong>新访问密钥 · 明文仅显示这一次</strong
            ><code>{{ newToken }}</code>
            <div class="row">
              <button @click="copyToken">
                <AppIcon name="copy" :size="16" />复制密钥</button
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
                <label
                  >到期时间<input
                    v-model="keyExpires"
                    type="datetime-local"
                    step="any"
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
                  <span
                    class="badge"
                    :class="
                      !k.active ? 'gray' : keyExpired(k) ? 'amber' : 'green'
                    "
                    >{{
                      !k.active ? "已停用" : keyExpired(k) ? "已过期" : "有效"
                    }}</span
                  >
                  <p class="muted small">
                    {{ k.prefix }}… · {{ k.rpm }} 次/分钟
                  </p>
                  <p class="muted small">
                    {{ k.policy_ids.map(policyLabel).join("、") }}
                  </p>
                  <p class="muted small">
                    {{
                      k.expires_at ? "到期 " + time(k.expires_at) : "长期有效"
                    }}
                    ·
                    {{
                      k.last_used_at
                        ? "最近访问 " + time(k.last_used_at)
                        : "暂无访问记录"
                    }}
                  </p>
                </div>
                <div class="row">
                  <button @click="revokeKey(k)" :disabled="busy || !k.active">
                    {{ k.active ? "停用" : "已停用" }}
                  </button>
                  <button
                    class="icon-button"
                    title="编辑访问密钥"
                    :aria-label="'编辑访问密钥 ' + k.name"
                    :disabled="busy"
                    @click="editKey(k)"
                  >
                    <AppIcon name="edit" :size="16" />
                  </button>
                  <button
                    class="icon-button"
                    title="轮换访问密钥"
                    :aria-label="'轮换访问密钥 ' + k.name"
                    :disabled="busy"
                    @click="rotateKey(k)"
                  >
                    <AppIcon name="refresh" :size="16" />
                  </button>
                  <button
                    class="icon-button danger-button"
                    title="删除密钥"
                    :aria-label="'删除 ' + k.name"
                    @click="deleteKey(k)"
                    :disabled="busy"
                  >
                    <AppIcon name="trash" :size="16" />
                  </button>
                </div>
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
                >请求 ID<input
                  v-model="logRequestID"
                  placeholder="audit_…"
                  maxlength="200"
              /></label>
              <label
                >审核模型<input
                  v-model="logModel"
                  placeholder="全部模型"
                  maxlength="200"
              /></label>
              <label
                >调用通道<select v-model="logChannel">
                  <option value="">全部通道</option>
                  <option v-for="c in channels" :key="c.id" :value="c.id">
                    {{ c.name }}
                  </option>
                </select></label
              >
              <label
                >错误码<input
                  v-model="logErrorCode"
                  placeholder="全部错误"
                  maxlength="200"
              /></label>
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
                >关键词忽略<select v-model="logKeywordIgnore">
                  <option value="exclude">隐藏关键词忽略（默认）</option>
                  <option value="include">包含关键词忽略</option>
                  <option value="only">仅关键词忽略</option>
                </select></label
              ><label
                >耗时<select
                  v-model="logLatencyGT"
                  title="整次审核请求耗时，严格大于所选阈值"
                >
                  <option value="">不限</option>
                  <option
                    v-for="ms in [1000, 2000, 3000, 5000, 10000]"
                    :key="ms"
                    :value="String(ms)"
                  >
                    大于 {{ ms }} ms
                  </option>
                </select></label
              ><label
                >策略<select v-model="logPolicy">
                  <option value="">全部策略</option>
                  <option v-for="p in allPolicies" :key="p.id" :value="p.id">
                    {{ p.name }}{{ p.archived ? "（已归档）" : "" }}
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
                >开始<input
                  v-model="logFrom"
                  type="datetime-local"
                  step="any" /></label
              ><label
                >结束<input
                  v-model="logTo"
                  type="datetime-local"
                  step="any" /></label
              ><button :disabled="busy">
                <AppIcon name="filters" :size="16" />筛选
              </button>
              <button
                type="button"
                class="icon-button"
                title="导出审核摘要 CSV"
                aria-label="导出审核摘要 CSV"
                :disabled="busy"
                @click="exportAuditLogs"
              >
                <AppIcon name="download" :size="16" />
              </button>
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
                    <th>费用（元）</th>
                    <th>图片数</th>
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
                    <td :title="l.cost?.note">
                      {{ auditCost(l) }}<small>{{ auditCostStatus(l) }}</small>
                    </td>
                    <td>{{ l.request?.image_count ?? "—" }}</td>
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
                  class="icon-button"
                  title="上一页"
                  aria-label="上一页"
                  :disabled="busy || logPage <= 1"
                  @click="
                    logPage--;
                    run(loadLogs);
                  "
                >
                  <AppIcon name="back" :size="16" /></button
                ><span>{{ logPage }}</span
                ><button
                  class="icon-button"
                  title="下一页"
                  aria-label="下一页"
                  :disabled="busy || logPage * 20 >= logTotal"
                  @click="
                    logPage++;
                    run(loadLogs);
                  "
                >
                  <AppIcon name="next" :size="16" />
                </button>
              </div>
            </div></section
        ></template>

        <template v-if="page === 'settings'"
          ><OperationsPanel @navigate="navigate" />
          <EmailSettingsPanel @saved="run(loadActions)" />
          <section class="panel settings-panel">
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
            <form class="log-filters" @submit.prevent="run(loadActions)">
              <label
                >操作类型<select v-model="actionFilter">
                  <option value="">全部操作</option>
                  <option
                    v-for="(label, value) in actionLabels"
                    :key="value"
                    :value="value"
                  >
                    {{ label }}
                  </option>
                </select></label
              ><label
                >资源 ID<input
                  v-model="actionResource"
                  maxlength="200" /></label
              ><button :disabled="busy">
                <AppIcon name="filters" :size="16" />筛选操作
              </button>
            </form>
            <div v-for="(a, i) in actions" :key="i" class="record-row">
              <div>
                <strong>{{
                  a.action === "policy.archive" && a.details?.archived === false
                    ? "恢复策略"
                    : actionLabels[a.action] || a.action
                }}</strong>
                <p class="muted small">
                  {{ a.username }} · {{ a.resource_id }}
                </p>
                <details
                  v-if="Object.keys(a.details || {}).length"
                  class="action-details"
                >
                  <summary>变更摘要</summary>
                  <pre class="input-detail">{{
                    JSON.stringify(a.details, null, 2)
                  }}</pre>
                </details>
              </div>
              <time class="muted small">{{ time(a.created_at) }}</time>
            </div>
            <p v-if="!actions.length" class="empty">暂无管理操作。</p>
          </section></template
        >
      </div>
    </main>
  </div>

  <div v-if="keyEditor" class="modal-backdrop" @click.self="keyEditor = null">
    <section
      class="modal"
      role="dialog"
      aria-modal="true"
      aria-label="编辑访问密钥"
    >
      <div class="panel-heading">
        <h2>编辑访问密钥</h2>
        <button aria-label="关闭密钥编辑" @click="keyEditor = null">
          <AppIcon name="close" />
        </button>
      </div>
      <form class="stack-form" @submit.prevent="saveKey">
        <label
          >调用方名称<input v-model="keyEditor.name" required maxlength="200"
        /></label>
        <label
          >每分钟请求上限<input
            v-model.number="keyEditor.rpm"
            type="number"
            min="1"
            max="10000"
            required
        /></label>
        <label
          >到期时间<input
            v-model="keyEditor.expiry_input"
            type="datetime-local"
            step="any"
        /></label>
        <label class="check-row"
          ><input
            v-model="keyEditor.active"
            type="checkbox"
          />启用访问密钥</label
        >
        <fieldset>
          <legend>允许调用的策略</legend>
          <label v-for="p in allPolicies" :key="p.id" class="check-row"
            ><input
              v-model="keyEditor.policy_ids"
              type="checkbox"
              :value="p.id"
              :disabled="p.archived && !keyEditor.policy_ids.includes(p.id)"
            />{{ p.name }}{{ p.archived ? "（已归档）" : "" }}</label
          >
        </fieldset>
        <p v-if="error" class="error">{{ error }}</p>
        <button
          class="primary"
          :disabled="busy || (keyEditor.active && !keyEditor.policy_ids.length)"
        >
          <AppIcon name="save" :size="16" />保存访问密钥
        </button>
      </form>
    </section>
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
        <button @click="creating = false" aria-label="关闭">
          <AppIcon name="close" />
        </button>
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
        ><button class="primary" :disabled="busy">
          <AppIcon name="plus" :size="16" />创建策略
        </button>
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
        <button
          class="text-button"
          :disabled="busy || !detail.input_stored || !detail.input?.trim()"
          title="仅可使用已保存的文本"
          @click="sampleFromAudit"
        >
          <AppIcon name="flask" :size="16" />加入评测样本
        </button>
        <button @click="detail = null" aria-label="关闭">
          <AppIcon name="close" />
        </button>
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
        <dt v-if="detail.keyword_ignored">费用</dt>
        <dd v-if="detail.keyword_ignored">
          {{ auditCost(detail) }} · {{ auditCostStatus(detail) }}
        </dd>
      </dl>
      <AttemptList :attempts="detail.attempts || []" />
      <template v-if="showStandaloneOutput">
        <h3>
          {{ detail.model_output_stored ? "模型原始输出" : "结构化判定" }}
        </h3>
        <p v-if="detail.error_code && detailModelOutput" class="hint">
          以下为当时保存的模型原始输出（已脱敏）。审核失败时，原始评分不作为有效判定。
        </p>
        <pre v-if="detailModelOutput" class="input-detail">{{
          detailModelOutput
        }}</pre>
        <p v-else class="hint">此请求的模型原始输出未保存或已过期。</p>
      </template>
      <h3>审核输入</h3>
      <p v-if="detail.request?.text_only_fallback" class="hint">
        最终通道仅审核文本，图片已跳过。各次调用的审核范围见“调用过程”。
      </p>
      <p v-if="detail.request?.input_scope === 'text_and_images'" class="hint">
        最终通道接收了文本和图片进行审核。
      </p>
      <p v-if="detail.request?.image_count" class="hint">
        请求包含
        {{ detail.request.image_count }}
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
