<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import type { EChartsCoreOption } from "echarts/core";
import AppIcon from "./AppIcon.vue";
import DataChart from "./DataChart.vue";
import {
  api,
  APIError,
  type Policy,
  type ClientKey,
  type ModelChannel,
  type AnalysisLogFilter,
} from "./api";
defineProps<{
  policies: Policy[];
  clients: ClientKey[];
  channels: ModelChannel[];
}>();

interface Metrics {
  successful_calls: number;
  failed_calls: number;
  outcome_samples: number;
  p50_latency_ms: number | null;
  p95_latency_ms: number | null;
  p99_latency_ms: number | null;
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
  policy_id: string;
  client_id: string;
  channel_id: string;
  errors: { code: string; calls: number }[];
  requests?: {
    records: number;
    allowed: number;
    flagged: number;
    errors: number;
    fallbacks: number;
    p50_latency_ms: number | null;
    p95_latency_ms: number | null;
    p99_latency_ms: number | null;
  };
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
const emit = defineEmits<{
  unauthorized: [];
  logs: [filter: AnalysisLogFilter];
}>();
const presets = [
  { value: "1h", label: "1 小时" },
  { value: "24h", label: "24 小时" },
  { value: "48h", label: "48 小时" },
  { value: "7d", label: "7 天" },
  { value: "30d", label: "30 天" },
  { value: "custom", label: "自定义" },
];
const metricOptions = [
  {
    value: "total_tokens",
    label: "Token 用量",
    icon: "database",
    color: "#3565e8",
  },
  {
    value: "known_cost_cny",
    label: "已知费用",
    icon: "coins",
    color: "#149d8c",
  },
  {
    value: "avg_latency_ms",
    label: "平均耗时",
    icon: "clock",
    color: "#c48929",
  },
  {
    value: "output_tokens_per_second",
    label: "输出速度",
    icon: "zap",
    color: "#b47698",
  },
] as const;
type ChartMetric = (typeof metricOptions)[number]["value"];
type ShareMetric = "total_tokens" | "calls" | "known_cost_cny";
const palette = [
  "#3565e8",
  "#24ac9a",
  "#e4af54",
  "#be81a1",
  "#72b7dc",
  "#84928c",
];
const range = ref("24h"),
  model = ref(""),
  kind = ref("production");
const policyID = ref(""),
  clientID = ref(""),
  channelID = ref("");
const percent = (value: number | undefined, total: number | undefined) =>
  total ? (((value || 0) / total) * 100).toFixed(1) + "%" : "—";
function logsFor(
  modelName = data.value?.model || "",
  code = "",
  bucket?: string,
) {
  const d = data.value;
  if (!d) return;
  let start = d.from,
    end = d.to;
  if (bucket && Number.isFinite(Date.parse(bucket))) {
    const at = Date.parse(bucket);
    start = new Date(Math.max(at, Date.parse(d.from))).toISOString();
    end = new Date(
      Math.min(at + d.interval_seconds * 1000, Date.parse(d.to)),
    ).toISOString();
  }
  emit("logs", {
    from: start,
    to: end,
    kind: d.kind,
    model: modelName,
    policy_id: d.policy_id || "",
    client_id: d.client_id || "",
    channel_id: d.channel_id || "",
    error_code: code,
  });
}
const busy = ref(false),
  error = ref(""),
  data = ref<Analytics | null>(null);
const availableModels = ref<string[]>([]);
const metric = ref<ChartMetric>("total_tokens"),
  chartType = ref<"line" | "bar">("line");
const shareMetric = ref<ShareMetric>("total_tokens");
const trend = ref<InstanceType<typeof DataChart>>();
const beijingInput = (ms: number) =>
  new Date(ms + 8 * 3600000).toISOString().slice(0, 16);
const from = ref(beijingInput(Date.now() - 86400000)),
  to = ref(beijingInput(Date.now()));
const count = (value: number) => value.toLocaleString("zh-CN");
const compact = (value: number) =>
  value.toLocaleString("en-US", {
    notation: "compact",
    maximumFractionDigits: 1,
  });
const money = (value: string) => `¥${value}`;
const latency = (value: number | null) =>
  value == null
    ? "—"
    : value < 1000
      ? `${value.toFixed(0)} ms`
      : `${(value / 1000).toFixed(2)} s`;
const speed = (value: number | null) =>
  value == null ? "—" : `${value.toFixed(2)}`;
const time = (value: string) =>
  new Date(value).toLocaleString("zh-CN", {
    timeZone: "Asia/Shanghai",
    hour12: false,
  });
const bucketLabel = (value: string, short = false) =>
  new Date(value).toLocaleString("zh-CN", {
    timeZone: "Asia/Shanghai",
    ...(!short || data.value?.interval_seconds === 86400
      ? { month: "2-digit" as const, day: "2-digit" as const }
      : {}),
    ...(data.value?.interval_seconds !== 86400
      ? { hour: "2-digit" as const, minute: "2-digit" as const, hour12: false }
      : {}),
  });
const interval = computed(() =>
  data.value?.interval_seconds === 60
    ? "分钟"
    : data.value?.interval_seconds === 3600
      ? "小时"
      : "天",
);
const currentMetric = computed(
  () => metricOptions.find((m) => m.value === metric.value)!,
);
const chartValues = computed(() =>
  (data.value?.series || []).map((p) =>
    p[metric.value] == null ? null : Number(p[metric.value]),
  ),
);
const hasSamples = computed(() => chartValues.value.some((v) => v != null));
const formatMetric = (value: number | null) =>
  value == null
    ? "无耗时样本"
    : metric.value === "avg_latency_ms"
      ? latency(value)
      : metric.value === "output_tokens_per_second"
        ? `${speed(value)} token/s`
        : metric.value === "known_cost_cny"
          ? `¥${value.toLocaleString("zh-CN", { maximumFractionDigits: 12 })}`
          : count(value);
async function load() {
  if (busy.value) return;
  error.value = "";
  const q = new URLSearchParams({
    range: range.value,
    model: model.value,
    kind: kind.value,
    policy_id: policyID.value,
    client_id: clientID.value,
    channel_id: channelID.value,
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
  data.value = null;
  try {
    const result = await api<Analytics>(`/admin/analytics?${q}`);
    data.value = result;
    availableModels.value = result.available_models;
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
  if (busy.value) return;
  model.value = value;
  await load();
}
const trendOption = computed<EChartsCoreOption>(() => {
  const series = data.value?.series || [];
  const tokens = metric.value === "total_tokens";
  const plotSeries = tokens
    ? [
        {
          name: "总 Token",
          color: palette[0],
          values: series.map((p) => p.total_tokens),
        },
        {
          name: "输入 Token",
          color: palette[1],
          values: series.map((p) => p.input_tokens),
        },
        {
          name: "输出 Token",
          color: palette[2],
          values: series.map((p) => p.output_tokens),
        },
      ]
    : [
        {
          name: currentMetric.value.label,
          color: currentMetric.value.color,
          values: chartValues.value,
        },
      ];
  return {
    tooltip: {
      trigger: "axis",
      confine: true,
      renderMode: "richText",
      backgroundColor: "#fff",
      borderColor: "#e7e9ef",
      padding: 12,
      textStyle: { color: "#4d5666", fontSize: 12 },
      axisPointer: {
        type: chartType.value === "bar" ? "shadow" : "line",
        lineStyle: { color: "#b6c5e4", type: "dashed" },
      },
      valueFormatter: (value: number | null) => formatMetric(value),
    },
    legend: {
      show: tokens,
      top: 0,
      left: 0,
      icon: "circle",
      itemWidth: 7,
      itemHeight: 7,
      itemGap: 22,
      textStyle: { color: "#8590a2", fontSize: 11 },
    },
    grid: {
      left: 4,
      right: 15,
      top: 42,
      bottom: 56,
      outerBoundsMode: "same",
      outerBoundsContain: "axisLabel",
    },
    xAxis: {
      type: "category",
      data: series.map((p) => p.time),
      boundaryGap: chartType.value === "bar",
      axisLine: { lineStyle: { color: "#e7ecf4" } },
      axisTick: { show: false },
      axisPointer: {
        label: {
          formatter: ({ value }: { value: string }) => bucketLabel(value),
        },
      },
      axisLabel: {
        color: "#939cad",
        fontSize: 10,
        hideOverlap: true,
        margin: 15,
        formatter: (value: string) => bucketLabel(value, true),
      },
    },
    yAxis: {
      type: "value",
      min: 0,
      splitNumber: 4,
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { lineStyle: { color: "#edf0f5", type: "dashed" } },
      axisLabel: {
        color: "#939cad",
        fontSize: 10,
        formatter: (v: number) =>
          metric.value === "known_cost_cny"
            ? `¥${v !== 0 && Math.abs(v) < 1 ? v.toLocaleString("zh-CN", { maximumSignificantDigits: 3 }) : compact(v)}`
            : metric.value === "avg_latency_ms"
              ? latency(v)
              : compact(v),
      },
    },
    dataZoom: [
      {
        type: "slider",
        height: 18,
        bottom: 0,
        left: 12,
        right: 12,
        handleIcon: "roundRect",
        handleSize: 16,
        borderColor: "transparent",
        backgroundColor: "#f6f8fc",
        fillerColor: "#dce6fc66",
        showDetail: false,
        dataBackground: {
          lineStyle: { color: "#c3d1ee" },
          areaStyle: { color: "#e7edf9" },
        },
        selectedDataBackground: {
          lineStyle: { color: "#99b4ed" },
          areaStyle: { color: "#d5e1fb" },
        },
        handleStyle: { color: "#fff", borderColor: "#b5c6e9" },
        moveHandleSize: 0,
      },
    ],
    series: plotSeries.map((s) => ({
      name: s.name,
      type: chartType.value,
      data: s.values,
      smooth: 0.22,
      smoothMonotone: "x",
      connectNulls: false,
      showSymbol:
        series.length === 1 || s.values.some((value) => value == null),
      symbol: "circle",
      symbolSize: 6,
      lineStyle: { width: 2.5, color: s.color },
      itemStyle: {
        color: s.color,
        borderRadius: chartType.value === "bar" ? [3, 3, 0, 0] : undefined,
      },
      areaStyle:
        chartType.value === "line" && (!tokens || s.name === "总 Token")
          ? { color: s.color, opacity: 0.07 }
          : undefined,
      barMaxWidth: 22,
      emphasis: { focus: "series" },
    })),
  };
});
const shares = computed(() =>
  (data.value?.models || [])
    .map((m, i) => ({
      name: m.model,
      value: Number(m[shareMetric.value]),
      color: palette[i % palette.length],
    }))
    .sort((a, b) => b.value - a.value),
);
const shareTotal = computed(() =>
  shares.value.reduce((sum, row) => sum + row.value, 0),
);
const shareLabel = computed(
  () =>
    ({ total_tokens: "Token", calls: "调用", known_cost_cny: "已知费用" })[
      shareMetric.value
    ],
);
const shareOption = computed<EChartsCoreOption>(() => ({
  tooltip: {
    trigger: "item",
    renderMode: "richText",
    confine: true,
    borderColor: "#e7e9ef",
    textStyle: { fontSize: 12, color: "#4d5666" },
  },
  series: [
    {
      type: "pie",
      radius: ["72%", "92%"],
      center: ["50%", "50%"],
      padAngle: shares.value.length > 1 ? 3 : 0,
      label: { show: false },
      emphasis: { scaleSize: 4 },
      labelLine: { show: false },
      data: shares.value.map((row) => ({
        name: row.name,
        value: row.value,
        itemStyle: { color: row.color, borderRadius: 4 },
      })),
    },
  ],
}));
const tokenShare = (row: ModelMetrics) =>
  data.value?.summary.total_tokens
    ? (row.total_tokens / data.value.summary.total_tokens) * 100
    : 0;
onMounted(load);
</script>

<template>
  <div class="analytics-panel" :aria-busy="busy">
    <div class="analytics-toolbar">
      <div
        class="segmented range-control"
        role="group"
        aria-label="统计时间范围"
      >
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
      <div class="analytics-selectors">
        <label class="sr-only" for="analytics-model">模型</label>
        <select
          id="analytics-model"
          v-model="model"
          :disabled="busy"
          @change="load"
        >
          <option value="">全部模型</option>
          <option
            v-if="model && !availableModels.includes(model)"
            :value="model"
          >
            {{ model }}
          </option>
          <option v-for="name in availableModels" :key="name" :value="name">
            {{ name }}
          </option>
        </select>
        <label class="sr-only" for="analytics-kind">来源</label>
        <select
          id="analytics-kind"
          v-model="kind"
          :disabled="busy"
          @change="load"
        >
          <option value="production">正式请求</option>
          <option value="test">后台试跑</option>
          <option value="all">全部来源</option>
        </select>
        <button
          class="icon-button"
          title="刷新数据"
          aria-label="刷新数据"
          @click="load"
          :disabled="busy"
        >
          <AppIcon name="refresh" :class="{ spinning: busy }" :size="16" />
        </button>
      </div>
    </div>
    <div class="analytics-dimensions">
      <label
        >策略<select v-model="policyID" :disabled="busy" @change="load">
          <option value="">全部策略</option>
          <option v-for="p in policies" :key="p.id" :value="p.id">
            {{ p.name }}
          </option>
        </select></label
      >
      <label
        >调用方<select v-model="clientID" :disabled="busy" @change="load">
          <option value="">全部调用方</option>
          <option v-for="c in clients" :key="c.id" :value="c.id">
            {{ c.name }}
          </option>
        </select></label
      >
      <label
        >模型通道<select v-model="channelID" :disabled="busy" @change="load">
          <option value="">全部通道</option>
          <option v-for="c in channels" :key="c.id" :value="c.id">
            {{ c.name }}
          </option>
        </select></label
      >
      <button
        v-if="policyID || clientID || channelID"
        class="icon-button"
        title="清除维度筛选"
        aria-label="清除维度筛选"
        :disabled="busy"
        @click="
          policyID = '';
          clientID = '';
          channelID = '';
          load();
        "
      >
        <AppIcon name="close" :size="16" />
      </button>
    </div>
    <form v-if="range === 'custom'" class="custom-range" @submit.prevent="load">
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
      <button class="primary" :disabled="busy">应用范围</button>
    </form>
    <div v-if="error" class="banner error" role="alert">
      {{ error
      }}<button :disabled="busy" @click="load">
        <AppIcon name="refresh" :size="15" />重试
      </button>
    </div>
    <div
      v-if="busy"
      class="analytics-skeleton"
      role="status"
      aria-label="正在加载分析数据"
    >
      <div class="metric-grid">
        <div v-for="i in 4" :key="i" class="skeleton-metric">
          <span /><strong /><span />
        </div>
      </div>
      <div class="skeleton-chart" />
      <span class="sr-only">正在加载分析数据</span>
    </div>
    <template v-else-if="data">
      <div class="analytics-period">
        <span
          ><AppIcon name="calendar" :size="13" />{{ bucketLabel(data.from) }} 至
          {{ bucketLabel(data.to) }}</span
        ><span>北京时间 · 结束时刻不含 · 按{{ interval }}统计</span>
      </div>
      <div class="metric-grid analytics-metrics">
        <button
          v-for="m in metricOptions"
          :key="m.value"
          class="metric"
          :class="{ selected: metric === m.value }"
          :aria-pressed="metric === m.value"
          @click="metric = m.value"
          :style="{ '--metric-color': m.color }"
        >
          <span class="metric-label"
            >{{ m.label
            }}<span class="metric-icon"
              ><AppIcon :name="m.icon" :size="17" /></span
          ></span>
          <template v-if="m.value === 'total_tokens'">
            <strong>{{ count(data.summary.total_tokens) }}</strong>
            <small
              >输入 {{ compact(data.summary.input_tokens)
              }}<span class="separator">/</span>输出
              {{ compact(data.summary.output_tokens) }}</small
            >
          </template>
          <template v-else-if="m.value === 'known_cost_cny'">
            <strong
              class="money-value"
              :title="money(data.summary.known_cost_cny)"
              >{{ money(data.summary.known_cost_cny) }}</strong
            >
            <small>含估算 {{ money(data.summary.estimated_cost_cny) }}</small>
          </template>
          <template v-else-if="m.value === 'avg_latency_ms'">
            <strong>{{ latency(data.summary.avg_latency_ms) }}</strong>
            <small>{{ count(data.summary.latency_samples) }} 次耗时样本</small>
          </template>
          <template v-else>
            <strong
              >{{ speed(data.summary.output_tokens_per_second) }}
              <span
                v-if="data.summary.output_tokens_per_second != null"
                class="metric-unit"
                >token/s</span
              ></strong
            >
            <small>输出 Token / 对应调用总耗时</small>
          </template>
        </button>
      </div>
      <div class="analytics-context">
        <span
          ><i
            class="legend-dot"
            :style="{ background: palette[0] }"
          />实际模型调用 <b>{{ count(data.summary.calls) }}</b> 次</span
        >
        <span
          ><i
            class="legend-dot"
            :style="{ background: palette[1] }"
          />本地缓存命中 <b>{{ count(data.summary.cache_hits) }}</b> 次</span
        >
        <span v-if="data.summary.unknown_usage" class="pending-note"
          >用量未知 {{ count(data.summary.unknown_usage) }} 次</span
        >
        <span v-if="data.summary.pending_costs" class="pending-note"
          >待核对 {{ count(data.summary.pending_costs) }} 笔</span
        >
      </div>
      <div class="performance-strip">
        <span
          >调用成功率<strong>{{
            percent(data.summary.successful_calls, data.summary.outcome_samples)
          }}</strong
          ><small
            >{{ count(data.summary.outcome_samples || 0) }} 次结果样本</small
          ></span
        >
        <span
          >缓存命中率<strong>{{
            percent(
              data.summary.cache_hits,
              data.summary.calls + data.summary.cache_hits,
            )
          }}</strong></span
        >
        <span
          >P95 调用耗时<strong>{{
            latency(data.summary.p95_latency_ms)
          }}</strong></span
        >
        <span
          >P99 调用耗时<strong>{{
            latency(data.summary.p99_latency_ms)
          }}</strong></span
        >
        <button class="text-button" @click="logsFor()">
          <AppIcon name="file" :size="16" />查看审核记录
        </button>
      </div>
      <div class="analytics-visuals">
        <section class="trend-section">
          <div class="panel-heading">
            <div>
              <h2>{{ currentMetric.label }}趋势</h2>
              <p class="muted small">
                {{ model || "全部模型" }} · 每{{ interval }}
              </p>
            </div>
            <div class="row">
              <div class="segmented" role="group" aria-label="图表类型">
                <button
                  class="icon-button"
                  :aria-pressed="chartType === 'line'"
                  aria-label="折线图"
                  title="折线图"
                  @click="chartType = 'line'"
                >
                  <AppIcon name="chart" :size="16" />
                </button>
                <button
                  class="icon-button"
                  :aria-pressed="chartType === 'bar'"
                  aria-label="柱状图"
                  title="柱状图"
                  @click="chartType = 'bar'"
                >
                  <AppIcon name="bars" :size="16" />
                </button>
              </div>
              <button
                class="icon-button"
                aria-label="下载趋势图"
                title="下载趋势图"
                :disabled="!data.summary.records || !hasSamples"
                @click="trend?.download('模型' + currentMetric.label + '趋势')"
              >
                <AppIcon name="download" :size="16" />
              </button>
            </div>
          </div>
          <div v-if="!data.summary.records" class="chart-empty">
            <AppIcon name="chart" :size="32" /><strong>暂无调用数据</strong
            ><span>所选范围没有模型调用或费用记录</span>
          </div>
          <div v-else-if="!hasSamples" class="chart-empty">
            <AppIcon name="clock" :size="32" /><strong>暂无耗时样本</strong
            ><span>所选范围没有可用的速度或耗时数据</span>
          </div>
          <div v-else class="trend-chart">
            <DataChart
              ref="trend"
              :option="trendOption"
              :label="
                currentMetric.label +
                '趋势，按' +
                interval +
                '统计，数据见下方时间明细'
              "
              @select="logsFor(data.model, '', $event)"
            />
          </div>
        </section>
        <section class="distribution-section">
          <div class="panel-heading">
            <h2>模型占比</h2>
            <label class="sr-only" for="share-metric">占比指标</label
            ><select id="share-metric" v-model="shareMetric">
              <option value="total_tokens">Token 用量</option>
              <option value="calls">调用次数</option>
              <option value="known_cost_cny">已知费用</option>
            </select>
          </div>
          <div v-if="!shareTotal" class="chart-empty">
            <AppIcon name="cpu" :size="32" /><strong
              >暂无{{ shareLabel }}数据</strong
            >
          </div>
          <template v-else>
            <div class="donut-chart">
              <DataChart
                :option="shareOption"
                :label="shareLabel + '模型占比，数据见下方模型列表'"
                @select="selectModel"
              />
              <div class="donut-center">
                <strong>{{ shares.filter((s) => s.value > 0).length }}</strong
                ><span>活跃模型</span>
              </div>
            </div>
            <div class="share-legend">
              <button
                v-for="row in shares"
                :key="row.name"
                :disabled="busy"
                @click="selectModel(row.name)"
                :title="row.name"
              >
                <i
                  class="legend-dot"
                  :style="{ background: row.color }"
                /><span>{{ row.name }}</span
                ><strong
                  >{{ ((row.value / shareTotal) * 100).toFixed(1) }}%</strong
                >
              </button>
            </div>
          </template>
        </section>
      </div>
      <details class="analytics-method">
        <summary>
          <AppIcon name="help" :size="14" />统计口径<AppIcon
            name="down"
            :size="13"
          />
        </summary>
        <p>
          重试分别计入对应模型，同名模型跨通道合计。本地缓存不重复计入模型 Token
          与耗时。已知费用包含估算，待核对金额未计入；未知用量不按零估算。
        </p>
        <p>
          平均耗时包含失败调用；速度按输出 Token
          总数除以对应调用总耗时计算，包含准备与响应等待。缺少耗时的历史记录不参与平均值，样本数见模型明细。
        </p>
        <p>
          调用成功率按有结果样本的实际上游尝试计算，缺少历史结果的调用不算成功；请求级统计仅覆盖仍在保留期内的审核记录。
        </p>
      </details>
      <section class="panel model-comparison">
        <div class="panel-heading">
          <div class="row">
            <h2>模型对比</h2>
            <span class="badge gray">{{ data.models.length }} 个模型</span>
          </div>
          <button
            v-if="model"
            class="text-button"
            :disabled="busy"
            @click="selectModel('')"
          >
            <AppIcon name="back" :size="15" />全部模型
          </button>
        </div>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>模型</th>
                <th>调用 / 缓存</th>
                <th>Token 用量</th>
                <th>已知费用</th>
                <th>平均耗时</th>
                <th>输出速度</th>
                <th>调用成功率 / P95</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(row, i) in data.models" :key="row.model">
                <td>
                  <button
                    class="text-button model-name"
                    :disabled="busy"
                    @click="selectModel(row.model)"
                  >
                    <span
                      class="model-symbol"
                      :style="{
                        color: palette[i % palette.length],
                        background: palette[i % palette.length] + '10',
                      }"
                      ><AppIcon name="cpu" :size="16" /></span
                    >{{ row.model }}<AppIcon name="chevron" :size="13" />
                  </button>
                </td>
                <td>
                  {{ count(row.calls)
                  }}<small>缓存 {{ count(row.cache_hits) }}</small>
                </td>
                <td>
                  <div class="token-cell">
                    <strong>{{ count(row.total_tokens) }}</strong
                    ><span>{{ tokenShare(row).toFixed(1) }}%</span>
                  </div>
                  <div class="token-track">
                    <span
                      :style="{
                        width: tokenShare(row) + '%',
                        background: palette[i % palette.length],
                      }"
                    />
                  </div>
                  <small
                    >输入 {{ count(row.input_tokens) }} / 输出
                    {{ count(row.output_tokens) }}</small
                  ><small v-if="row.unknown_usage" class="pending-note"
                    >{{ row.unknown_usage }} 次用量未知</small
                  >
                </td>
                <td>
                  {{ money(row.known_cost_cny)
                  }}<small v-if="row.pending_costs" class="pending-note"
                    >{{ row.pending_costs }} 笔待核对</small
                  ><small v-if="Number(row.estimated_cost_cny) !== 0"
                    >含估算 {{ money(row.estimated_cost_cny) }}</small
                  >
                </td>
                <td>
                  {{ latency(row.avg_latency_ms)
                  }}<small>{{ count(row.latency_samples) }} 次样本</small>
                </td>
                <td>
                  {{ speed(row.output_tokens_per_second)
                  }}<small v-if="row.output_tokens_per_second != null"
                    >token/s</small
                  >
                </td>
                <td>
                  {{ percent(row.successful_calls, row.outcome_samples)
                  }}<small
                    >{{ row.failed_calls || 0 }} 次失败 /
                    {{ row.outcome_samples || 0 }} 次样本</small
                  ><small>P95 {{ latency(row.p95_latency_ms) }}</small>
                </td>
                <td>
                  <button
                    class="icon-button"
                    :aria-label="'查看 ' + row.model + ' 审核记录'"
                    title="查看审核记录"
                    @click="logsFor(row.model)"
                  >
                    <AppIcon name="file" :size="16" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="!data.models.length" class="empty">没有符合条件的模型记录</p>
      </section>
      <section v-if="data.requests" class="panel">
        <div class="panel-heading">
          <h2>请求记录统计</h2>
          <span class="muted small">仅含仍在保留期内的审核记录</span>
        </div>
        <div class="performance-strip request-performance">
          <span
            >请求数<strong>{{ count(data.requests.records) }}</strong></span
          >
          <span
            >请求成功率<strong>{{
              percent(
                data.requests.allowed + data.requests.flagged,
                data.requests.records,
              )
            }}</strong></span
          >
          <span
            >策略命中<strong>{{ count(data.requests.flagged) }}</strong></span
          >
          <span
            >多次调用比例<strong>{{
              percent(data.requests.fallbacks, data.requests.records)
            }}</strong></span
          >
          <span
            >P50 / P95 / P99<strong class="percentile-values"
              >{{ latency(data.requests.p50_latency_ms) }} /
              {{ latency(data.requests.p95_latency_ms) }} /
              {{ latency(data.requests.p99_latency_ms) }}</strong
            ></span
          >
        </div>
      </section>
      <section v-if="data.errors?.length" class="panel">
        <div class="panel-heading"><h2>调用错误分布</h2></div>
        <div class="error-distribution">
          <button
            v-for="item in data.errors"
            :key="item.code"
            @click="logsFor(data.model, item.code)"
          >
            <code>{{ item.code }}</code
            ><strong>{{ count(item.calls) }}</strong
            ><AppIcon name="file" :size="14" />
          </button>
        </div>
      </section>
      <details v-if="data.summary.records" class="time-details">
        <summary>
          <span><AppIcon name="calendar" :size="16" />每{{ interval }}明细</span
          ><span class="muted"
            >{{ data.series.length }} 个时间段<AppIcon name="down" :size="14"
          /></span>
        </summary>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>时间（北京时间）</th>
                <th></th>
                <th>模型调用</th>
                <th>Token</th>
                <th>已知费用</th>
                <th>平均耗时</th>
                <th>输出速度</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in data.series" :key="p.time">
                <td :title="time(p.time)">{{ bucketLabel(p.time) }}</td>
                <td>
                  <button
                    class="icon-button"
                    title="查看时段记录"
                    :aria-label="'查看 ' + bucketLabel(p.time) + ' 记录'"
                    @click="logsFor(data.model, '', p.time)"
                  >
                    <AppIcon name="file" :size="14" />
                  </button>
                </td>
                <td>{{ count(p.calls) }}</td>
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
                <td>
                  {{ speed(p.output_tokens_per_second)
                  }}{{ p.output_tokens_per_second == null ? "" : " token/s" }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </details>
    </template>
  </div>
</template>

<style scoped>
.analytics-dimensions {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: end;
  margin-top: 16px;
}
.analytics-dimensions label {
  flex: 1;
  min-width: 140px;
}
.analytics-dimensions select {
  font-size: 12px;
}
.performance-strip {
  display: flex;
  gap: 20px;
  flex-wrap: wrap;
  align-items: center;
  padding: 0 0 20px;
}
.performance-strip > span {
  flex: 1;
  min-width: 120px;
  color: var(--muted);
  font-size: 11px;
}
.performance-strip strong {
  display: block;
  color: #445269;
  font-size: 19px;
  font-weight: 550;
}
.performance-strip small {
  display: block;
  font-size: 10px;
}
.request-performance .percentile-values {
  font-size: 13px;
  white-space: nowrap;
}
.error-distribution {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  padding-bottom: 16px;
}
.error-distribution button {
  gap: 10px;
}
.analytics-toolbar {
  display: flex;
  justify-content: space-between;
  gap: 14px;
  flex-wrap: wrap;
}
.analytics-selectors {
  display: flex;
  align-items: center;
  gap: 8px;
}
.analytics-selectors select {
  font-size: 12px;
  width: 145px;
  padding: 8px 10px;
  height: 36px;
}
.analytics-selectors select:nth-of-type(2) {
  width: 108px;
}
.custom-range {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  align-items: end;
  margin-top: 18px;
}
.custom-range label {
  flex: 1;
  min-width: 210px;
}
.analytics-period {
  display: flex;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 6px;
  margin: 20px 0 14px;
  font-size: 10px;
  color: #939cac;
}
.analytics-period > span {
  display: flex;
  align-items: center;
  gap: 6px;
}
.analytics-panel > .banner {
  margin-top: 20px;
}
.analytics-metrics .metric {
  text-align: left;
  align-items: stretch;
  gap: 11px;
  position: relative;
  transition:
    border-color 0.18s,
    background 0.18s;
}
.analytics-metrics .metric:hover {
  background: #fafbff;
}
.analytics-metrics .metric.selected {
  border-color: #b8caf7;
  background: #fcfdff;
  box-shadow: 0 2px 8px #3565e807;
}
.analytics-metrics .metric.selected::after {
  content: "";
  position: absolute;
  bottom: -1px;
  left: 20px;
  right: 20px;
  height: 2px;
  background: var(--metric-color);
}
.metric-icon {
  width: 28px;
  height: 28px;
  border-radius: 6px;
  display: grid;
  place-items: center;
  background: color-mix(in srgb, var(--metric-color) 8%, white);
  color: var(--metric-color);
}
.metric-icon .app-icon {
  color: inherit;
}
.analytics-metrics strong {
  color: #293244;
  font-weight: 550;
}
.analytics-metrics small {
  line-height: 1.6;
  overflow-wrap: anywhere;
}
.metric-unit {
  font-size: 12px;
  font-weight: 400;
  color: #8d96a5;
  white-space: nowrap;
}
.money-value {
  font-size: 25px;
}
.separator {
  margin: 0 7px;
  color: #c1c8d4;
}
.analytics-context {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px 22px;
  padding: 18px 0 23px;
  color: #8a94a5;
  font-size: 11px;
}
.analytics-context > span {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.analytics-context b {
  font-weight: 500;
  color: #5d6b80;
}
.legend-dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
}
.pending-note {
  color: #b38131;
}
.analytics-visuals {
  display: grid;
  grid-template-columns: minmax(0, 2.1fr) minmax(235px, 1fr);
  border-top: 1px solid var(--border);
}
.trend-section {
  min-width: 0;
  padding-right: 28px;
}
.distribution-section {
  min-width: 0;
  padding-left: 28px;
  border-left: 1px solid var(--border);
}
.trend-section .panel-heading {
  min-height: 87px;
}
.distribution-section select {
  width: auto;
  max-width: 118px;
  padding: 6px 8px;
  font-size: 11px;
  border-color: transparent;
  background: #f7f8fb;
}
.trend-chart,
.chart-empty {
  height: 280px;
}
.chart-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  color: #98a6bd;
  font-size: 11px;
  text-align: center;
}
.chart-empty strong {
  font-size: 13px;
  font-weight: 500;
  color: #78879f;
}
.chart-empty > .app-icon {
  margin-bottom: 3px;
  color: #b2bfd5;
}
.donut-chart {
  width: 158px;
  height: 158px;
  position: relative;
  margin: 0 auto 12px;
}
.donut-center {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  pointer-events: none;
}
.donut-center strong {
  font-size: 29px;
  font-weight: 550;
  color: #3f4c64;
}
.donut-center span {
  font-size: 10px;
  color: #8e98a8;
}
.share-legend {
  display: grid;
  gap: 4px;
  max-height: 125px;
  overflow-y: auto;
  padding-bottom: 9px;
}
.share-legend button {
  display: flex;
  border: 0;
  background: transparent;
  width: 100%;
  padding: 4px 2px;
  min-height: 27px;
  gap: 8px;
  font-size: 11px;
}
.share-legend button > span {
  flex: 1;
  min-width: 0;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #8590a2;
}
.share-legend strong {
  font-size: 11px;
  font-weight: 500;
  color: #5b6980;
}
.analytics-method {
  color: #8a94a6;
  font-size: 11px;
  margin: 18px 0 22px;
}
.analytics-method summary {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  cursor: pointer;
}
.analytics-method p {
  margin: 10px 0 0;
  max-width: 900px;
  line-height: 1.8;
}
.analytics-method summary::-webkit-details-marker,
.time-details summary::-webkit-details-marker {
  display: none;
}
.model-comparison {
  margin-bottom: 18px;
}
.model-name {
  color: #4e5e7b;
  gap: 9px;
  font-weight: 500;
}
.model-name > .app-icon {
  color: #a8b3c5;
}
.model-symbol {
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  border-radius: 6px;
}
.token-cell {
  min-width: 130px;
  display: flex;
  justify-content: space-between;
  gap: 12px;
}
.token-cell > span {
  color: #9ca6b7;
  font-size: 10px;
}
.token-track {
  height: 3px;
  width: 100%;
  background: #edf1f8;
  margin-top: 7px;
  border-radius: 2px;
  overflow: hidden;
}
.token-track span {
  display: block;
  height: 100%;
  border-radius: 2px;
}
.time-details {
  border-top: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
}
.time-details summary {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 17px 0;
  cursor: pointer;
  font-size: 12px;
}
.time-details summary > span {
  display: flex;
  gap: 9px;
  align-items: center;
}
.time-details summary > .muted {
  font-size: 11px;
}
.analytics-skeleton {
  margin-top: 26px;
}
.skeleton-metric {
  padding: 20px;
  border: 1px solid var(--border);
  border-radius: 7px;
  display: grid;
  gap: 16px;
}
.skeleton-metric span,
.skeleton-metric strong,
.skeleton-chart {
  display: block;
  background: #f2f4f8;
  border-radius: 4px;
  animation: pulse 1.2s ease-in-out infinite alternate;
}
.skeleton-metric span {
  height: 12px;
  width: 65%;
}
.skeleton-metric strong {
  height: 30px;
  width: 80%;
}
.skeleton-chart {
  height: 300px;
  margin-top: 35px;
}
@keyframes pulse {
  to {
    opacity: 0.45;
  }
}
@media (max-width: 1200px) {
  .trend-section {
    padding-right: 20px;
  }
  .distribution-section {
    padding-left: 20px;
  }
  .range-control button {
    padding-inline: 10px;
  }
}
@media (max-width: 1000px) {
  .analytics-visuals {
    grid-template-columns: minmax(0, 1fr);
  }
  .trend-section {
    padding-right: 0;
  }
  .distribution-section {
    padding-left: 0;
    border-left: 0;
    border-top: 1px solid var(--border);
    margin-top: 24px;
    display: grid;
    grid-template-columns: 170px minmax(0, 1fr);
    gap: 0 25px;
    align-items: center;
  }
  .distribution-section .panel-heading {
    grid-column: 1 / -1;
  }
  .distribution-section .chart-empty {
    grid-column: 1 / -1;
    height: 190px;
  }
  .share-legend {
    max-height: 150px;
  }
  .donut-chart {
    margin-bottom: 0;
  }
}
@media (max-width: 760px) {
  .analytics-toolbar {
    gap: 12px;
  }
  .range-control {
    width: 100%;
    justify-content: space-between;
  }
  .range-control button {
    padding-inline: 8px;
    font-size: 11px;
  }
  .analytics-selectors {
    width: 100%;
  }
  .analytics-selectors select {
    width: 0;
    flex: 1;
  }
  .analytics-selectors select:nth-of-type(2) {
    flex: 0 0 107px;
  }
  .analytics-metrics .metric {
    gap: 10px;
  }
  .analytics-metrics strong {
    font-size: 23px;
  }
  .analytics-metrics .money-value {
    font-size: 20px;
  }
  .metric-icon {
    width: 24px;
    height: 24px;
  }
  .metric-unit {
    font-size: 10px;
  }
  .analytics-context {
    gap: 9px 15px;
    padding-bottom: 20px;
  }
  .trend-chart {
    height: 260px;
  }
  .trend-section .panel-heading {
    gap: 8px;
  }
  .trend-section .panel-heading > .row {
    gap: 5px;
  }
  .distribution-section {
    gap: 0 12px;
    grid-template-columns: 145px minmax(0, 1fr);
  }
  .donut-chart {
    width: 140px;
    height: 140px;
  }
  .custom-range {
    gap: 12px;
  }
  .custom-range label {
    min-width: 100%;
  }
}
@media (max-width: 370px) {
  .range-control button {
    padding-inline: 5px;
  }
  .analytics-metrics strong {
    font-size: 20px;
  }
}
</style>
