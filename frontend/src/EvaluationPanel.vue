<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  api,
  APIError,
  ignoreAPIError,
  downloadFile,
  type Config,
  type Policy,
  type ModelChannel,
  type CostView,
  type EvaluationSampleSeed,
} from "./api";
import AppIcon from "./AppIcon.vue";

interface Sample {
  id: string;
  name: string;
  input?: string;
  expected: string;
  note: string;
  revision: number;
}
interface Run {
  id: string;
  name: string;
  policy_name: string;
  status: string;
  total: number;
  completed: number;
  max_cost_cny: string;
  message: string;
  created_at: string;
}
interface Result {
  sequence: number;
  sample_id: string;
  sample_name: string;
  target: string;
  iteration: number;
  expected: string;
  status: string;
  request_id: string;
  model: string;
  confidence: number | null;
  keyword_ignored?: boolean;
  flagged: boolean;
  threshold: number;
  reason: string;
  error_code: string;
  error_message: string;
  latency_ms: number;
  cost?: CostView;
}
interface Score {
  target: string;
  total: number;
  processed: number;
  valid: number;
  errors: number;
  labelled: number;
  correct: number;
  allow_samples: number;
  flagged_samples: number;
  false_positives: number;
  false_negatives: number;
  unstable_samples: number;
  average_latency_ms: number | null;
  known_cost_cny: string;
  pending_costs: number;
}
interface Detail {
  run: Run;
  config: Config;
  policy_revision: number;
  targets: Record<string, string>;
  results: Result[];
  scores: Score[];
}
const props = defineProps<{
  policies: Policy[];
  channels: ModelChannel[];
  seed?: EvaluationSampleSeed | null;
}>();
const emit = defineEmits<{ unauthorized: []; log: [id: string]; seeded: [] }>();
const tab = ref("samples"),
  busy = ref(false),
  error = ref(""),
  notice = ref("");
const samples = ref<Sample[]>([]),
  runs = ref<Run[]>([]),
  selected = ref<string[]>([]);
const editor = ref<Sample | null>(null),
  detail = ref<Detail | null>(null),
  snapshot = ref<Sample | null>(null);
const runForm = ref<{
  name: string;
  policy_id: string;
  channel_ids: string[];
  repetitions: number;
  max_cost_cny: string;
  prompt: string;
  threshold: number;
} | null>(null);
const importInput = ref<HTMLInputElement>(),
  resultFilter = ref("all");
const abort = new AbortController();
watch(
  () => props.seed,
  (value) => {
    if (value) {
      editor.value = { ...value, id: "", revision: 0 };
      emit("seeded");
    }
  },
  { immediate: true },
);
let timer: ReturnType<typeof setInterval> | undefined;
let polling = false,
  disposed = false;
let requestVersion = 0;
const call = <T,>(path: string, method = "GET", body?: unknown) =>
  api<T>(path, method, body, abort.signal);
const expectedLabels: Record<string, string> = {
  allow: "应放行",
  flagged: "应命中",
  manual: "待人工判断",
};
const statusLabels: Record<string, string> = {
  queued: "等待开始",
  running: "运行中",
  completed: "已完成",
  cancelled: "已取消",
  interrupted: "已中断",
  budget_exhausted: "预算不足",
  pending_cost: "费用待核对",
  pending: "待执行",
  error: "失败",
  skipped: "未执行",
};
const ratio = (n: number, d: number) =>
  d ? ((n / d) * 100).toFixed(1) + "%" : "—";
const latency = (n: number | null) =>
  n == null
    ? "—"
    : n < 1000
      ? n.toFixed(0) + " ms"
      : (n / 1000).toFixed(2) + " s";
const time = (v: string) =>
  new Date(v).toLocaleString("zh-CN", {
    timeZone: "Asia/Shanghai",
    hour12: false,
  });
