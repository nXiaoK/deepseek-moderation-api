<script setup lang="ts">
import { onMounted, ref } from "vue";
import {
  api,
  APIError,
  type ClientKey,
  type CostView,
  type Usage,
} from "./api";
const props = defineProps<{ clients: ClientKey[] }>();
const emit = defineEmits<{ unauthorized: [] }>();
interface Price {
  id: number;
  model: string;
  rates: Record<string, number>;
  source: string;
  effective_at: string;
}
interface Budget {
  client_id: string;
  name: string;
  revision: number;
  daily_limit_cny: string | null;
  monthly_limit_cny: string | null;
  daily_used_cny: string;
  daily_reserved_cny: string;
  monthly_used_cny: string;
  monthly_reserved_cny: string;
}
interface CostRow {
  id: string;
  channel_id: string;
  price_snapshot?: Price;
  request_id: string;
  client_id: string;
  kind: string;
  model: string;
  policy_id: string;
  started_at: string;
  usage: Usage;
  cost: CostView;
}
interface Summary {
  calculated_cny: string;
  estimated_cny: string;
  reserved_cny: string;
  pending_count: number;
  local_cache_hits: number;
  cache_hit_tokens: number;
  cache_miss_tokens: number;
  output_tokens: number;
}
const tab = ref("costs"),
  busy = ref(false),
  error = ref(""),
  notice = ref("");
const prices = ref<Price[]>([]),
  budgets = ref<Budget[]>([]),
  rows = ref<CostRow[]>([]),
  summary = ref<Summary | null>(null),
  page = ref(1),
  total = ref(0);
const kind = ref(""),
  client = ref(""),
  status = ref(""),
  days = ref("30");
const priceForm = ref<{
  model: string;
  rates_cny: Record<string, string>;
  source: string;
  expected_price_id: number;
} | null>(null);
const costDetail = ref<CostRow | null>(null);
const reconciliation = ref<CostRow | null>(null),
  reconcileAmount = ref(""),
  reconcileReason = ref("");
const rateLabels = [
  ["hit", "缓存命中输入"],
  ["miss", "缓存未命中输入"],
  ["output", "输出"],
];
const statusLabels: Record<string, string> = {
  calculated: "用量计价",
  estimated: "保守估算",
  reserved: "预留中",
  pending: "待核对",
  local_cache: "本地缓存",
  zero: "无上游调用",
  reconciled: "已核对",
};
const yuan = (v: string | number | null | undefined) => {
  if (v === null || v === undefined) return "—";
  const raw =
    typeof v === "number"
      ? v.toFixed(12).replace(/0+$/, "").replace(/\.$/, "")
      : v;
  const [whole, fraction] = raw.split(".");
  return `¥${Number(whole || 0).toLocaleString("zh-CN")}${fraction ? "." + fraction : ""}`;
};
const time = (v: string) =>
  new Date(v).toLocaleString("zh-CN", {
    timeZone: "Asia/Shanghai",
    hour12: false,
  });
const clientName = (id: string) =>
  props.clients.find((k) => k.id === id)?.name || id;
const usagePercent = (b: Budget) =>
  b.monthly_limit_cny === null
    ? null
    : Number(b.monthly_limit_cny) === 0
      ? 100
      : Math.min(
          100,
          ((Number(b.monthly_used_cny) + Number(b.monthly_reserved_cny)) /
            Number(b.monthly_limit_cny)) *
            100,
        );
