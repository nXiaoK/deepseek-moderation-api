import type { Page } from "@playwright/test";

const modelNames = [
  "deepseek-v3.2",
  "grok-4-fast",
  "deepseek-r1",
  "grok-3-mini",
];
export const config = {
  store_model_output: false,
  model_output_retention_days: 7,
  prompt: "你是内容审核员。根据审核策略输出 JSON，包含 confidence 与 reason。",
  threshold: 0.8,
  store_input: false,
  retention_days: 30,
  total_timeout_ms: 9000,
  max_attempts: 3,
  result_cache_ttl_seconds: 0,
  channels: [
    { channel_id: "channel-1", priority: 1, weight: 100, enabled: true },
  ],
};
const policies = [
  {
    id: "policy-1",
    name: "默认内容审核",
    alias: "abuse-audit-v1",
    enabled: true,
    revision: 1,
    config,
    updated_at: "2026-09-13T04:00:00Z",
  },
];
const keys = [
  {
    id: "client-1",
    name: "生产应用",
    prefix: "audit_demo",
    policy_ids: ["policy-1"],
    rpm: 60,
    active: true,
    created_at: "2026-09-12T04:00:00Z",
  },
];
const credentials = [
  {
    id: "credential-1",
    name: "DeepSeek 主账户",
    provider: "deepseek",
    base_url: "https://api.deepseek.com",
    masked: "sk-••••demo",
    active: true,
  },
];
const channels = [
  {
    id: "channel-1",
    name: "DeepSeek 主用",
    provider: "deepseek",
    base_url: "https://api.deepseek.com",
    model: modelNames[0],
    credential_id: "credential-1",
    credential_active: true,
    timeout_ms: 4000,
    max_tokens: 512,
    max_concurrency: 8,
    text_only: true,
    enabled: true,
    revision: 1,
    policy_names: ["默认内容审核"],
    health: { status: "ready", in_flight: 2, calls: 824, failures: 3 },
  },
];
const usage = {
  reported: true,
  prompt_tokens: 1250,
  completion_tokens: 50,
  total_tokens: 1300,
  prompt_cache_hit_tokens: 200,
  prompt_cache_miss_tokens: 1050,
};
const cost = {
  status: "calculated",
  amount_cny: "0.00325",
  reserved_cny: "0",
  period: "off",
  note: "按请求时价格计费",
  price_id: 1,
};
const log = {
  id: "audit-1",
  kind: "production",
  policy_id: "policy-1",
  client_id: "client-1",
  model: modelNames[0],
  provider: "deepseek",
  channel_id: "channel-1",
  attempt_count: 1,
  flagged: false,
  confidence: 0.05,
  threshold: 0.8,
  reason: "未命中审核策略",
  error_code: "",
  latency_ms: 1260,
  usage,
  cost,
  input_stored: false,
  created_at: "2026-09-13T04:30:00Z",
  request: {
    method: "POST",
    path: "/v1/moderations",
    model: "abuse-audit-v1",
    stage: "completed",
    http_status: 200,
    text_chars: 320,
    image_count: 0,
  },
  attempts: [],
};
const zero = {
  records: 0,
  calls: 0,
  cache_hits: 0,
  input_tokens: 0,
  output_tokens: 0,
  total_tokens: 0,
  unknown_usage: 0,
  known_cost_cny: "0",
  estimated_cost_cny: "0",
  reserved_cny: "0",
  pending_costs: 0,
  avg_latency_ms: null as number | null,
  latency_samples: 0,
  output_tokens_per_second: null as number | null,
};
export function analytics(url: URL, state = "populated") {
  const selected = url.searchParams.get("model");
  const names = selected ? [selected] : modelNames;
  const points = [
    3200, 4600, 4100, 7800, 6300, 11200, 8100, 7500, 5900, 8500, 9100, 7800,
    12900, 10800, 12400, 9000, 7800, 15600, 11200, 13200, 16100, 11400, 13800,
    9300,
  ];
  const series = points.map((n, i) => {
    const calls = Math.round(n / 250);
    return {
      ...zero,
      time: new Date(Date.UTC(2026, 8, 12, 16 + i)).toISOString(),
      records: calls + 2,
      calls,
      cache_hits: 2,
      input_tokens: n * 3,
      output_tokens: n,
      total_tokens: n * 4,
      known_cost_cny: (n / 100000).toFixed(4),
      estimated_cost_cny: "0.0012",
      pending_costs: i % 6 === 0 ? 1 : 0,
      avg_latency_ms: i === 6 || state === "no-latency" ? null : 1200 + i * 12,
      latency_samples: i === 6 || state === "no-latency" ? 0 : calls,
      output_tokens_per_second:
        i === 6 || state === "no-latency" ? null : 25 + i * 0.2,
    };
  });
  const sum = (
    key:
      | "total_tokens"
      | "input_tokens"
      | "output_tokens"
      | "calls"
      | "cache_hits"
      | "records"
      | "latency_samples"
      | "pending_costs",
  ) => series.reduce((n, p) => n + p[key], 0);
  const summary = {
    ...zero,
    records: sum("records"),
    calls: sum("calls"),
    cache_hits: sum("cache_hits"),
    total_tokens: sum("total_tokens"),
    input_tokens: sum("input_tokens"),
    output_tokens: sum("output_tokens"),
    known_cost_cny: series
      .reduce((n, p) => n + Number(p.known_cost_cny), 0)
      .toFixed(4),
    estimated_cost_cny: "0.0288",
    pending_costs: sum("pending_costs"),
    unknown_usage: 2,
    avg_latency_ms: state === "no-latency" ? null : 1372,
    latency_samples: sum("latency_samples"),
    output_tokens_per_second: state === "no-latency" ? null : 27.42,
  };
  const weights = selected ? [1] : [0.48, 0.28, 0.16, 0.08];
  const models = names.map((name, i) => ({
    ...summary,
    model: name,
    calls: Math.round(summary.calls * weights[i]),
    cache_hits: Math.round(summary.cache_hits * weights[i]),
    total_tokens: Math.round(summary.total_tokens * weights[i]),
    input_tokens: Math.round(summary.input_tokens * weights[i]),
    output_tokens: Math.round(summary.output_tokens * weights[i]),
    known_cost_cny: (Number(summary.known_cost_cny) * weights[i]).toFixed(4),
    avg_latency_ms: state === "no-latency" ? null : 1280 + i * 330,
    output_tokens_per_second: state === "no-latency" ? null : 25.4 + i * 3,
  }));
  return {
    from: "2026-09-12T16:00:00Z",
    to: "2026-09-13T16:00:00Z",
    kind: url.searchParams.get("kind"),
    model: selected || "",
    interval_seconds: 3600,
    available_models: modelNames,
    summary: state === "empty" ? zero : summary,
    models: state === "empty" ? [] : models,
    series: state === "empty" ? [] : series,
  };
}
export async function mockAPI(page: Page) {
  await page.route("**/admin/**", async (route) => {
    const url = new URL(route.request().url());
    const responses: Record<string, unknown> = {
      "/admin/session": { username: "admin", csrf: "test-csrf" },
      "/admin/policies": policies,
      "/admin/policies/policy-1": policies[0],
      "/admin/credentials": credentials,
      "/admin/api-keys": keys,
      "/admin/model-channels": channels,
      "/admin/overview": {
        requests: 2846,
        flagged: 124,
        errors: 8,
        tokens: 964200,
        avg_latency_ms: 1372,
        p95_latency_ms: 2840,
      },
      "/admin/audit-logs": { items: [log], total: 1 },
      "/admin/audit-logs/audit-1": log,
      "/admin/actions": [
        {
          username: "admin",
          action: "保存策略配置",
          resource_id: "policy-1",
          created_at: "2026-09-13T04:00:00Z",
        },
      ],
      "/admin/billing/costs": {
        items: [
          {
            id: "cost-1",
            request_id: "audit-1",
            client_id: "client-1",
            kind: "production",
            model: modelNames[0],
            started_at: "2026-09-13T04:30:00Z",
            usage,
            cost,
          },
        ],
        total: 1,
        summary: {
          calculated_cny: "24.382",
          estimated_cny: "0.185",
          reserved_cny: "0.026",
          pending_count: 3,
          local_cache_hits: 164,
          cache_hit_tokens: 284650,
          cache_miss_tokens: 580320,
          output_tokens: 12450,
        },
      },
      "/admin/billing/budgets": [
        {
          client_id: "client-1",
          name: "生产应用",
          revision: 1,
          daily_limit_cny: "10",
          monthly_limit_cny: "100",
          daily_used_cny: "2.38",
          daily_reserved_cny: "0.02",
          monthly_used_cny: "24.38",
          monthly_reserved_cny: "0.026",
        },
      ],
      "/admin/billing/prices": [
        {
          id: 1,
          model: modelNames[0],
          rates: {
            off_hit: 200000,
            off_miss: 2000000,
            off_output: 3000000,
            peak_hit: 300000,
            peak_miss: 3000000,
            peak_output: 4000000,
          },
          source: "供应商公布价格",
          effective_at: "2026-09-12T00:00:00Z",
        },
      ],
    };
    if (url.pathname === "/admin/analytics")
      return route.fulfill({ json: analytics(url) });
    if (route.request().method() !== "GET")
      return route.fulfill({
        status: 400,
        json: { error: { message: "此测试没有配置写入响应" } },
      });
    if (!(url.pathname in responses))
      throw new Error("Missing fixture: " + url.pathname);
    await route.fulfill({ json: responses[url.pathname] });
  });
}
