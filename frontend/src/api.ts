export type Provider = "deepseek" | "grok_via_sub2api";
export interface RuntimeLimits {
  model_concurrency: number;
  request_concurrency: number;
  request_body_mib: number;
  trial_concurrency: number;
  max_images: number;
}
export interface EvaluationSampleSeed {
  name: string;
  input: string;
  expected: "allow" | "flagged" | "manual";
  note: string;
}
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
  keyword_ignore_enabled?: boolean;
  ignore_keywords?: string[];
  store_model_output?: boolean;
  model_output_retention_days?: number;
  result_cache_ttl_seconds: number;
  prompt: string;
  threshold: number;
  store_input: boolean;
  retention_days: number;
  total_timeout_ms: number;
  max_attempts: number;
  failure_threshold?: number;
  failure_cooldown_minutes?: number;
  channels: ChannelBinding[];
}
export interface Policy {
  archived?: boolean;
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
  rpm: number;
  text_only: boolean;
  enabled: boolean;
  revision: number;
  policy_names: string[];
  health: {
    verified?: boolean;
    last_success_at?: string;
    last_failure_at?: string;
    last_error_code?: string;
    status: string;
    in_flight: number;
    calls: number;
    failures: number;
    cooldown_until?: string;
    rpm_used?: number;
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
  revision: number;
  expires_at?: string | null;
  last_used_at?: string | null;
  rotated_at?: string | null;
  id: string;
  name: string;
  prefix: string;
  policy_ids: string[];
  rpm: number;
  active: boolean;
  created_at: string;
}
export interface Metadata {
  keyword_ignored?: boolean;
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
  keyword_ignored?: boolean;
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
let unauthorizedHandler: (() => void) | null = null;
export function setUnauthorizedHandler(handler: (() => void) | null) {
  unauthorizedHandler = handler;
}
export function setCSRF(token: string) {
  csrf = token;
}
export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
    public staleSession = false,
  ) {
    super(message);
  }
}
export function ignoreAPIError(error: unknown) {
  return (
    (error instanceof APIError && error.staleSession) ||
    (error instanceof DOMException && error.name === "AbortError")
  );
}
export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const requestCSRF = csrf;
  const response = await fetch(path, {
    signal,
    method,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      ...(requestCSRF ? { "X-CSRF-Token": requestCSRF } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data;
  try {
    data = await response.json();
  } catch (error) {
    if (signal?.aborted) throw error;
    if (response.ok)
      throw new APIError(502, "服务返回了无效响应", requestCSRF !== csrf);
    data = {
      error: {
        message: response.status === 401 ? "登录已过期" : "服务返回了无效响应",
      },
    };
  }
  if (requestCSRF !== csrf)
    throw new APIError(response.status, "会话已更新，旧请求已忽略", true);
  if (!response.ok) {
    if (response.status === 401 && path !== "/admin/auth/login")
      unauthorizedHandler?.();
    throw new APIError(response.status, data.error?.message || "请求失败");
  }
  return data as T;
}
export async function downloadFile(
  path: string,
  filename: string,
  signal?: AbortSignal,
) {
  const requestCSRF = csrf;
  const response = await fetch(path, { credentials: "same-origin", signal });
  if (requestCSRF !== csrf)
    throw new APIError(response.status, "会话已更新，旧请求已忽略", true);
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    if (requestCSRF !== csrf)
      throw new APIError(response.status, "会话已更新，旧请求已忽略", true);
    if (response.status === 401) unauthorizedHandler?.();
    throw new APIError(response.status, body?.error?.message || "下载失败");
  }
  const blob = await response.blob();
  if (requestCSRF !== csrf)
    throw new APIError(response.status, "会话已更新，旧请求已忽略", true);
  const url = URL.createObjectURL(blob);
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