async function run(fn: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await fn();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "请求失败";
    if (e instanceof APIError && e.status === 401) emit("unauthorized");
  } finally {
    busy.value = false;
  }
}
async function loadCosts() {
  const q = new URLSearchParams({
    page: String(page.value),
    kind: kind.value,
    client_id: client.value,
    status: status.value,
  });
  if (days.value !== "all")
    q.set(
      "from",
      new Date(Date.now() - Number(days.value) * 86400000).toISOString(),
    );
  const data = await api<{ items: CostRow[]; total: number; summary: Summary }>(
    `/admin/billing/costs?${q}`,
  );
  rows.value = data.items;
  total.value = data.total;
  summary.value = data.summary;
}
async function refresh() {
  await run(async () => {
    const [ps, bs] = await Promise.all([
      api<Price[]>("/admin/billing/prices"),
      api<Budget[]>("/admin/billing/budgets"),
    ]);
    prices.value = ps;
    budgets.value = bs;
    await loadCosts();
  });
}
async function filter() {
  page.value = 1;
  await run(loadCosts);
}
async function saveBudget(b: Budget) {
  await run(async () => {
    await api(`/admin/api-keys/${b.client_id}/budget`, "PUT", {
      daily_limit_cny: b.daily_limit_cny?.trim() || null,
      monthly_limit_cny: b.monthly_limit_cny?.trim() || null,
      expected_revision: b.revision,
    });
    budgets.value = await api("/admin/billing/budgets");
    notice.value = "预算已保存，下一次正式审核前生效。";
  });
}
function editPrice(p?: Price) {
  const rates: Record<string, string> = {};
  for (const period of ["off", "peak"])
    for (const [key] of rateLabels)
      rates[`${period}_${key}`] = p
        ? String(p.rates[`${period}_${key}`] / 1e6)
        : "0";
  priceForm.value = {
    model: p?.model || "",
    rates_cny: rates,
    source: p?.source || "",
    expected_price_id: p?.id || 0,
  };
}
async function savePrice() {
  await run(async () => {
    if (!priceForm.value) return;
    prices.value = await api("/admin/billing/prices", "POST", priceForm.value);
    priceForm.value = null;
    notice.value = "单价已保存，已有账单保留计费时的价格。";
  });
}
async function reconcile() {
  await run(async () => {
    if (!reconciliation.value) return;
    await api(
      `/admin/billing/costs/${reconciliation.value.id}/reconcile`,
      "POST",
      { amount_cny: reconcileAmount.value, reason: reconcileReason.value },
    );
    reconciliation.value = null;
    await loadCosts();
    budgets.value = await api("/admin/billing/budgets");
    notice.value = "费用核对已记录，预算占用已更新。";
  });
}
onMounted(refresh);
</script>