const active = (run: Run) => ["queued", "running"].includes(run.status);
const policy = computed(() =>
  props.policies.find((p) => p.id === runForm.value?.policy_id),
);
const targets = computed(() =>
  props.channels.filter(
    (c) =>
      c.enabled &&
      c.credential_active &&
      policy.value?.config.channels.some(
        (b) => b.channel_id === c.id && b.enabled,
      ),
  ),
);
const planned = computed(
  () =>
    selected.value.length *
    (runForm.value?.channel_ids.length || 0) *
    (runForm.value?.repetitions || 0),
);
const maxCalls = computed(
  () =>
    selected.value.length *
    (runForm.value?.repetitions || 0) *
    (runForm.value?.channel_ids.reduce(
      (n, id) =>
        n +
        (id
          ? 1
          : Math.min(
              targets.value.length,
              policy.value?.config.max_attempts || 1,
            )),
      0,
    ) || 0),
);
const visibleResults = computed(
  () =>
    detail.value?.results.filter(
      (row) =>
        resultFilter.value === "all" ||
        (resultFilter.value === "errors" &&
          ["error", "interrupted"].includes(row.status)) ||
        (resultFilter.value === "wrong" &&
          row.status === "completed" &&
          row.expected !== "manual" &&
          row.flagged !== (row.expected === "flagged")),
    ) || [],
);
const totals = computed(() => ({
  valid: detail.value?.scores.reduce((n, s) => n + s.valid, 0) || 0,
  errors: detail.value?.scores.reduce((n, s) => n + s.errors, 0) || 0,
  pending: detail.value?.scores.reduce((n, s) => n + s.pending_costs, 0) || 0,
}));
async function work(fn: () => Promise<void>) {
  if (busy.value) return;
  requestVersion++;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await fn();
  } catch (e) {
    if (ignoreAPIError(e)) return;
    if (!disposed) {
      error.value = e instanceof Error ? e.message : "操作失败";
      if (e instanceof APIError && e.status === 401) emit("unauthorized");
    }
  } finally {
    busy.value = false;
  }
}
async function load() {
  const [ss, rr] = await Promise.all([
    call<Sample[]>("/admin/evaluation/samples"),
    call<Run[]>("/admin/evaluation/runs"),
  ]);
  samples.value = ss;
  runs.value = rr;
  selected.value = selected.value.filter((id) => ss.some((s) => s.id === id));
}
async function showRun(id: string) {
  detail.value = await call<Detail>("/admin/evaluation/runs/" + id);
  resultFilter.value = "all";
}
async function refresh() {
  await load();
  if (detail.value) await showRun(detail.value.run.id);
}
async function poll() {
  if (polling || busy.value || !runs.value.some(active)) return;
  polling = true;
  const version = requestVersion;
  try {
    const selectedID = detail.value?.run.id;
    const [nextRuns, nextDetail] = await Promise.all([
      call<Run[]>("/admin/evaluation/runs"),
      selectedID
        ? call<Detail>("/admin/evaluation/runs/" + selectedID)
        : Promise.resolve(null),
    ]);
    if (version === requestVersion && !disposed) {
      runs.value = nextRuns;
      if (nextDetail) detail.value = nextDetail;
    }
  } catch (e) {
    if (ignoreAPIError(e)) return;
    if (!disposed) {
      error.value = e instanceof Error ? e.message : "评测刷新失败";
      if (e instanceof APIError && e.status === 401) emit("unauthorized");
    }
  } finally {
    polling = false;
  }
}
function addSample() {
  editor.value = {
    id: "",
    name: "",
    input: "",
    expected: "manual",
    note: "",
    revision: 0,
  };
}
async function editSample(sample: Sample) {
  await work(async () => {
    editor.value = await call<Sample>("/admin/evaluation/samples/" + sample.id);
  });
}
async function saveSample() {
  await work(async () => {
    const s = editor.value;
    if (!s) return;
    await call(
      "/admin/evaluation/samples" + (s.id ? "/" + s.id : ""),
      s.id ? "PUT" : "POST",
      {
        name: s.name,
        input: s.input,
        expected: s.expected,
        note: s.note,
        expected_revision: s.revision,
      },
    );
    editor.value = null;
    await load();
    notice.value = "样本已加密保存。";
  });
}
async function deleteSample(sample: Sample) {
  if (!window.confirm("删除样本“" + sample.name + "”？已有评测快照不受影响。"))
    return;
  await work(async () => {
    await call("/admin/evaluation/samples/" + sample.id, "DELETE");
    await load();
  });
}
async function importBuiltin() {
  await work(async () => {
    const result = await call<{ imported: number }>(
      "/admin/evaluation/samples/import",
      "POST",
      { builtin: true },
    );
    await load();
    notice.value = "已导入 " + result.imported + " 条内置样本。";
  });
}
async function importSamples(event: Event) {
  const input = event.target as HTMLInputElement,
    file = input.files?.[0];
  input.value = "";
  if (!file) return;
  await work(async () => {
    if (file.size > 900 * 1024) throw new Error("导入文件最多 900 KiB");
    const text = await file.text();
    const rows: Record<string, unknown>[] = file.name
      .toLowerCase()
      .endsWith(".json")
      ? JSON.parse(text)
      : text
          .trim()
          .split(/\r?\n/)
          .map((line) => JSON.parse(line));
    if (!Array.isArray(rows) || rows.length < 1 || rows.length > 100)
      throw new Error("每次导入 1～100 条样本");
    const imported = rows.map((row, i) => {
      if (!row || typeof row !== "object" || Array.isArray(row))
        throw new Error("第 " + (i + 1) + " 条样本格式无效");
      if (typeof row.input !== "string")
        throw new Error("第 " + (i + 1) + " 条样本缺少文本 input");
      const label = row.expected ?? row.expected_decision;
      return {
        id: "",
        name: String(row.name || row.id || "样本 " + (i + 1)),
        input: row.input,
        expected:
          row.include_in_accuracy === false
            ? "manual"
            : label === "allow"
              ? "allow"
              : ["flagged", "block"].includes(String(label))
                ? "flagged"
                : "manual",
        note: String(row.note || row.expected_basis || ""),
        revision: 0,
      };
    });
    const result = await call<{ imported: number }>(
      "/admin/evaluation/samples/import",
      "POST",
      { samples: imported },
    );
    await load();
    notice.value = "已导入 " + result.imported + " 条样本。";
  });
}
function newRun() {
  const p = props.policies[0];
  if (!p) return;
  runForm.value = {
    name: p.name + " · " + new Date().toLocaleDateString("zh-CN"),
    policy_id: p.id,
    channel_ids: [""],
    repetitions: 1,
    max_cost_cny: "5",
    prompt: p.config.prompt,
    threshold: p.config.threshold,
  };
}
function resetRunConfig() {
  if (runForm.value && policy.value) {
    runForm.value.prompt = policy.value.config.prompt;
    runForm.value.threshold = policy.value.config.threshold;
    runForm.value.channel_ids = [""];
  }
}
async function startRun() {
  await work(async () => {
    const f = runForm.value,
      p = policy.value;
    if (!f || !p) return;
    const result = await call<{ id: string }>(
      "/admin/evaluation/runs",
      "POST",
      {
        name: f.name,
        policy_id: f.policy_id,
        config: { ...p.config, prompt: f.prompt, threshold: f.threshold },
        sample_ids: selected.value,
        channel_ids: f.channel_ids,
        repetitions: f.repetitions,
        max_cost_cny: f.max_cost_cny,
      },
    );
    runForm.value = null;
    tab.value = "runs";
    await load();
    await showRun(result.id);
  });
}
async function cancelRun(run: Run) {
  await work(async () => {
    await call("/admin/evaluation/runs/" + run.id + "/cancel", "POST", {});
    await load();
    if (detail.value?.run.id === run.id) await showRun(run.id);
  });
}
async function viewSnapshot(row: Result) {
  await work(async () => {
    snapshot.value = await call<Sample>(
      "/admin/evaluation/runs/" +
        detail.value!.run.id +
        "/samples/" +
        row.sample_id,
    );
  });
}
function toggleAll() {
  selected.value =
    selected.value.length === samples.value.length
      ? []
      : samples.value.map((s) => s.id);
}
onMounted(() => {
  work(load);
  timer = setInterval(poll, 2000);
});
onBeforeUnmount(() => {
  disposed = true;
  abort.abort();
  if (timer) clearInterval(timer);
});
</script>

