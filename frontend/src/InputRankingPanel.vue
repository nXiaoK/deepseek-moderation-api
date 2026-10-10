<script setup lang="ts">
import { onMounted, ref } from "vue";
import AppIcon from "./AppIcon.vue";
import Pagination from "./Pagination.vue";
import {
  api,
  downloadFile,
  ignoreAPIError,
  type AnalysisLogFilter,
  type ClientKey,
  type InputRankingItem,
  type InputRankingResponse,
  type Policy,
} from "./api";

defineProps<{ policies: Policy[]; clients: ClientKey[] }>();
const emit = defineEmits<{ logs: [filter: AnalysisLogFilter] }>();
const presets = [
  { value: "24h", label: "24 小时" },
  { value: "7d", label: "7 天" },
  { value: "30d", label: "30 天" },
  { value: "custom", label: "自定义" },
];
const range = ref("24h"),
  policyID = ref(""),
  clientID = ref(""),
  minCount = ref(2);
const beijingInput = (ms: number) =>
  new Date(ms + 8 * 3600000).toISOString().slice(0, 16);
const from = ref(beijingInput(Date.now() - 86400000)),
  to = ref(beijingInput(Date.now()));
const data = ref<InputRankingResponse | null>(null),
  busy = ref(false),
  exporting = ref(false),
  error = ref("");
const appliedQuery = ref("");
const detail = ref<InputRankingItem | null>(null),
  detailBusy = ref(false),
  detailError = ref(""),
  copied = ref(false);
let detailSequence = 0;
const count = (n: number) => n.toLocaleString("zh-CN");
const time = (value: string) =>
  new Date(value).toLocaleString("zh-CN", {
    timeZone: "Asia/Shanghai",
    hour12: false,
  });