<template>
  <div class="billing-workspace">
    <div class="tabs" role="tablist" aria-label="成本管理">
      <button
        v-for="item in [
          ['costs', '成本明细'],
          ['budgets', '调用方预算'],
          ['prices', '模型单价'],
        ]"
        :key="item[0]"
        :class="{ active: tab === item[0] }"
        role="tab"
        :aria-selected="tab === item[0]"
        @click="tab = item[0]"
      >
        {{ item[1] }}</button
      ><button class="billing-refresh" @click="refresh" :disabled="busy">
        刷新
      </button>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="success" role="status">{{ notice }}</p>
    <template v-if="tab === 'costs'">
      <div v-if="summary" class="metric-grid billing-metrics">
        <section class="panel metric">
          <span class="muted">按用量计算 / 已核对</span
          ><strong>{{ yuan(summary.calculated_cny) }}</strong
          ><small>不等同于供应商实扣账单</small>
        </section>
        <section class="panel metric">
          <span class="muted">保守估算费用</span
          ><strong>{{ yuan(summary.estimated_cny) }}</strong
          ><small>缺少缓存拆分或跨计费时段</small>
        </section>
        <section class="panel metric">
          <span class="muted">预留 / 待核对</span
          ><strong>{{ yuan(summary.reserved_cny) }}</strong
          ><small>{{ summary.pending_count }} 笔，继续占用预算</small>
        </section>
      </div>
      <section v-if="summary" class="panel token-summary">
        <div>
          <span class="muted">DeepSeek 缓存命中输入</span
          ><strong
            >{{ summary.cache_hit_tokens.toLocaleString() }} tokens</strong
          >
        </div>
        <div>
          <span class="muted">未命中输入</span
          ><strong
            >{{ summary.cache_miss_tokens.toLocaleString() }} tokens</strong
          >
        </div>
        <div>
          <span class="muted">输出</span
          ><strong>{{ summary.output_tokens.toLocaleString() }} tokens</strong>
        </div>
        <div>
          <span class="muted">本地结果缓存</span
          ><strong>{{ summary.local_cache_hits }} 次免上游调用</strong>
        </div>
      </section>
      <section class="panel">
        <form class="log-filters" @submit.prevent="filter">
          <label
            >统计范围<select v-model="days">
              <option value="7">最近 7 天</option>
              <option value="30">最近 30 天</option>
              <option value="all">全部成本记录</option>
            </select></label
          ><label
            >来源<select v-model="kind">
              <option value="">正式 + 试跑</option>
              <option value="production">正式请求</option>
              <option value="test">后台试跑</option>
            </select></label
          ><label
            >调用方<select v-model="client">
              <option value="">全部调用方</option>
              <option v-for="k in clients" :key="k.id" :value="k.id">
                {{ k.name }}
              </option>
            </select></label
          ><label
            >计量状态<select v-model="status">
              <option value="">全部</option>
              <option
                v-for="(label, key) in statusLabels"
                :key="key"
                :value="key"
              >
                {{ label }}
              </option>
            </select></label
          ><button :disabled="busy">筛选</button>
        </form>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>时间（北京时间）</th>
                <th>调用方 / 模型</th>
                <th>状态</th>
                <th>输入命中 / 未命中</th>
                <th>输出 tokens</th>
                <th>费用 / 预留</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in rows" :key="row.id">
                <td>
                  {{ time(row.started_at)
                  }}<small>{{
                    row.kind === "test" ? "后台试跑" : "正式请求"
                  }}</small>
                </td>
                <td>
                  {{ clientName(row.client_id) }}<small>{{ row.model }}</small>
                </td>
                <td>
                  <span
                    class="badge"
                    :class="
                      ['pending', 'estimated', 'reserved'].includes(
                        row.cost.status,
                      )
                        ? 'amber'
                        : 'green'
                    "
                    >{{
                      statusLabels[row.cost.status] || row.cost.status
                    }}</span
                  ><small
                    >{{
                      row.cost.period === "gateway_managed"
                        ? "网关计费"
                        : row.cost.period === "higher_rate_estimate"
                          ? "跨时段估算"
                          : row.cost.period === "peak"
                            ? "高峰"
                            : "空闲"
                    }}
                    · 价格 #{{ row.cost.price_id ?? "未知" }}</small
                  >
                </td>
                <td>
                  {{ row.usage.prompt_cache_hit_tokens ?? "—" }} /
                  {{ row.usage.prompt_cache_miss_tokens ?? "—" }}
                </td>
                <td>
                  {{ row.usage.reported ? row.usage.completion_tokens : "—" }}
                </td>
                <td>
                  {{ yuan(row.cost.amount_cny)
                  }}<small v-if="row.cost.amount_cny === null"
                    >预留 {{ yuan(row.cost.reserved_cny) }}</small
                  >
                </td>
                <td>
                  <button class="text-button" @click="costDetail = row">
                    计价依据
                  </button>
                  <button
                    v-if="['pending', 'estimated'].includes(row.cost.status)"
                    class="text-button"
                    @click="
                      reconciliation = row;
                      reconcileAmount = row.cost.amount_cny || '';
                      reconcileReason = '';
                    "
                  >
                    核对费用</button
                  ><span v-else class="muted small">{{
                    row.cost.status === "local_cache" ? "无新增上游费用" : ""
                  }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="!rows.length" class="empty">
          暂无成本记录。新请求会记录用量与价格快照，旧审核记录不会被补算成零元。
        </p>
        <div class="pagination">
          <span>共 {{ total }} 条</span>
          <div class="row">
            <button
              :disabled="busy || page <= 1"
              @click="
                page--;
                run(loadCosts);
              "
            >
              上一页</button
            ><span>{{ page }}</span
            ><button
              :disabled="busy || page * 20 >= total"
              @click="
                page++;
                run(loadCosts);
              "
            >
              下一页
            </button>
          </div>
        </div>
      </section>
      <p class="muted small">
        计算费用 = 缓存命中输入 × 命中单价 + 未命中输入 × 未命中单价 + 输出 ×
        输出单价。按本系统请求开始时间选价；跨时段使用较高价格并标记估算。待核对不表示免费。
      </p>
    </template>
    <template v-if="tab === 'budgets'"
      ><p class="hint">
        预算按北京时间自然日 / 自然月统计。留空表示不限，0
        表示没有可用预算。调用前保守预留，完成后按用量结算；待核对费用保留占用。本地缓存不产生新增上游费用，预算耗尽时仍可命中。
      </p>
      <section
        v-for="b in budgets"
        :key="b.client_id"
        class="panel budget-card"
      >
        <div class="panel-heading">
          <h2>{{ b.name }}</h2>
          <span
            v-if="usagePercent(b) !== null"
            class="badge"
            :class="usagePercent(b)! >= 80 ? 'amber' : 'green'"
            >月预算占用 {{ usagePercent(b)!.toFixed(1) }}%</span
          >
        </div>
        <div class="form-grid">
          <label
            >每日预算（元）<input
              v-model="b.daily_limit_cny"
              type="text"
              inputmode="decimal"
              placeholder="不限"
            /><small class="muted"
              >已计量 {{ yuan(b.daily_used_cny) }} · 预留
              {{ yuan(b.daily_reserved_cny) }}</small
            ></label
          ><label
            >每月预算（元）<input
              v-model="b.monthly_limit_cny"
              type="text"
              inputmode="decimal"
              placeholder="不限"
            /><small class="muted"
              >已计量 {{ yuan(b.monthly_used_cny) }} · 预留
              {{ yuan(b.monthly_reserved_cny) }}</small
            ></label
          >
        </div>
        <div class="budget-actions">
          <button class="primary" @click="saveBudget(b)" :disabled="busy">
            保存预算
          </button>
        </div>
      </section>
      <p v-if="!budgets.length" class="empty">
        先在“访问密钥”中创建调用方，再设置预算。
      </p>
      <p class="muted small">
        预算不足会阻止新的上游审核调用并返回 budget_exceeded。sub2api
        是否继续转发业务请求，仍由其“审核服务失败时”设置决定。预算预留是保守估算，实际用量高于预留时会如实记录并停止后续超额调用。
      </p></template
    >
    <template v-if="tab === 'prices'"
      ><div class="row price-heading">
        <p class="muted">
          人民币 / 每百万 tokens。高峰：周一至周五
          09:00–12:00、14:00–18:00（北京时间）。
        </p>
        <button @click="editPrice()">添加模型单价</button>
      </div>
      <section v-for="p in prices" :key="p.id" class="panel">
        <div class="panel-heading">
          <div>
            <h2>{{ p.model }}</h2>
            <p class="muted small">生效时间 · {{ time(p.effective_at) }}</p>
          </div>
          <button @click="editPrice(p)">更新价格</button>
        </div>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>计费项</th>
                <th>空闲时段</th>
                <th>高峰时段</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="[key, label] in rateLabels" :key="key">
                <td>{{ label }}</td>
                <td>{{ yuan(p.rates[`off_${key}`] / 1e6) }}</td>
                <td>{{ yuan(p.rates[`peak_${key}`] / 1e6) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="price-source muted small">来源：{{ p.source }}</p>
      </section>
      <p class="muted small">
        初始化价格已按 2026-09-12
        官方文档核对。价格变化时在此保存新单价；已有账单不重新计价。
      </p></template
    >
    <div
      v-if="costDetail"
      class="modal-backdrop"
      @click.self="costDetail = null"
    >
      <section
        class="modal"
        role="dialog"
        aria-modal="true"
        aria-label="计价依据"
      >
        <div class="panel-heading">
          <h2>计价依据</h2>
          <button @click="costDetail = null" aria-label="关闭">×</button>
        </div>
        <div class="stack-form">
          <p class="hint">{{ costDetail.cost.note }}</p>
          <code>{{ costDetail.request_id }}</code
          ><small
            >费用记录 {{ costDetail.id }} · 通道
            {{ costDetail.channel_id }}</small
          >
          <p>
            {{ costDetail.model }} · {{ time(costDetail.started_at) }} ·
            {{ statusLabels[costDetail.cost.status] }}
          </p>
          <p>
            费用 {{ yuan(costDetail.cost.amount_cny) }} · 预留
            {{ yuan(costDetail.cost.reserved_cny) }}
          </p>
          <template v-if="costDetail.price_snapshot">
            <strong>请求时价格快照 #{{ costDetail.price_snapshot.id }}</strong>
            <table>
              <thead>
                <tr>
                  <th>每百万 tokens</th>
                  <th>空闲</th>
                  <th>高峰</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="[key, label] in rateLabels" :key="key">
                  <td>{{ label }}</td>
                  <td>
                    {{
                      yuan(costDetail.price_snapshot.rates[`off_${key}`] / 1e6)
                    }}
                  </td>
                  <td>
                    {{
                      yuan(costDetail.price_snapshot.rates[`peak_${key}`] / 1e6)
                    }}
                  </td>
                </tr>
              </tbody>
            </table>
            <p class="muted small">{{ costDetail.price_snapshot.source }}</p>
          </template>
          <p v-else class="muted">该记录没有可用价格快照。</p>
        </div>
      </section>
    </div>
    <div v-if="priceForm" class="modal-backdrop" @click.self="priceForm = null">
      <section
        class="modal"
        role="dialog"
        aria-modal="true"
        aria-label="设置模型单价"
      >
        <div class="panel-heading">
          <h2>设置模型单价</h2>
          <button @click="priceForm = null" aria-label="关闭">×</button>
        </div>
        <form class="stack-form" @submit.prevent="savePrice">
          <label
            >模型名称<input
              v-model="priceForm.model"
              :readonly="!!priceForm.expected_price_id"
              required
          /></label>
          <div class="form-grid price-form">
            <template v-for="[key, label] in rateLabels" :key="key"
              ><label
                >空闲 · {{ label
                }}<input
                  v-model="priceForm.rates_cny[`off_${key}`]"
                  type="text"
                  inputmode="decimal"
                  required /></label
              ><label
                >高峰 · {{ label
                }}<input
                  v-model="priceForm.rates_cny[`peak_${key}`]"
                  type="text"
                  inputmode="decimal"
                  required /></label
            ></template>
          </div>
          <label
            >价格来源 / 变更依据<input
              v-model="priceForm.source"
              required
              maxlength="500"
          /></label>
          <p class="muted small">以上均为每百万 tokens 的人民币单价。</p>
          <button class="primary" :disabled="busy">保存单价</button>
          <p v-if="error" class="error">{{ error }}</p>
        </form>
      </section>
    </div>
    <div
      v-if="reconciliation"
      class="modal-backdrop"
      @click.self="reconciliation = null"
    >
      <section
        class="modal"
        role="dialog"
        aria-modal="true"
        aria-label="核对上游费用"
      >
        <div class="panel-heading">
          <h2>核对上游费用</h2>
          <button @click="reconciliation = null" aria-label="关闭">×</button>
        </div>
        <form class="stack-form" @submit.prevent="reconcile">
          <p class="hint">{{ reconciliation.cost.note }}</p>
          <label
            >核对后实际费用（元）<input
              v-model="reconcileAmount"
              type="text"
              inputmode="decimal"
              required /></label
          ><label
            >核对依据<textarea
              v-model="reconcileReason"
              rows="3"
              required
              maxlength="500"
              placeholder="例如：供应商账单记录或未扣费确认"
            ></textarea></label
          ><button class="primary" :disabled="busy">保存核对结果</button>
          <p class="muted small">原状态、原金额、操作者和依据都会留存。</p>
          <p v-if="error" class="error">{{ error }}</p>
        </form>
      </section>
    </div>
  </div>
</template>

<style scoped>
.billing-refresh {
  margin-left: auto;
}
.billing-metrics {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.billing-metrics strong {
  font-size: 25px;
  overflow-wrap: anywhere;
}
.token-summary {
  display: flex;
  justify-content: space-between;
  gap: 20px;
  padding: 20px;
  flex-wrap: wrap;
}
.token-summary div {
  display: flex;
  flex-direction: column;
  gap: 7px;
  font-size: 12px;
}
.token-summary strong {
  font-weight: 500;
}
.budget-actions {
  padding: 0 24px 24px;
}
.price-heading {
  justify-content: space-between;
  margin-bottom: 20px;
}
.price-heading p {
  margin: 0;
}
.price-source {
  padding: 18px 22px;
  overflow-wrap: anywhere;
  margin: 0;
}
.price-form {
  padding: 0;
  gap: 14px;
}
.billing-workspace .tabs {
  margin-top: 0;
}
@media (max-width: 800px) {
  .billing-metrics {
    grid-template-columns: 1fr;
  }
  .billing-workspace .tabs {
    gap: 16px;
  }
}
</style>