<template>
  <div class="evaluation-workspace">
    <div class="tabs" role="tablist" aria-label="审核评测">
      <button
        role="tab"
        :aria-selected="tab === 'samples'"
        :class="{ active: tab === 'samples' }"
        @click="
          tab = 'samples';
          detail = null;
        "
      >
        标注样本
      </button>
      <button
        role="tab"
        :aria-selected="tab === 'runs'"
        :class="{ active: tab === 'runs' }"
        @click="tab = 'runs'"
      >
        评测记录
      </button>
      <button
        class="icon-button eval-refresh"
        title="刷新评测"
        aria-label="刷新评测"
        :disabled="busy"
        @click="work(refresh)"
      >
        <AppIcon name="refresh" :size="16" />
      </button>
    </div>
    <p v-if="error" class="banner error" role="alert">{{ error }}</p>
    <p v-if="notice" class="banner success" role="status">{{ notice }}</p>
    <template v-if="tab === 'samples'">
      <div class="panel-heading">
        <span class="muted small"
          >{{ samples.length }} 个样本 · 已选 {{ selected.length }}</span
        >
        <div class="row">
          <button :disabled="busy" @click="importBuiltin">
            <AppIcon name="database" :size="16" />导入内置样本
          </button>
          <button :disabled="busy" @click="importInput?.click()">
            <AppIcon name="upload" :size="16" />导入 JSONL
          </button>
          <input
            ref="importInput"
            class="sr-only"
            type="file"
            accept=".json,.jsonl"
            aria-label="导入评测样本文件"
            @change="importSamples"
          />
          <button :disabled="busy" @click="addSample">
            <AppIcon name="plus" :size="16" />新增样本
          </button>
          <button
            class="primary"
            :disabled="
              busy || !selected.length || !policies.length || runs.some(active)
            "
            @click="newRun"
          >
            <AppIcon name="play" :size="16" />运行评测
          </button>
        </div>
      </div>
      <div class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>
                <input
                  type="checkbox"
                  aria-label="全选样本"
                  :checked="
                    samples.length > 0 && selected.length === samples.length
                  "
                  @change="toggleAll"
                />
              </th>
              <th>样本</th>
              <th>预期判定</th>
              <th>标注依据</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="sample in samples" :key="sample.id">
              <td>
                <input
                  v-model="selected"
                  type="checkbox"
                  :value="sample.id"
                  :aria-label="'选择 ' + sample.name"
                />
              </td>
              <td>
                <button class="text-button" @click="editSample(sample)">
                  {{ sample.name }}
                </button>
              </td>
              <td>
                <span
                  class="badge"
                  :class="
                    sample.expected === 'manual'
                      ? 'gray'
                      : sample.expected === 'allow'
                        ? 'green'
                        : 'amber'
                  "
                  >{{ expectedLabels[sample.expected] }}</span
                >
              </td>
              <td class="sample-note">{{ sample.note || "—" }}</td>
              <td>
                <div class="row">
                  <button
                    class="icon-button"
                    title="编辑样本"
                    :aria-label="'编辑 ' + sample.name"
                    :disabled="busy"
                    @click="editSample(sample)"
                  >
                    <AppIcon name="edit" :size="15" /></button
                  ><button
                    class="icon-button danger-button"
                    title="删除样本"
                    :aria-label="'删除 ' + sample.name"
                    :disabled="busy"
                    @click="deleteSample(sample)"
                  >
                    <AppIcon name="trash" :size="15" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="!samples.length" class="empty">暂无标注样本</p>
    </template>
    <template v-else-if="!detail">
      <div v-for="run in runs" :key="run.id" class="record-row">
        <div>
          <button class="text-button" @click="work(() => showRun(run.id))">
            {{ run.name }}
          </button>
          <p class="muted small">
            {{ run.policy_name }} · {{ time(run.created_at) }}
          </p>
          <small v-if="run.message" class="muted">{{ run.message }}</small>
        </div>
        <div class="row">
          <span
            class="badge"
            :class="
              run.status === 'completed'
                ? 'green'
                : active(run)
                  ? 'gray'
                  : 'amber'
            "
            >{{ statusLabels[run.status] || run.status }}</span
          ><span class="muted small">{{ run.completed }} / {{ run.total }}</span
          ><button v-if="active(run)" :disabled="busy" @click="cancelRun(run)">
            <AppIcon name="close" :size="15" />取消</button
          ><button
            class="icon-button"
            title="查看评测"
            :aria-label="'查看 ' + run.name"
            @click="work(() => showRun(run.id))"
          >
            <AppIcon name="chevron" :size="16" />
          </button>
        </div>
      </div>
      <p v-if="!runs.length" class="empty">暂无评测记录</p>
    </template>
    <template v-else>
      <div class="panel-heading">
        <div>
          <button class="text-button" @click="detail = null">
            <AppIcon name="back" :size="15" />评测记录
          </button>
          <h2>{{ detail.run.name }}</h2>
          <p class="muted small">
            {{ detail.run.policy_name }} · 配置修订
            {{ detail.policy_revision }} · 预算 ¥{{ detail.run.max_cost_cny }}
          </p>
        </div>
        <div class="row">
          <span
            class="badge"
            :class="
              detail.run.status === 'completed'
                ? 'green'
                : active(detail.run)
                  ? 'gray'
                  : 'amber'
            "
            >{{ statusLabels[detail.run.status] || detail.run.status }}</span
          ><button
            v-if="active(detail.run)"
            :disabled="busy"
            @click="cancelRun(detail.run)"
          >
            <AppIcon name="close" :size="16" />取消评测</button
          ><button
            class="icon-button"
            title="导出评测 CSV"
            aria-label="导出评测 CSV"
            @click="
              work(() =>
                downloadFile(
                  '/admin/evaluation/runs/' + detail!.run.id + '/export',
                  'evaluation-' + detail!.run.id + '.csv',
                  abort.signal,
                ),
              )
            "
          >
            <AppIcon name="download" :size="16" />
          </button>
        </div>
      </div>
      <p v-if="detail.run.message" class="hint">{{ detail.run.message }}</p>
      <div class="metric-grid evaluation-metrics">
        <div class="metric">
          <span class="metric-label">已完成</span
          ><strong
            >{{ detail.run.completed
            }}<small> / {{ detail.run.total }}</small></strong
          >
        </div>
        <div class="metric">
          <span class="metric-label">有效结果</span
          ><strong>{{ totals.valid }}</strong>
        </div>
        <div class="metric">
          <span class="metric-label">请求失败</span
          ><strong>{{ totals.errors }}</strong>
        </div>
        <div class="metric">
          <span class="metric-label">待核对费用</span
          ><strong>{{ totals.pending }}<small> 笔</small></strong>
        </div>
      </div>
      <section class="panel">
        <div class="panel-heading"><h2>通道对比</h2></div>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>评测目标</th>
                <th>判定正确率</th>
                <th>误报率</th>
                <th>漏判率</th>
                <th>请求失败率</th>
                <th>重复判定不一致</th>
                <th>平均耗时</th>
                <th>已知费用</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="score in detail.scores" :key="score.target">
                <td>{{ detail.targets[score.target] || score.target }}</td>
                <td>
                  {{ ratio(score.correct, score.labelled)
                  }}<small
                    >{{ score.correct }} /
                    {{ score.labelled }} 个有效标注结果</small
                  >
                </td>
                <td>
                  {{ ratio(score.false_positives, score.allow_samples)
                  }}<small
                    >{{ score.false_positives }} /
                    {{ score.allow_samples }} 个应放行结果</small
                  >
                </td>
                <td>
                  {{ ratio(score.false_negatives, score.flagged_samples)
                  }}<small
                    >{{ score.false_negatives }} /
                    {{ score.flagged_samples }} 个应命中结果</small
                  >
                </td>
                <td>
                  {{ ratio(score.errors, score.processed)
                  }}<small
                    >{{ score.errors }} / {{ score.processed }} 次已执行</small
                  >
                </td>
                <td>{{ score.unstable_samples }} 个样本</td>
                <td>{{ latency(score.average_latency_ms) }}</td>
                <td>
                  ¥{{ score.known_cost_cny
                  }}<small v-if="score.pending_costs"
                    >{{ score.pending_costs }} 笔待核对</small
                  >
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
      <section class="panel">
        <div class="panel-heading">
          <h2>样本结果</h2>
          <label class="sr-only" for="eval-result-filter">结果筛选</label
          ><select
            id="eval-result-filter"
            v-model="resultFilter"
            class="result-filter"
          >
            <option value="all">全部结果</option>
            <option value="wrong">仅误判</option>
            <option value="errors">仅失败</option>
          </select>
        </div>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>样本</th>
                <th>通道 / 轮次</th>
                <th>预期</th>
                <th>实际判定</th>
                <th>评分 / 阈值</th>
                <th>耗时</th>
                <th>审核记录</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in visibleResults" :key="row.sequence">
                <td>
                  <button class="text-button" @click="viewSnapshot(row)">
                    {{ row.sample_name }}
                  </button>
                </td>
                <td>
                  {{ detail.targets[row.target] || row.target
                  }}<small
                    >第 {{ row.iteration }} 轮 ·
                    {{ row.model || "未调用" }}</small
                  >
                </td>
                <td>{{ expectedLabels[row.expected] }}</td>
                <td>
                  <span
                    v-if="
                      row.status === 'completed' &&
                      (row.confidence != null || row.keyword_ignored)
                    "
                    class="badge"
                    :class="
                      row.expected !== 'manual' &&
                      row.flagged !== (row.expected === 'flagged')
                        ? 'red'
                        : 'green'
                    "
                    >{{
                      row.keyword_ignored
                        ? "关键词忽略"
                        : row.flagged
                          ? "命中"
                          : "放行"
                    }}</span
                  ><span v-else class="badge gray">{{
                    statusLabels[row.status] || row.status
                  }}</span
                  ><small :title="row.error_message">{{
                    row.error_code || row.reason
                  }}</small>
                </td>
                <td :title="String(row.confidence)">
                  {{ row.confidence == null ? "—" : row.confidence.toFixed(3) }}
                  / {{ row.threshold ?? "—" }}
                </td>
                <td>
                  {{
                    row.status === "pending" || row.status === "skipped"
                      ? "—"
                      : latency(row.latency_ms)
                  }}
                </td>
                <td>
                  <button
                    v-if="row.request_id"
                    class="icon-button"
                    title="查看审核记录"
                    :aria-label="'查看请求 ' + row.request_id"
                    @click="emit('log', row.request_id)"
                  >
                    <AppIcon name="file" :size="15" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
      <details class="evaluation-config">
        <summary>评测配置快照</summary>
        <p class="muted small">
          阈值 {{ detail.config.threshold }} · 总调用时限
          {{ detail.config.total_timeout_ms }} ms
        </p>
        <pre class="input-detail">{{ detail.config.prompt }}</pre>
      </details>
    </template>

    <div v-if="editor" class="modal-backdrop" @click.self="editor = null">
      <section
        class="modal wide"
        role="dialog"
        aria-modal="true"
        aria-label="标注样本"
      >
        <div class="panel-heading">
          <h2>{{ editor.id ? "编辑样本" : "新增样本" }}</h2>
          <button aria-label="关闭样本" @click="editor = null">
            <AppIcon name="close" />
          </button>
        </div>
        <form class="stack-form" @submit.prevent="saveSample">
          <div class="form-grid">
            <label
              >样本名称<input
                v-model="editor.name"
                required
                maxlength="200" /></label
            ><label
              >预期判定<select v-model="editor.expected">
                <option value="allow">应放行</option>
                <option value="flagged">应命中</option>
                <option value="manual">待人工判断</option>
              </select></label
            >
          </div>
          <label
            >样本内容<textarea
              v-model="editor.input"
              required
              rows="8"
              maxlength="64000"
            /></label
          ><label
            >标注依据<textarea
              v-model="editor.note"
              rows="2"
              maxlength="1000"
            />
          </label>
          <p v-if="error" class="error">{{ error }}</p>
          <button class="primary" :disabled="busy">
            <AppIcon name="save" :size="16" />保存样本
          </button>
        </form>
      </section>
    </div>
    <div v-if="runForm" class="modal-backdrop" @click.self="runForm = null">
      <section
        class="modal wide"
        role="dialog"
        aria-modal="true"
        aria-label="运行评测"
      >
        <div class="panel-heading">
          <h2>运行评测</h2>
          <button aria-label="关闭评测配置" @click="runForm = null">
            <AppIcon name="close" />
          </button>
        </div>
        <form class="stack-form" @submit.prevent="startRun">
          <div class="form-grid">
            <label
              >评测名称<input
                v-model="runForm.name"
                required
                maxlength="200" /></label
            ><label
              >审核策略<select
                v-model="runForm.policy_id"
                @change="resetRunConfig"
              >
                <option v-for="p in policies" :key="p.id" :value="p.id">
                  {{ p.name }}
                </option>
              </select></label
            ><label
              >重复轮次<input
                v-model.number="runForm.repetitions"
                type="number"
                min="1"
                max="3"
                required /></label
            ><label
              >费用上限（元）<input
                v-model="runForm.max_cost_cny"
                inputmode="decimal"
                required
            /></label>
          </div>
          <fieldset>
            <legend>评测通道</legend>
            <label class="check-row"
              ><input
                v-model="runForm.channel_ids"
                type="checkbox"
                value=""
                :disabled="
                  runForm.channel_ids.length >= 4 &&
                  !runForm.channel_ids.includes('')
                "
              />按策略调度</label
            ><label v-for="c in targets" :key="c.id" class="check-row"
              ><input
                v-model="runForm.channel_ids"
                type="checkbox"
                :value="c.id"
                :disabled="
                  runForm.channel_ids.length >= 4 &&
                  !runForm.channel_ids.includes(c.id)
                "
              />{{ c.name }} · {{ c.model }}</label
            >
          </fieldset>
          <label
            >评测阈值<input
              v-model.number="runForm.threshold"
              type="number"
              min="0"
              max="1"
              step="0.01"
              required /></label
          ><label
            >评测提示词<textarea
              v-model="runForm.prompt"
              required
              rows="7"
              maxlength="64000"
            />
          </label>
          <p class="hint">
            {{ selected.length }} 个样本 · {{ planned }} 次评测 · 最多
            {{ maxCalls }}
            次模型调用。预算不足或费用待核对时停止后续评测；已发送调用按上游实际用量结算。
          </p>
          <p v-if="planned > 200" class="error">每次最多 200 次评测。</p>
          <p v-if="selected.length > 100" class="error">
            每次最多选择 100 个样本。
          </p>
          <p v-if="error" class="error">{{ error }}</p>
          <button
            class="primary"
            :disabled="
              busy ||
              planned < 1 ||
              planned > 200 ||
              selected.length > 100 ||
              !runForm.channel_ids.length
            "
          >
            <AppIcon name="play" :size="16" />开始评测
          </button>
        </form>
      </section>
    </div>
    <div v-if="snapshot" class="modal-backdrop" @click.self="snapshot = null">
      <section
        class="modal wide"
        role="dialog"
        aria-modal="true"
        aria-label="评测样本快照"
      >
        <div class="panel-heading">
          <h2>{{ snapshot.name }}</h2>
          <button aria-label="关闭样本快照" @click="snapshot = null">
            <AppIcon name="close" />
          </button>
        </div>
        <p class="muted">
          {{ expectedLabels[snapshot.expected] }} · {{ snapshot.note }}
        </p>
        <pre class="input-detail">{{ snapshot.input }}</pre>
      </section>
    </div>
  </div>
</template>

<style scoped>
.evaluation-workspace > .tabs {
  margin-top: 0;
}
.eval-refresh {
  margin-left: auto;
}
.evaluation-workspace > .banner {
  margin-top: 20px;
}
.evaluation-metrics {
  margin: 8px 0 24px;
}
.evaluation-metrics strong small {
  font-size: 13px;
}
.sample-note {
  max-width: 420px;
  white-space: normal;
  overflow-wrap: anywhere;
}
.result-filter {
  width: 130px;
  font-size: 12px;
}
.evaluation-config {
  border-top: 1px solid var(--border);
  padding: 18px 0;
}
.evaluation-config summary {
  cursor: pointer;
  font-size: 12px;
  margin-bottom: 15px;
}
.evaluation-config pre {
  max-height: 350px;
  overflow: auto;
}
.record-row > div {
  min-width: 0;
}
.record-row .text-button {
  text-align: left;
  overflow-wrap: anywhere;
}
.modal .form-grid {
  padding: 0;
}
.modal textarea {
  max-height: 460px;
}
</style>
