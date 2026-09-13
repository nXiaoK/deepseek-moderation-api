export type Provider = "deepseek" | "grok_via_sub2api";
export interface AnalysisLogFilter {
  from: string;
  to: string;
  kind: string;
  model: string;
  policy_id: string;
  client_id: string;
  channel_id: string;
  error_code?: string;
}
export interface ChannelBinding {
  channel_id: string;
  priority: number;
  weight: number;
  enabled: boolean;
}
export interface Config {
  store_model_output?: boolean;
  model_output_retention_days?: number;
  result_cache_ttl_seconds: number;
  prompt: string;
  threshold: number;
  store_input: boolean;
  retention_days: number;
  total_timeout_ms: number;
  max_attempts: number;
  channels: ChannelBinding[];
}
export interface Policy {
  id: string;
  name: string;
  alias: string;
  enabled: boolean;
  revision: number;
  config: Config;
  updated_at: string;
}
export interface ModelChannel {
  id: string;
  name: string;
  provider: Provider;
  base_url: string;
  model: string;
  credential_id: string;
  credential_active: boolean;
  timeout_ms: number;
  max_tokens: number;
  max_concurrency: number;
  text_only: boolean;
  enabled: boolean;
  revision: number;
  policy_names: string[];
  health: {
    status: string;
    in_flight: number;
    calls: number;
    failures: number;
    cooldown_until?: string;
  };
}
export interface AuditAttempt {
  input_scope?: string;
  image_count?: number;
  error_message?: string;
  model_output?: string;
  id: string;
  channel_id: string;
  channel_name: string;
  provider: Provider;
  model: string;
  error_code: string;
  latency_ms: number;
  cache_hit: boolean;
  sent: boolean;
  usage: Usage;
  cost?: CostView;
}
export interface Credential {
  provider: Provider;
  base_url: string;
  id: string;
  name: string;
  masked: string;
  active: boolean;
}
export interface ClientKey {
  id: string;
  name: string;
  prefix: string;
  policy_ids: string[];
  rpm: number;
  active: boolean;
  created_at: string;
}
export interface Metadata {
  schema_version: number;
  policy_id: string;
  policy_version: number;
  confidence: number;
  threshold: number;
  reason: string;
}
export interface AuditResponse {
  channel_id: string;
  actual_model: string;
  attempt_count: number;
  attempts?: AuditAttempt[];
  provider?: Provider;
  id: string;
  model: string;
  latency_ms: number;
  usage: Usage;
  cost?: CostView;
  cache_hit?: boolean;
  results: {
    flagged: boolean;
    category_scores: Record<string, number>;
    audit: Metadata;
  }[];
}
export interface AuditLog {
  model_output_stored?: boolean;
  request?: {
    method: string;
    path: string;
    model?: string;
    stage: string;
    http_status: number;
    input_type?: string;
    text_chars: number;
    image_count: number;
    text_only_fallback?: boolean;
    input_scope?: string;
  };
  error_message?: string;
  channel_id: string;
  attempt_count: number;
  attempts: AuditAttempt[];
  provider?: Provider;
  cost?: CostView;
  cache_hit?: boolean;
  id: string;
  kind: string;
  policy_id: string;
  client_id: string;
  model: string;
  flagged: boolean;
  confidence: number | null;
  threshold: number;
  reason: string;
  model_output?: string;
  error_code: string;
  latency_ms: number;
  usage: AuditResponse["usage"];
  input_stored: boolean;
  input?: string;
  created_at: string;
}
let csrf = "";
export function setCSRF(token: string) {
  csrf = token;
}
export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(path, {
    signal,
    method,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      ...(csrf ? { "X-CSRF-Token": csrf } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await response
    .json()
    .catch(() => ({ error: { message: "服务返回了无效响应" } }));
  if (!response.ok)
    throw new APIError(response.status, data.error?.message || "请求失败");
  return data as T;
}
export async function downloadFile(
  path: string,
  filename: string,
  signal?: AbortSignal,
) {
  const response = await fetch(path, { credentials: "same-origin", signal });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new APIError(response.status, body?.error?.message || "下载失败");
  }
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export interface Usage {
  actual_model?: string;
  upstream_request_id?: string;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  prompt_cache_hit_tokens?: number;
  prompt_cache_miss_tokens?: number;
  reasoning_tokens?: number;
  reported: boolean;
}
export interface CostView {
  status: string;
  amount_cny: string | null;
  reserved_cny: string;
  price_id?: number;
  period: string;
  note: string;
}
