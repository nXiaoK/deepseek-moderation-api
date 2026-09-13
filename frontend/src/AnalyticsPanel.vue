<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { api, APIError } from "./api";

interface Metrics {
  records: number;
  calls: number;
  cache_hits: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  unknown_usage: number;
  known_cost_cny: string;
  estimated_cost_cny: string;
  reserved_cny: string;
  pending_costs: number;
  avg_latency_ms: number | null;
  latency_samples: number;
  output_tokens_per_second: number | null;
}
interface ModelMetrics extends Metrics {
  model: string;
}
interface Point extends Metrics {
  time: string;
}
interface Analytics {
  from: string;
  to: string;
  kind: string;
  model: string;
  interval_seconds: number;
  available_models: string[];
  summary: Metrics;
  models: ModelMetrics[];
  series: Point[];
}
const emit = defineEmits<{ unauthorized: [] }>();
const presets = [
  { value: "1h", label: "近 1 小时" },
  { value: "24h", label: "24 小时" },
  { value: "48h", label: "48 小时" },
  { value: "7d", label: "7 天" },
  { value: "30d", label: "30 天" },
  { value: "custom", label: "自定义" },
];
const metricOptions = [
  { value: "total_tokens", label: "Token 消耗" },
  { value: "known_cost_cny", label: "已知费用" },
  { value: "avg_latency_ms", label: "平均耗时" },
  { value: "output_tokens_per_second", label: "输出速度" },
] as const;
type ChartMetric = (typeof metricOptions)[number]["value"];
const range = ref("24h"),
  model = ref(""),
  kind = ref("production"),
  busy = ref(false),
  error = ref("");
const data = ref<Analytics | null>(null),
  metric = ref<ChartMetric>("total_tokens");
const beijingInput = (ms: number) =>
  new Date(ms + 8 * 3600000).toISOString().slice(0, 16);
const from = ref(beijingInput(Date.now() - 86400000)),
  to = ref(beijingInput(Date.now()));
const count = (value: number) => value.toLocaleString("zh-CN");
const money = (value: string) => `¥${value}`;
const latency = (value: number | null) =>
  value == null
    ? "—"
    : value < 1000
      ? `${value.toFixed(0)} ms`
      : `${(value / 1000).toFixed(2)} s`;
const speed = (value: number | null) =>
  value == null ? "—" : `${value.toFixed(2)} token/s`;
const time = (value: string) =>
  new Date(value).toLocaleString("zh-CN", {
    timeZone: "Asia/Shanghai",
    hour12: false,
  });
const interval = computed(() =>
  data.value?.interval_seconds === 60
    ? "分钟"
    : data.value?.interval_seconds === 3600
      ? "小时"
      : "天",
);
async function load() {
  if (busy.value) return;
  error.value = "";
  const q = new URLSearchParams({
    range: range.value,
    model: model.value,
    kind: kind.value,
  });
  if (range.value === "custom") {
    const start = new Date(`${from.value}+08:00`),
      end = new Date(`${to.value}+08:00`);
    if (
      !Number.isFinite(start.getTime()) ||
      !Number.isFinite(end.getTime()) ||
      end <= start ||
      end.getTime() - start.getTime() > 366 * 86400000
    ) {
      error.value = "请选择有效的起止时间，结束须晚于开始，范围最多 366 天。";
      return;
    }
    q.set("from", start.toISOString());
    q.set("to", end.toISOString());
  }
  busy.value = true;
  try {
    data.value = await api<Analytics>(`/admin/analytics?${q}`);
  } catch (e) {
    error.value = e instanceof Error ? e.message : "分析数据加载失败";
    if (e instanceof APIError && e.status === 401) emit("unauthorized");
  } finally {
    busy.value = false;
  }
}
async function selectRange(value: string) {
  range.value = value;
  if (value !== "custom") await load();
}
async function selectModel(value: string) {
  model.value = value;
  await load();
}
const chart = computed(() => {
  const series = data.value?.series || [];
  const values = series.map((p) =>
    p[metric.value] == null ? null : Number(p[metric.value]),
  );
  const max = Math.max(0, ...values.filter((v): v is number => v != null));
  const ceiling = max || 1;
  const x = (i: number) => 78 + (i * 670) / Math.max(1, series.length - 1);
  const y = (v: number) => 190 - (v / ceiling) * 160;
  const paths: string[] = [];
  let path: string[] = [];
  values.forEach((v, i) => {
    if (v == null) {
      if (path.length) paths.push(path.join(" "));
      path = [];
    } else path.push(`${x(i)},${y(v)}`);
  });
  if (path.length) paths.push(path.join(" "));
  return {
    series,
    values,
    ceiling,
    x,
    y,
    paths,
    ticks: [
      ...new Set([0, Math.floor((series.length - 1) / 2), series.length - 1]),
    ].filter((i) => i >= 0),
  };
});
const chartValue = (v: number | null) =>
  v == null
    ? "无耗时样本"
    : metric.value === "avg_latency_ms"
      ? latency(v)
      : metric.value === "output_tokens_per_second"
        ? speed(v)
        : (metric.value === "known_cost_cny" ? "¥" : "") +
          v.toLocaleString("zh-CN", { maximumSignificantDigits: 5 });