function filterQuery() {
  if (
    !Number.isInteger(minCount.value) ||
    minCount.value < 1 ||
    minCount.value > 1000000000
  )
    throw new Error("最低出现次数须为 1～1000000000 的整数。");
  const q = new URLSearchParams({
    range: range.value,
    policy_id: policyID.value,
    client_id: clientID.value,
    min_count: String(minCount.value),
  });
  if (range.value === "custom") {
    const start = new Date(`${from.value}+08:00`),
      end = new Date(`${to.value}+08:00`);
    if (
      !Number.isFinite(start.getTime()) ||
      !Number.isFinite(end.getTime()) ||
      end <= start ||
      end.getTime() - start.getTime() > 366 * 86400000
    )
      throw new Error("请选择有效的起止时间，范围最多 366 天。");
    q.set("from", start.toISOString());
    q.set("to", end.toISOString());
  }
  return q;
}
async function load(
  page = 1,
  pageSize = data.value?.page_size || 20,
  useFilters = true,
) {
  if (busy.value) return;
  error.value = "";
  let q: URLSearchParams;
  try {
    q = useFilters ? filterQuery() : new URLSearchParams(appliedQuery.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : "筛选条件无效";
    return;
  }
  q.set("page", String(page));
  q.set("page_size", String(pageSize));
  busy.value = true;
  try {
    const result = await api<InputRankingResponse>(
      `/admin/analytics/input-ranking?${q}`,
    );
    data.value = result;
    q.set("range", "custom");
    q.set("from", result.from);
    q.set("to", result.to);
    appliedQuery.value = q.toString();
  } catch (e) {
    if (!ignoreAPIError(e))
      error.value = e instanceof Error ? e.message : "输入排行加载失败";
  } finally {
    busy.value = false;
  }
}
function refresh() {
  return load();
}
function selectRange(value: string) {
  range.value = value;
  if (value !== "custom") void load();
}
function changePage(page: number, pageSize: number) {
  return load(page, pageSize, false);
}
async function exportCSV() {
  if (!data.value || exporting.value) return;
  exporting.value = true;
  error.value = "";
  try {
    await downloadFile(
      `/admin/analytics/input-ranking/export?${appliedQuery.value}`,
      "input-ranking.csv",
    );
  } catch (e) {
    if (!ignoreAPIError(e))
      error.value = e instanceof Error ? e.message : "导出失败";
  } finally {
    exporting.value = false;
  }
}
async function openDetail(item: InputRankingItem) {
  const sequence = ++detailSequence;
  detail.value = item;
  detailBusy.value = true;
  detailError.value = "";
  copied.value = false;
  try {
    const result = await api<InputRankingItem>(
      `/admin/analytics/input-ranking/${item.fingerprint}?${appliedQuery.value}`,
    );
    if (sequence === detailSequence) detail.value = result;
  } catch (e) {
    if (sequence === detailSequence && !ignoreAPIError(e))
      detailError.value = e instanceof Error ? e.message : "输入详情加载失败";
  } finally {
    if (sequence === detailSequence) detailBusy.value = false;
  }
}
function closeDetail() {
  ++detailSequence;
  detail.value = null;
}
async function copyInput() {
  if (detail.value?.input == null) return;
  try {
    await navigator.clipboard.writeText(detail.value.input);
    copied.value = true;
  } catch {
    detailError.value = "复制失败。";
  }
}
function logsFor(item: InputRankingItem) {
  const d = data.value;
  if (!d) return;
  closeDetail();
  emit("logs", {
    from: d.from,
    to: d.to,
    kind: "production",
    policy_id: d.policy_id,
    client_id: d.client_id,
    model: "",
    channel_id: "",
    input_fingerprint: item.fingerprint,
  });
}
onMounted(refresh);
</script>

<template>
  <div class="input-ranking" :aria-busy="busy">
    <div class="ranking-toolbar">
      <div class="segmented" role="group" aria-label="输入排行时间范围">
        <button
          v-for="p in presets"
          :key="p.value"
          :aria-pressed="range === p.value"
          :disabled="busy"
          @click="selectRange(p.value)"
        >
          {{ p.label }}
        </button>
      </div>
      <div class="row ranking-actions">
        <button
          class="icon-button"
          title="刷新输入排行"
          aria-label="刷新输入排行"
          :disabled="busy"
          @click="refresh"
        >
          <AppIcon name="refresh" :size="16" :class="{ spinning: busy }" />
        </button>
        <button
          class="icon-button"
          title="导出输入排行 CSV"
          aria-label="导出输入排行 CSV"
          :disabled="busy || exporting || !data?.total"
          @click="exportCSV"
        >
          <AppIcon name="download" :size="16" />
        </button>
      </div>
    </div>
    <form class="ranking-filters" @submit.prevent="refresh">
      <label
        >策略<select v-model="policyID" :disabled="busy" @change="refresh">
          <option value="">全部策略</option>
          <option v-for="p in policies" :key="p.id" :value="p.id">
            {{ p.name }}
          </option>
        </select></label
      >
      <label
        >调用方<select v-model="clientID" :disabled="busy" @change="refresh">
          <option value="">全部调用方</option>
          <option v-for="c in clients" :key="c.id" :value="c.id">
            {{ c.name }}
          </option>
        </select></label
      >
      <label
        >最低出现次数<input
          v-model.number="minCount"
          type="number"
          min="1"
          max="1000000000"
          step="1"
          :disabled="busy"
      /></label>
      <template v-if="range === 'custom'">
        <label
          >开始时间（北京时间）<input
            v-model="from"
            type="datetime-local"
            required
            :disabled="busy"
        /></label>
        <label
          >结束时间（北京时间）<input
            v-model="to"
            type="datetime-local"
            required
            :disabled="busy"
        /></label>
      </template>
      <button type="submit" :disabled="busy">
        <AppIcon name="filters" :size="16" />筛选
      </button>
    </form>
    <div v-if="error" class="banner error" role="alert">
      {{ error }}<button :disabled="busy" @click="refresh">重试</button>
    </div>
    <p v-if="error && data" class="muted small">当前显示上次成功加载的结果。</p>
    <p v-if="busy && !data" role="status" class="muted">正在加载输入排行…</p>
    <template v-if="data">
      <div class="ranking-period">
        <span>{{ time(data.from) }} 至 {{ time(data.to) }}</span
        ><span>正式请求 · 北京时间</span>
      </div>
      <div class="ranking-metrics">
        <div>
          <span>已统计请求</span
          ><strong>{{ count(data.summary.indexed_requests) }}</strong>
        </div>
        <div>
          <span>不同输入</span
          ><strong>{{ count(data.summary.unique_inputs) }}</strong>
        </div>
        <div>
          <span>重复输入</span
          ><strong>{{ count(data.summary.repeated_inputs) }}</strong>
        </div>
        <div>
          <span>重复发送次数</span
          ><strong>{{ count(data.summary.repeat_requests) }}</strong>
        </div>
      </div>
      <div class="ranking-coverage muted small">
        <span v-if="data.summary.oldest_retained_at"
          >最早保留记录 {{ time(data.summary.oldest_retained_at) }}</span
        >
        <span v-if="data.summary.unindexed_requests"
          >未建立文本指纹 {{ count(data.summary.unindexed_requests) }} 条</span
        >
        <span v-if="data.summary.backfill_pending"
          >历史补算待处理 {{ count(data.summary.backfill_pending) }} 条</span
        >
      </div>
      <div class="table-scroll">
        <table class="ranking-table">
          <thead>
            <tr>
              <th>排名</th>
              <th>完整输入</th>
              <th class="number">出现次数</th>
              <th class="number">拦截次数</th>
              <th>最近出现</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(item, index) in data.items" :key="item.fingerprint">
              <td>{{ (data.page - 1) * data.page_size + index + 1 }}</td>
              <td class="ranking-text">
                <code v-if="item.preview != null" class="input-preview">{{
                  item.preview
                }}</code
                ><span v-else class="muted"
                  >原文未保存
                  <code>{{ item.fingerprint.slice(0, 12) }}</code></span
                ><small
                  >{{ count(item.text_chars) }} 字<span
                    v-if="item.preview != null && item.text_chars > 120"
                  >
                    · 摘要</span
                  ></small
                >
              </td>
              <td class="number">
                <strong>{{ count(item.occurrences) }}</strong>
              </td>
              <td class="number">{{ count(item.flagged) }}</td>
              <td>{{ time(item.last_seen) }}</td>
              <td>
                <div class="row row-actions">
                  <button
                    class="icon-button"
                    title="查看输入详情"
                    aria-label="查看输入详情"
                    @click="openDetail(item)"
                  >
                    <AppIcon name="file" :size="16" /></button
                  ><button
                    class="icon-button"
                    title="查看对应审核记录"
                    aria-label="查看对应审核记录"
                    @click="logsFor(item)"
                  >
                    <AppIcon name="next" :size="16" />
                  </button>
                </div>
              </td>
            </tr>
            <tr v-if="!data.items.length">
              <td colspan="6" class="empty">
                当前范围没有达到最低次数的输入。
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pagination
        :page="data.page"
        :page-size="data.page_size"
        :total="data.total"
        :busy="busy"
        aria-label="输入排行分页"
        @change="changePage"
      />
    </template>
    <div
      v-if="detail"
      class="modal-backdrop"
      @click.self="closeDetail"
      @keydown.esc="closeDetail"
    >
      <section
        class="modal ranking-detail"
        role="dialog"
        aria-modal="true"
        aria-label="重复输入详情"
      >
        <header class="panel-heading">
          <h2>输入详情</h2>
          <button
            class="icon-button"
            title="关闭输入详情"
            aria-label="关闭输入详情"
            @click="closeDetail"
          >
            <AppIcon name="close" :size="18" />
          </button>
        </header>
        <div v-if="detailError" class="banner error" role="alert">
          {{ detailError
          }}<button :disabled="detailBusy" @click="openDetail(detail)">
            重试
          </button>
        </div>
        <dl class="ranking-detail-metrics">
          <dt>出现次数</dt>
          <dd>{{ count(detail.occurrences) }}</dd>
          <dt>调用方数量</dt>
          <dd>{{ count(detail.clients) }}</dd>
          <dt>关键词阻止</dt>
          <dd>{{ count(detail.keyword_blocked) }}</dd>
          <dt>关键词忽略</dt>
          <dd>{{ count(detail.keyword_ignored) }}</dd>
          <dt>模型命中</dt>
          <dd>{{ count(detail.model_flagged) }}</dd>
          <dt>模型放行</dt>
          <dd>{{ count(detail.model_allowed) }}</dd>
          <dt>审核失败</dt>
          <dd>{{ count(detail.errors) }}</dd>
          <dt>缓存命中</dt>
          <dd>{{ count(detail.cache_hits) }}</dd>
          <dt>首次出现</dt>
          <dd>{{ time(detail.first_seen) }}</dd>
          <dt>最近出现</dt>
          <dd>{{ time(detail.last_seen) }}</dd>
        </dl>
        <div class="panel-heading">
          <h3>完整输入 · {{ count(detail.text_chars) }} 字</h3>
          <button
            class="icon-button"
            :title="copied ? '已复制' : '复制完整输入'"
            :aria-label="copied ? '已复制' : '复制完整输入'"
            :disabled="detailBusy || detail.input == null"
            @click="copyInput"
          >
            <AppIcon :name="copied ? 'check' : 'copy'" :size="16" />
          </button>
        </div>
        <p v-if="detailBusy" role="status" class="muted">正在读取输入…</p>
        <pre v-else-if="detail.input != null" class="input-detail">{{
          detail.input
        }}</pre>
        <p v-else class="muted">原文未保存或已过期。</p>
        <button @click="logsFor(detail)">
          <AppIcon name="file" :size="16" />查看审核记录
        </button>
      </section>
    </div>
  </div>
</template>

<style scoped>
.input-ranking {
  min-width: 0;
  padding-top: 24px;
}
.ranking-toolbar,
.ranking-period {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}
.ranking-actions {
  gap: 8px;
}
.ranking-filters {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: 14px;
  margin: 20px 0;
}
.ranking-filters label {
  flex: 1 1 180px;
  max-width: 260px;
  min-width: 0;
}
.ranking-filters input,
.ranking-filters select {
  width: 100%;
}
.ranking-period {
  font-size: 12px;
  color: var(--muted);
  padding: 12px 0;
}
.ranking-metrics {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  border-bottom: 1px solid var(--border);
  padding: 18px 0 24px;
  gap: 20px;
}
.ranking-metrics span {
  display: block;
  font-size: 12px;
  color: var(--muted);
}
.ranking-metrics strong {
  display: block;
  font-size: 24px;
  margin-top: 8px;
  overflow-wrap: anywhere;
}
.ranking-coverage {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  padding: 16px 0;
}
.ranking-table {
  min-width: 780px;
  table-layout: fixed;
}
.ranking-table th:first-child {
  width: 60px;
}
.ranking-table th:nth-child(2) {
  width: 38%;
}
.ranking-table th:nth-child(3),
.ranking-table th:nth-child(4) {
  width: 95px;
}
.ranking-table th:nth-child(5) {
  width: 160px;
}
.ranking-table th:last-child {
  width: 100px;
}
.number {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.ranking-text {
  overflow-wrap: anywhere;
}
.input-preview {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
  overflow: hidden;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--text);
}
.row-actions {
  flex-wrap: nowrap;
  gap: 4px;
}
.ranking-detail {
  width: 760px;
  max-width: 760px;
}
.ranking-detail-metrics {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr) 110px minmax(0, 1fr);
  gap: 12px;
  padding: 20px 0;
  margin: 0;
}
.ranking-detail-metrics dt {
  font-size: 12px;
  color: var(--muted);
}
.ranking-detail-metrics dd {
  margin: 0;
  overflow-wrap: anywhere;
}
.ranking-detail .input-detail {
  max-height: 320px;
  overflow: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
@media (max-width: 760px) {
  .ranking-filters label {
    flex-basis: calc(50% - 7px);
    max-width: none;
  }
  .ranking-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .ranking-detail-metrics {
    grid-template-columns: 110px minmax(0, 1fr);
  }
  .ranking-toolbar .segmented button {
    padding: 6px 8px;
  }
}
</style>