const bucketLabel = (value: string) =>
  new Date(value).toLocaleString("zh-CN", {
    timeZone: "Asia/Shanghai",
    month: "2-digit",
    day: "2-digit",
    ...(data.value?.interval_seconds !== 86400
      ? { hour: "2-digit" as const, minute: "2-digit" as const, hour12: false }
      : {}),
  });
onMounted(load);
</script>

<template>
  <div class="analytics-panel" :aria-busy="busy">
    <div v-if="error" class="banner error" role="alert">{{ error }}</div>
    <section class="panel">
      <div class="panel-heading">
        <div>
          <h2>模型数据分析</h2>
          <p class="muted small">按模型汇总用量、费用和响应速度。</p>
        </div>
        <button @click="load" :disabled="busy">
          {{ busy ? "加载中…" : "刷新数据" }}
        </button>
      </div>
      <div class="analytics-presets" aria-label="统计时间范围">
        <button
          v-for="p in presets"
          :key="p.value"
          :class="{ primary: range === p.value }"
          :aria-pressed="range === p.value"
          :disabled="busy"
          @click="selectRange(p.value)"
        >
          {{ p.label }}
        </button>
      </div>
      <form class="analytics-filters" @submit.prevent="load">
        <label
          >模型<select v-model="model" :disabled="busy" @change="load">
            <option value="">全部模型</option>
            <option
              v-if="model && !data?.available_models.includes(model)"
              :value="model"
            >
              {{ model }}
            </option>
            <option
              v-for="name in data?.available_models || []"
              :key="name"
              :value="name"
            >
              {{ name }}
            </option>
          </select></label
        >
        <label
          >来源<select v-model="kind" :disabled="busy" @change="load">
            <option value="production">正式请求</option>
            <option value="test">后台试跑</option>
            <option value="all">全部</option>
          </select></label
        >
        <template v-if="range === 'custom'"
          ><label
            >开始时间（北京时间）<input
              v-model="from"
              type="datetime-local"
              required
              :disabled="busy" /></label
          ><label
            >结束时间（北京时间）<input
              v-model="to"
              type="datetime-local"
              required
              :disabled="busy" /></label
          ><button class="primary" :disabled="busy">应用范围</button></template
        >
      </form>
      <p v-if="data" class="muted small">
        {{ time(data.from) }} 至 {{ time(data.to) }}（不含结束时刻） · 按{{
          interval
        }}展示趋势
      </p>
    </section>

    <template v-if="data">
      <div class="metric-grid analytics-metrics">
        <section class="metric">
          <span>模型 Token 消耗</span
          ><strong>{{ count(data.summary.total_tokens) }}</strong
          ><small
            >输入 {{ count(data.summary.input_tokens) }} · 输出
            {{ count(data.summary.output_tokens) }}</small
          ><small v-if="data.summary.unknown_usage"
            >另有 {{ count(data.summary.unknown_usage) }} 次调用用量未知</small
          >
        </section>
        <section class="metric">
          <span>已知费用（含估算）</span
          ><strong>{{ money(data.summary.known_cost_cny) }}</strong
          ><small>其中估算 {{ money(data.summary.estimated_cost_cny) }}</small
          ><small v-if="data.summary.pending_costs"
            >另有
            {{ count(data.summary.pending_costs) }} 笔待核对，未计入金额</small
          >
        </section>
        <section class="metric">
          <span>平均调用耗时</span
          ><strong>{{ latency(data.summary.avg_latency_ms) }}</strong
          ><small
            >{{ count(data.summary.latency_samples) }} 次耗时样本 ·
            不含本地缓存</small
          >
        </section>
        <section class="metric">
          <span>平均输出速度</span
          ><strong>{{ speed(data.summary.output_tokens_per_second) }}</strong
          ><small>输出 token ÷ 对应调用总耗时</small>
        </section>
      </div>
      <p class="muted small">
        实际模型调用 {{ count(data.summary.calls) }} 次 · 本地缓存命中
        {{ count(data.summary.cache_hits) }}
        次。重试分别计入对应模型；同名模型跨通道合计。
      </p>

      <section class="panel">
        <div class="panel-heading">
          <h2>用量趋势</h2>
          <div class="row">
            <button
              v-for="m in metricOptions"
              :key="m.value"
              :class="{ primary: metric === m.value }"
              :aria-pressed="metric === m.value"
              @click="metric = m.value"
            >
              {{ m.label }}
            </button>
          </div>
        </div>
        <div v-if="!data.summary.records" class="empty">
          所选范围没有模型调用或费用记录。
        </div>
        <div
          v-else-if="chart.values.every((value) => value == null)"
          class="empty"
        >
          所选范围暂无可用的速度或耗时样本。
        </div>
        <div v-else class="analytics-chart">
          <svg
            viewBox="0 0 780 235"
            role="img"
            :aria-label="`${metricOptions.find((m) => m.value === metric)?.label}，按${interval}统计`"
          >
            <g v-for="ratio in [0, 0.5, 1]" :key="ratio">
              <line
                x1="78"
                x2="748"
                :y1="chart.y(ratio * chart.ceiling)"
                :y2="chart.y(ratio * chart.ceiling)"
                class="chart-grid"
              />
              <text
                x="68"
                :y="chart.y(ratio * chart.ceiling) + 4"
                text-anchor="end"
              >
                {{ chartValue(ratio * chart.ceiling) }}
              </text>
            </g>
            <polyline
              v-for="(path, i) in chart.paths"
              :key="i"
              :points="path"
              class="chart-line"
            />
            <template v-for="(point, i) in chart.series" :key="point.time">
              <circle
                v-if="chart.values[i] != null"
                :cx="chart.x(i)"
                :cy="chart.y(chart.values[i]!)"
                r="3"
                class="chart-point"
              >
                <title>
                  {{ time(point.time) }} · {{ chartValue(chart.values[i]) }} ·
                  {{ point.calls }} 次调用
                </title>
              </circle>
            </template>
            <text
              v-for="i in chart.ticks"
              :key="i"
              :x="chart.x(i)"
              y="218"
              :text-anchor="
                i === 0
                  ? 'start'
                  : i === chart.series.length - 1
                    ? 'end'
                    : 'middle'
              "
            >
              {{ bucketLabel(chart.series[i].time) }}
            </text>
          </svg>
        </div>
        <p class="hint">
          耗时含失败调用，速度按整次调用耗时计算（含请求准备与响应等待），不是首字延迟或纯生成速度。缺少耗时的历史记录不计入速度平均值；未知用量和待核对费用不按
          0 估算。
        </p>
      </section>

      <section class="panel">
        <div class="panel-heading">
          <h2>模型对比</h2>
          <button v-if="model" :disabled="busy" @click="selectModel('')">
            查看全部模型
          </button>
        </div>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>模型</th>
                <th>调用 / 缓存</th>
                <th>Token 消耗</th>
                <th>已知费用</th>
                <th>平均耗时</th>
                <th>输出速度</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in data.models" :key="row.model">
                <td>
                  <button
                    class="text-button"
                    :disabled="busy"
                    @click="selectModel(row.model)"
                  >
                    {{ row.model }}
                  </button>
                </td>
                <td>
                  {{ count(row.calls)
                  }}<small>缓存 {{ count(row.cache_hits) }}</small>
                </td>
                <td>
                  {{ count(row.total_tokens)
                  }}<small
                    >输入 {{ count(row.input_tokens) }} / 输出
                    {{ count(row.output_tokens) }}</small
                  ><small v-if="row.unknown_usage"
                    >{{ row.unknown_usage }} 次用量未知</small
                  >
                </td>
                <td>
                  {{ money(row.known_cost_cny)
                  }}<small v-if="row.pending_costs"
                    >{{ row.pending_costs }} 笔待核对</small
                  ><small v-if="row.estimated_cost_cny !== '0'"
                    >含估算 {{ money(row.estimated_cost_cny) }}</small
                  >
                </td>
                <td>
                  {{ latency(row.avg_latency_ms)
                  }}<small>{{ row.latency_samples }} 次样本</small>
                </td>
                <td>{{ speed(row.output_tokens_per_second) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="!data.models.length" class="empty">没有符合条件的模型记录。</p>
      </section>

      <details v-if="data.summary.records" class="panel">
        <summary>查看每{{ interval }}明细</summary>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>时间（北京时间）</th>
                <th>模型调用</th>
                <th>Token</th>
                <th>已知费用</th>
                <th>平均耗时</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in data.series" :key="p.time">
                <td>{{ bucketLabel(p.time) }}</td>
                <td>{{ p.calls }}</td>
                <td>
                  {{ count(p.total_tokens)
                  }}<small v-if="p.unknown_usage"
                    >{{ p.unknown_usage }} 次用量未知</small
                  >
                </td>
                <td>
                  {{ money(p.known_cost_cny)
                  }}<small v-if="p.pending_costs"
                    >{{ p.pending_costs }} 笔待核对</small
                  >
                </td>
                <td>{{ latency(p.avg_latency_ms) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </details>
    </template>
  </div>
</template>

<style scoped>
.analytics-panel {
  display: grid;
  gap: 20px;
}
.analytics-panel > .panel {
  margin-bottom: 0;
}
.analytics-panel .panel-heading {
  flex-wrap: wrap;
}
.analytics-panel > .panel > .muted,
.analytics-panel > .panel > .hint {
  margin: 16px 24px;
}
.analytics-presets {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 20px 24px;
}
.analytics-filters {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  align-items: end;
  margin: 0 24px 16px;
}
.analytics-filters label {
  min-width: 180px;
  flex: 1;
}
.analytics-metrics {
  margin: 0;
}
.analytics-metrics strong {
  font-size: clamp(20px, 2vw, 30px);
  overflow-wrap: anywhere;
}
.analytics-chart {
  width: 100%;
  overflow-x: auto;
}
.analytics-chart svg {
  display: block;
  width: 100%;
  min-width: 540px;
}
.analytics-chart text {
  fill: currentColor;
  font-size: 10px;
  opacity: 0.7;
}
.chart-grid {
  stroke: currentColor;
  opacity: 0.12;
}
.chart-line {
  fill: none;
  stroke: #3e6d51;
  stroke-width: 2.5;
}
.chart-point {
  fill: #3e6d51;
}
summary {
  cursor: pointer;
  font-weight: 600;
  padding: 20px 24px;
}
</style>
